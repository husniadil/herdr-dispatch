// Package cli is the CLI door: it turns an argv into the same request the MCP
// door builds, sends it to the daemon, and prints what comes back. It holds
// no state, and it decides nothing the daemon has not already decided.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/client"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// Door names this surface in the daemon's log.
const Door = "cli"

// Request builds the daemon request for one subcommand, and reports whether
// the caller asked for the answer as it came.
func Request(v verbs.Verb, argv []string) (protocol.Request, bool, error) {
	fs := flag.NewFlagSet("hdis "+strings.Join(v.CLI, " "), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print the daemon's answer as it came")
	if err := fs.Parse(argv); err != nil {
		return protocol.Request{}, false, codes.Errorf(codes.Invalid, "%s: %v", strings.Join(v.CLI, " "), err)
	}

	req := protocol.Request{
		Verb: v.Name,
		Args: map[string]any{},
		// The pane this door runs in, recorded by the daemon and granting
		// nothing. A caller on another harness has none, and needs none.
		Pane: os.Getenv("HERDR_PANE_ID"),
		Door: Door,
	}
	rest := fs.Args()
	for _, a := range v.Args {
		if !a.Positional {
			continue
		}
		if len(rest) == 0 {
			if a.Required {
				return protocol.Request{}, false, codes.Errorf(codes.Invalid,
					"%s needs <%s>", strings.Join(v.CLI, " "), a.Name)
			}
			continue
		}
		req.Args[a.Name], rest = rest[0], rest[1:]
	}
	if len(rest) > 0 {
		return protocol.Request{}, false, codes.Errorf(codes.Invalid,
			"%s takes no argument %q", strings.Join(v.CLI, " "), rest[0])
	}
	return req, *asJSON, nil
}

// Run sends one subcommand to the daemon and writes the answer.
func Run(v verbs.Verb, argv []string, out io.Writer) error {
	req, asJSON, err := Request(v, argv)
	if err != nil {
		return err
	}
	result, err := (&client.Client{NoStart: v.NoAutostart}).Call(req)
	if err != nil {
		return err
	}
	return Write(v.Name, result, asJSON, out)
}

// Write prints one answer: as it came when the caller asked for that, and as
// a line an operator reads otherwise. The JSON is the daemon's own bytes,
// which is the same document the MCP door hands its caller.
func Write(verb string, result json.RawMessage, asJSON bool, out io.Writer) error {
	if asJSON {
		_, err := fmt.Fprintln(out, string(result))
		return err
	}
	switch verb {
	case "doctor":
		var rep daemon.DoctorReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "hdis %s on %s\n", rep.Version, rep.Socket)
		fmt.Fprintf(out, "  contract    %s satisfied by this plugin\n", rep.Contract)
		fmt.Fprintf(out, "  base pane   %s\n", or(rep.BasePane, "none: nothing is spawned and dispatch refuses"))
		fmt.Fprintf(out, "  workers     %d live%s, %d reserved, max %d\n",
			rep.Workers, held(rep.AwaitingReview), rep.Pending, rep.MaxWorkers)
		fmt.Fprintf(out, "  tick        every %s\n", rep.Interval)
		fmt.Fprintf(out, "  bindings    %s, %d re-adopted at start\n",
			or(rep.Bindings, "in memory only"), rep.Readopted)
		fmt.Fprintf(out, "  log         %s\n", or(rep.Log, "stdout only: no file could be opened"))
		fmt.Fprintf(out, "  verify      %s\n", verifyLane(rep.Verify))
		fmt.Fprintf(out, "  layout      min_pane_columns %d, max_panes_per_tab %d per task\n",
			rep.MinPaneColumns, rep.MaxPanesPerTab)
		if rep.Board.Error != "" {
			fmt.Fprintf(out, "  board       unreachable: %s\n", rep.Board.Error)
			return nil
		}
		fmt.Fprintf(out, "  board       htask %s (contract %s), herdr reachable: %t\n",
			rep.Board.Version, rep.Board.Contract, rep.Board.HerdrReachable)
	case "dispatch":
		var res loop.Reservation
		if err := json.Unmarshal(result, &res); err != nil {
			return err
		}
		fmt.Fprintf(out, "task #%d %q is reserved; its worker comes up on the next tick\n", res.Seq, res.Title)
	case "stop":
		var rep daemon.StopReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		fmt.Fprintf(out, "the daemon on %s (pid %d) is stopping\n", rep.Socket, rep.PID)
	case "status":
		var st loop.Status
		if err := json.Unmarshal(result, &st); err != nil {
			return err
		}
		if len(st.Workers) == 0 && len(st.Pending) == 0 {
			fmt.Fprintf(out, "nothing is being driven (max %d workers, base pane %s)\n",
				st.MaxWorkers, or(st.BasePane, "none"))
			return nil
		}
		for _, w := range st.Workers {
			state := w.AgentStatus
			if !w.PaneAlive {
				state = "pane gone"
			}
			branch := or(w.Branch, "detached")
			if w.Behind {
				// Said beside the branch because that is where the operator
				// is already looking for the work, and the merge that will
				// refuse names the same branch.
				branch += " (behind)"
			}
			title := w.Title
			if w.AwaitingReview {
				title += "  (" + HeldPhrase + ")"
			}
			fmt.Fprintf(out, "#%-4d %-10s %-8s %-10s %-23s prompted %s x%d notified=%t  %s\n",
				w.Seq, w.Pane, or(w.Tab, "-"), or(state, "unknown"), branch,
				w.PromptedAt.Format(time.RFC3339), w.Prompts, w.Notified, title)
		}
		for _, id := range st.Pending {
			fmt.Fprintf(out, "%-5s %-10s reserved, not yet spawned\n", "", id)
		}
	default:
		_, err := fmt.Fprintln(out, string(result))
		return err
	}
	return nil
}

// HeldPhrase is what both status and doctor call a worker slot spent on a
// pane that has submitted and is waiting for a human. It is deliberately one
// phrase: an operator who sees nothing moving reads the same words wherever
// they look.
const HeldPhrase = "holding a slot while awaiting review"

// held names how many slots are spent on panes waiting for a human, and says
// nothing at all when none are.
func held(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d %s)", n, HeldPhrase)
}

// Usage is the help both the bare command and `hdis help` print, listed from
// the verb table so a new verb appears here without being written out.
func Usage() string {
	var b strings.Builder
	b.WriteString("hdis — the dispatcher for the htask board.\n\nUsage:\n")
	for _, v := range verbs.All {
		line := strings.Join(v.CLI, " ")
		for _, a := range v.Args {
			if a.Positional {
				line += " <" + a.Name + ">"
			}
		}
		fmt.Fprintf(&b, "  hdis %-18s %s\n", line, v.Short)
	}
	for _, extra := range [][2]string{
		{"daemon", "Own the tick and answer both doors (`run` is the same)"},
		{"mcp", "Serve the same verbs over stdio MCP"},
		{"version", "Print the version"},
	} {
		fmt.Fprintf(&b, "  hdis %-18s %s\n", extra[0], extra[1])
	}
	b.WriteString("\nEvery verb takes --json. Run `hdis daemon -h` for the dispatcher's knobs.\n")
	return b.String()
}

// verifyLane is the verification lane in a line: on says what a submission
// buys and where it lands, off says so rather than leaving it to be inferred.
func verifyLane(v daemon.VerifyHealth) string {
	if !v.Enabled {
		return "off: a submission earns no self-review shot"
	}
	return "on: a submission earns one self-review shot in the worker's own pane"
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
