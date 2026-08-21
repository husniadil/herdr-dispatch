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
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/mcpdoor"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
	"github.com/husniadil/herdr-dispatch/internal/version"
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
		if err := mcpdoor.Serve(context.Background(), version.Version, nil); err != nil {
			log.Fatal(err)
		}
	case "version":
		fmt.Println(version.Version)
	case "-h", "--help", "help":
		fmt.Print(cli.Usage())
	default:
		v, ok := verbs.ByCLI(os.Args[1:2])
		if !ok {
			fmt.Fprintf(os.Stderr, "hdis: unknown command %q\n\n%s", os.Args[1], cli.Usage())
			os.Exit(2)
		}
		if err := cli.Run(v, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "hdis: %s\n", err)
			os.Exit(1)
		}
	}
}

// serve is the daemon: it owns the tick and the bindings, and answers the
// socket both doors dial.
func serve(argv []string) error {
	fs := flag.NewFlagSet("hdis daemon", flag.ExitOnError)
	configPath := fs.String("config", config.ConfigPath(), "worker profiles and per-project overrides")
	interval := fs.Duration("interval", 15*time.Second, "how often to tick")
	once := fs.Bool("once", false, "run one tick and exit")
	basePane := fs.String("pane", os.Getenv("HERDR_PANE_ID"), "the pane worker panes are split off")
	maxWorkers := fs.Int("max-workers", 2, "how many workers may be live at once")
	claimTimeout := fs.Duration("claim-timeout", 5*time.Minute, "how long a delivered goal may go unclaimed before a nudge")
	maxPrompts := fs.Int("max-prompts", 3, "how many times one task's goal may be delivered before giving up")
	startTimeout := fs.Duration("start-timeout", 45*time.Second, "how long herdr waits for a worker to become interactive")
	confirmCeiling := fs.Duration("confirm-ceiling", spawn.DefaultConfirmCeiling, "how long to wait for a delivered goal to show on the worker's screen")
	if err := fs.Parse(argv); err != nil {
		return err
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

	board := &htask.Client{}
	pens := &herdr.Client{}

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
	if pane == "" {
		log.Print(`no base pane: nothing will be spawned and dispatch will refuse. ` +
			`Run inside a Herdr pane, pass -pane, or set "pane" in the config.`)
	}

	l := &loop.Loop{
		Board:  board,
		Herdr:  pens,
		Config: cfg,
		Policy: decide.Policy{
			MaxWorkers:   *maxWorkers,
			ClaimTimeout: *claimTimeout,
			MaxPrompts:   *maxPrompts,
		},
		Spawn: &spawn.Pipeline{
			Herdr:          pens,
			Proxy:          &proxy.Client{Bin: cfg.Proxy},
			StartTimeout:   *startTimeout,
			DialogCeiling:  20 * time.Second,
			ConfirmCeiling: *confirmCeiling,
			ShellCeiling:   spawn.DefaultShellCeiling,
			Poll:           2 * time.Second,
		},
		BasePane: pane,
		Log:      log.Default(),
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
	}
	err = d.Serve(ctx, ln)
	log.Print("stopping")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
