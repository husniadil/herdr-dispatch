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
	"strings"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/client"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// Version is the dispatcher's own version.
const Version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("hdis: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}
	switch os.Args[1] {
	case "daemon", "run":
		if err := serve(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "version":
		fmt.Println(Version)
	case "-h", "--help", "help":
		fmt.Print(usage())
	default:
		v, ok := verbs.ByCLI(os.Args[1:2])
		if !ok {
			fmt.Fprintf(os.Stderr, "hdis: unknown command %q\n\n%s", os.Args[1], usage())
			os.Exit(2)
		}
		if err := ask(v, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "hdis: %s\n", err)
			os.Exit(1)
		}
	}
}

func usage() string {
	var b strings.Builder
	b.WriteString("hdis — the dispatcher for the htask board.\n\nUsage:\n")
	for _, v := range verbs.All {
		line := "  hdis " + strings.Join(v.CLI, " ")
		for _, a := range v.Args {
			if a.Positional {
				line += " <" + a.Name + ">"
			}
		}
		b.WriteString(fmt.Sprintf("  %-22s %s\n", strings.TrimSpace(line), v.Short))
	}
	b.WriteString("  daemon                 own the tick and answer both doors (`run` is the same)\n")
	b.WriteString("  version                print the version\n")
	b.WriteString("\nRun `hdis daemon -h` for the dispatcher's knobs.\n")
	return b.String()
}

// ask is the CLI door: build the same request the MCP door builds, send it to
// the daemon, and print what comes back.
func ask(v verbs.Verb, argv []string) error {
	fs := flag.NewFlagSet("hdis "+strings.Join(v.CLI, " "), flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the daemon's answer as it came")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	req := protocol.Request{
		Verb: v.Name,
		Args: map[string]any{},
		Pane: os.Getenv("HERDR_PANE_ID"),
		Door: "cli",
	}
	rest := fs.Args()
	for _, a := range v.Args {
		if !a.Positional {
			continue
		}
		if len(rest) == 0 {
			if a.Required {
				return codes.Errorf(codes.Invalid, "%s needs <%s>", strings.Join(v.CLI, " "), a.Name)
			}
			continue
		}
		req.Args[a.Name], rest = rest[0], rest[1:]
	}
	if len(rest) > 0 {
		return codes.Errorf(codes.Invalid, "%s takes no argument %q", strings.Join(v.CLI, " "), rest[0])
	}

	c := &client.Client{}
	result, err := c.Call(req)
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(result))
		return nil
	}
	return render(v.Name, result)
}

func render(verb string, result json.RawMessage) error {
	switch verb {
	case "doctor":
		var rep daemon.DoctorReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Printf("hdis %s on %s\n", rep.Version, rep.Socket)
		fmt.Printf("  base pane   %s\n", orNone(rep.BasePane, "none: dispatch will refuse"))
		fmt.Printf("  workers     %d live, %d reserved, max %d\n", rep.Workers, rep.Pending, rep.MaxWorkers)
		fmt.Printf("  tick        every %s\n", rep.Interval)
		if rep.Board.Error != "" {
			fmt.Printf("  board       unreachable: %s\n", rep.Board.Error)
			return nil
		}
		fmt.Printf("  board       htask %s (contract %s), herdr reachable: %t\n",
			rep.Board.Version, rep.Board.Contract, rep.Board.HerdrReachable)
	case "dispatch":
		var res loop.Reservation
		if err := json.Unmarshal(result, &res); err != nil {
			return err
		}
		fmt.Printf("task #%d %q is reserved; its worker comes up on the next tick\n", res.Seq, res.Title)
	case "status":
		var st loop.Status
		if err := json.Unmarshal(result, &st); err != nil {
			return err
		}
		if len(st.Workers) == 0 && len(st.Pending) == 0 {
			fmt.Printf("nothing is being driven (max %d workers, base pane %s)\n",
				st.MaxWorkers, orNone(st.BasePane, "none"))
			return nil
		}
		for _, w := range st.Workers {
			state := w.AgentStatus
			if !w.PaneAlive {
				state = "pane gone"
			}
			fmt.Printf("#%-4d %-10s %-9s %s prompts=%d notified=%t %s\n",
				w.Seq, w.Pane, state, w.PromptedAt.Format(time.RFC3339), w.Prompts, w.Notified, w.Title)
		}
		for _, id := range st.Pending {
			fmt.Printf("%-5s %-10s reserved, not yet spawned\n", "", id)
		}
	}
	return nil
}

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
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
	pane := *basePane
	if pane == "" {
		pane = cfg.Pane
	}

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
		Version:  Version,
		Log:      log.Default(),
	}
	err = d.Serve(ctx, ln)
	log.Print("stopping")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
