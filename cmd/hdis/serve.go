package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/version"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// serve is the daemon: it owns the tick and the bindings, and answers the
// socket both doors dial.
func serve(f *daemonFlags) error {
	// The log is opened here, before anything worth reading is said. Where
	// it goes is the daemon's own call for the same reason the socket, the
	// lock and the bindings are: a shell line that redirects elsewhere can
	// be lost on a restart, and it is only ever missed once the log is
	// already needed.
	if err := config.EnsureStateDir(); err != nil {
		return err
	}
	logOut, logFile, logErr := daemon.OpenLog(f.logPath, os.Stdout)
	if logFile != nil {
		defer logFile.Close()
	}
	log.SetOutput(logOut)
	if logErr != nil {
		f.logPath = ""
		log.Printf("%v; logging to stdout alone", logErr)
	}

	cfg, err := config.Load(f.configPath)
	if err != nil {
		return err
	}
	pane := cfg.BasePaneOr(f.basePane)

	// One daemon per user. The lock is what makes one live worker per task
	// true across every caller rather than per process.
	lock, err := daemon.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	// The board principal carries this daemon's own pane, so a row the board
	// is holding names the daemon that reserved it and a restart can tell
	// its own stale hold from a live peer's.
	board := &htask.Client{Principal: htask.PrincipalFor(pane)}
	pens := herdr.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Discovery: say what is unreachable now rather than failing one task at
	// a time later. It is said, not obeyed: a board that is down comes back,
	// and both doors still have doctor and status to answer with.
	if doc, err := board.Doctor(ctx); err != nil {
		log.Printf("the board could not be read: %v", err)
	} else {
		if !doc.SocketLive {
			log.Printf("the board's daemon is not answering on %s", doc.Binary)
		}
		if !doc.HerdrReachable {
			log.Print("the board cannot reach Herdr, so neither can the dispatcher")
		}
		log.Printf("htask %s (contract %s), ticking every %s", doc.Version, doc.Contract, f.interval)
	}
	// §11.2: feature-detect at daemon start, once, and decide at the verb.
	// It is said and not obeyed — a missing capability is UNSUPPORTED at the
	// verb that needs it, never a refusal to start — and a Herdr that could
	// not be asked is reported rather than assumed either way, so the first
	// verb asks again.
	if schema, err := pens.Schema(ctx); err != nil {
		log.Printf("herdr could not say what it offers, so the first verb that needs a capability will ask again: %v", err)
	} else {
		log.Printf("herdr offers %d request(s) and %d event kind(s) at protocol %d",
			len(schema.Requests), len(schema.Events), schema.Protocol)
		for _, want := range []string{
			herdr.CapTabCreate, herdr.CapPaneSplit, herdr.CapPaneRun, herdr.CapPaneRead,
			herdr.CapAgentStart, herdr.CapAgentGet, herdr.CapPrompt,
		} {
			if !schema.Has(want) {
				log.Printf("this herdr does not offer %s; the verbs that need it will refuse with UNSUPPORTED", want)
			}
		}
	}
	if pane == "" {
		log.Print(`no base pane: nothing will be spawned and dispatch will refuse. ` +
			`Run inside a Herdr pane, pass --pane, or set "pane" in the config.`)
	}

	l := &loop.Loop{
		Board:  board,
		Herdr:  pens,
		Config: cfg,
		Policy: decide.Policy{
			MaxWorkers:   cfg.MaxWorkersOr(f.maxWorkers),
			ClaimTimeout: f.claimTimeout,
			MaxPrompts:   f.maxPrompts,
			// The lane is the config document's call, not a flag: which
			// agent verifies and whether one runs at all is execution
			// policy, and it lives where the profiles live.
			Verify: cfg.Verify.Enabled,
		},
		Spawn: &spawn.Pipeline{
			Herdr:          pens,
			Proxy:          &proxy.Client{Bin: cfg.Proxy},
			StartTimeout:   f.startTimeout,
			DialogCeiling:  20 * time.Second,
			ConfirmCeiling: f.confirmCeiling,
			ShellCeiling:   spawn.DefaultShellCeiling,
			Poll:           2 * time.Second,
			// How many workers may share one tab before the next opens its
			// own, which is the measured readable width turned into a count.
			MaxPanesPerTab: cfg.Layout.MaxPanesPerTab,
			// The bound the reap is drawn at, read the other way round: a
			// pane sitting under it is a worker of this daemon's own, and
			// never the desk a report is owed at.
			OwnTrees: config.WorktreeDir(),
			Log:      log.Default(),
		},
		// Every worker gets a checkout of its own, never the project
		// directory the operator sits in and every other worker would
		// otherwise hold.
		Worktrees: &worktree.Manager{Root: config.WorktreeDir()},
		Store:     &store.Bindings{Path: config.BindingsPath()},
		BasePane:  pane,
		Log:       log.Default(),
	}

	// What the previous daemon left behind is taken back or reaped before
	// anything is dispatched: a worker that was prompted and has not claimed
	// yet keeps the pane it was given and is never handed a second one, a
	// task the board still holds for this daemon is adopted or released, and
	// a checkout no binding names is removed.
	if _, err := l.Adopt(ctx); err != nil {
		log.Printf("%v", err)
	}

	if f.once {
		return l.Tick(ctx)
	}

	ln, err := daemon.Listen()
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("listening on %s", config.SocketPath())

	d := &daemon.Daemon{
		Loop:     l,
		Board:    board,
		Interval: f.interval,
		Version:  version.Version,
		Log:      log.Default(),
		LogPath:  f.logPath,
		Lock:     lock,
	}
	// Every event the loop writes reaches the §8.3 hook and every live
	// `events --follow` through the daemon, which is the one place both
	// live. The loop knows nothing of either.
	l.OnEvent = d.Emitted
	err = d.Serve(ctx, ln)
	log.Print("stopping")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
