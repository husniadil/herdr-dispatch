// Command hdis is the dispatcher: it watches the htask board for ready work,
// brings a worker pane up in Herdr for each ready task, delivers the task's
// goal, tracks the worker, and stops at review.
//
// One binary is the daemon and both doors. `hdis daemon` (or `hdis run`) owns
// the tick and the bindings; `hdis <verb>` and `hdis mcp` are thin clients of
// it, and start one when none is running.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/mcpdoor"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
	"github.com/husniadil/herdr-dispatch/internal/version"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("hdis: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, cli.Usage())
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon", "run":
		if err := serve(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "mcp":
		// The door takes no arguments, and silently ignoring one is how a
		// caller ends up believing it passed something that took effect.
		if len(os.Args) > 2 {
			fmt.Fprintf(os.Stderr, "hdis: mcp takes no argument %q\n", os.Args[2])
			os.Exit(codes.Exit(codes.Usage))
		}
		if err := mcpdoor.Serve(context.Background(), version.Version, nil); err != nil {
			log.Fatal(err)
		}
	case "version":
		// The contract revision travels with the version, the way both
		// siblings print it: §13.4 makes a plugin declare it, and a reader
		// comparing three binaries should not have to run a different verb on
		// one of them to see it. `--json` answers the same three facts.
		if len(os.Args) > 2 && os.Args[2] == "--json" {
			out, _ := json.Marshal(map[string]string{
				"version": version.Version, "contract": version.Contract, "plugin": "herdr-dispatch",
			})
			fmt.Println(string(out))
			break
		}
		fmt.Printf("hdis %s (herdr-dispatch), shared plugin contract %s\n", version.Version, version.Contract)
	case "-h", "--help", "help":
		fmt.Print(cli.Usage())
	default:
		v, ok := verbs.ByCLI(os.Args[1:2])
		if !ok {
			fmt.Fprintf(os.Stderr, "hdis: unknown command %q\n\n%s", os.Args[1], cli.Usage())
			os.Exit(2)
		}
		if err := cli.Run(v, os.Args[2:], os.Stdout); err != nil {
			// One failure, one report, one stream (§6.2): with --json the
			// envelope IS the report and it goes to stdout, otherwise a
			// sentence goes to stderr and stdout stays empty. The status is
			// the one §6.3 fixes for the code, so a caller scripting three
			// sibling plugins reads the same number from each rather than
			// this binary's old "1 for everything".
			if cli.WantsJSON(os.Args[2:]) {
				cli.WriteError(err, os.Stdout)
			} else {
				fmt.Fprintf(os.Stderr, "hdis: %s\n", err)
			}
			os.Exit(codes.Exit(codes.Of(err)))
		}
	}
}

// serve is the daemon: it owns the tick and the bindings, and answers the
// socket both doors dial.
func serve(argv []string) error {
	fs := flag.NewFlagSet("hdis daemon", flag.ExitOnError)
	configPath := fs.String("config", config.ConfigPath(), "worker profiles and per-project overrides")
	logPath := fs.String("log", config.LogPath(), "the file the log is appended to; stdout keeps every line too")
	interval := fs.Duration("interval", 15*time.Second, "how often to tick")
	once := fs.Bool("once", false, "run one tick and exit")
	basePane := fs.String("pane", os.Getenv("HERDR_PANE_ID"), "the pane worker panes are split off")
	maxWorkers := fs.Int("max-workers", 0, `how many workers may be live at once; 0 means the config's "max_workers"`)
	claimTimeout := fs.Duration("claim-timeout", 5*time.Minute, "how long a delivered goal may go unclaimed before a nudge")
	maxPrompts := fs.Int("max-prompts", 3, "how many times one task's goal may be delivered before giving up")
	startTimeout := fs.Duration("start-timeout", 45*time.Second, "how long herdr waits for a worker to become interactive")
	confirmCeiling := fs.Duration("confirm-ceiling", spawn.DefaultConfirmCeiling, "how long to wait for a delivered goal to show on the worker's screen")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	// The log is opened here, before anything worth reading is said. Where
	// it goes is the daemon's own call for the same reason the socket, the
	// lock and the bindings are: a shell line that redirects elsewhere can
	// be lost on a restart, and it is only ever missed once the log is
	// already needed.
	if err := config.EnsureStateDir(); err != nil {
		return err
	}
	logOut, logFile, logErr := daemon.OpenLog(*logPath, os.Stdout)
	if logFile != nil {
		defer logFile.Close()
	}
	log.SetOutput(logOut)
	if logErr != nil {
		*logPath = ""
		log.Printf("%v; logging to stdout alone", logErr)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	pane := cfg.BasePaneOr(*basePane)

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
		log.Printf("htask %s (contract %s), ticking every %s", doc.Version, doc.Contract, *interval)
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
			`Run inside a Herdr pane, pass -pane, or set "pane" in the config.`)
	}

	l := &loop.Loop{
		Board:  board,
		Herdr:  pens,
		Config: cfg,
		Policy: decide.Policy{
			MaxWorkers:   cfg.MaxWorkersOr(*maxWorkers),
			ClaimTimeout: *claimTimeout,
			MaxPrompts:   *maxPrompts,
			// The lane is the config document's call, not a flag: which
			// agent verifies and whether one runs at all is execution
			// policy, and it lives where the profiles live.
			Verify: cfg.Verify.Enabled,
		},
		Spawn: &spawn.Pipeline{
			Herdr:          pens,
			Proxy:          &proxy.Client{Bin: cfg.Proxy},
			StartTimeout:   *startTimeout,
			DialogCeiling:  20 * time.Second,
			ConfirmCeiling: *confirmCeiling,
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

	if *once {
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
		Interval: *interval,
		Version:  version.Version,
		Log:      log.Default(),
		LogPath:  *logPath,
		Lock:     lock,
	}
	err = d.Serve(ctx, ln)
	log.Print("stopping")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
