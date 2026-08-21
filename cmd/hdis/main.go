// Command hdis is the dispatcher: it watches the htask board for ready work,
// brings a worker pane up in Herdr for each ready task, delivers the task's
// goal, tracks the worker, and stops at review.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
)

// Version is the dispatcher's own version.
const Version = "0.1.0"

const usage = `hdis — the dispatcher for the htask board.

Usage:
  hdis run [flags]   watch the board and drive workers
  hdis version       print the version

Run ` + "`hdis run -h`" + ` for the flags.
`

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("hdis: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		if err := run(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "version":
		fmt.Println(Version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "hdis: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func run(argv []string) error {
	fs := flag.NewFlagSet("hdis run", flag.ExitOnError)
	configPath := fs.String("config", defaultConfigPath(), "worker profiles and per-project overrides")
	interval := fs.Duration("interval", 15*time.Second, "how often to tick")
	once := fs.Bool("once", false, "run one tick and exit")
	basePane := fs.String("pane", os.Getenv("HERDR_PANE_ID"), "the pane worker panes are split off")
	maxWorkers := fs.Int("max-workers", 2, "how many workers may be live at once")
	claimTimeout := fs.Duration("claim-timeout", 5*time.Minute, "how long a delivered goal may go unclaimed before a nudge")
	maxPrompts := fs.Int("max-prompts", 3, "how many times one task's goal may be delivered before giving up")
	startTimeout := fs.Duration("start-timeout", 45*time.Second, "how long herdr waits for a worker to become interactive")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	if *basePane == "" {
		return errors.New("no pane to split workers off: run inside a Herdr pane, or pass -pane")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	board := &htask.Client{}
	pens := &herdr.Client{}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Discovery: say what is unreachable now rather than failing one task at
	// a time later.
	doctor, err := board.Doctor(ctx)
	if err != nil {
		return err
	}
	if !doctor.SocketLive {
		return fmt.Errorf("the board's daemon is not answering on %s", doctor.Binary)
	}
	if !doctor.HerdrReachable {
		return errors.New("the board cannot reach Herdr, so neither can the dispatcher")
	}
	log.Printf("htask %s (contract %s), ticking every %s from pane %s", doctor.Version, doctor.Contract, *interval, *basePane)

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
			Proxy:          &proxy.Client{},
			StartTimeout:   *startTimeout,
			DialogCeiling:  20 * time.Second,
			ConfirmCeiling: 60 * time.Second,
			ShellCeiling:   30 * time.Second,
			Poll:           2 * time.Second,
		},
		BasePane: *basePane,
		Log:      log.Default(),
	}

	if *once {
		return l.Tick(ctx)
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		// A tick that fails is reported and the next one still runs: the
		// board being unreachable for a moment is not a reason to stop
		// dispatching for good.
		if err := l.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Print(err)
		}
		select {
		case <-ctx.Done():
			log.Print("stopping")
			return nil
		case <-ticker.C:
		}
	}
}

func defaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "hdis.json"
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hdis", "hdis.json")
}
