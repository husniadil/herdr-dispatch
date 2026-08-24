// Package cli is the CLI door: it turns an argv into the same request the MCP
// door builds, sends it to the daemon, and prints what comes back. It holds
// no state, and it decides nothing the daemon has not already decided.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/client"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// Door names this surface in the daemon's log.
const Door = "cli"

// WantsJSON reads --json out of a raw argv, wherever in it the caller wrote
// the flag. It has to be known BEFORE cobra parses, because cobra's own parse
// failures are among the failures §6.2 makes answer with one document, and at
// that moment the flag exists only in argv. A value that is not an explicit
// false counts as asking for a document: cobra will refuse a bad value
// itself, and a machine caller that asked for JSON should be told in JSON.
func WantsJSON(argv []string) bool {
	on := false
	for _, a := range argv {
		switch {
		case a == "--":
			return on
		case a == "--json":
			on = true
		case strings.HasPrefix(a, "--json="):
			_, v, _ := strings.Cut(a, "=")
			b, err := strconv.ParseBool(v)
			on = err != nil || b
		}
	}
	return on
}

// WriteError prints the §6.2 failure document: with --json, one envelope on
// stdout carrying the contract code and the message; otherwise nothing here,
// because a human reads the sentence on stderr instead. It is the same
// document the MCP door builds for the same failure.
func WriteError(err error, out io.Writer) error {
	envelope := map[string]string{"code": string(codes.Of(err)), "message": message(err)}
	// §9.3: a DENIED the gate deferred names the row the operator resolves.
	if id := codes.ParkedOf(err); id != "" {
		envelope["parked_id"] = id
	}
	body, merr := json.Marshal(map[string]any{"error": envelope})
	if merr != nil {
		return merr
	}
	_, werr := fmt.Fprintln(out, string(body))
	return werr
}

// message is the failure without the code repeated in front of it: the
// envelope already carries the code in its own field.
func message(err error) string {
	var named *codes.Error
	if errors.As(err, &named) {
		return named.Message
	}
	return err.Error()
}

// Send performs one parsed call: it asks the daemon and writes the answer. It
// is the Runner the binary hands Root, and the only place in this door that
// opens the socket.
func Send(c Call) error {
	cl := &client.Client{NoStart: c.Verb.NoAutostart}
	if c.Req.Follow {
		// A stream has no single answer to print, so each event is written
		// as it arrives and the call returns when the daemon says the
		// stream is over or the caller goes away.
		return cl.Stream(c.Req, func(raw json.RawMessage) error {
			return WriteEvent(raw, c.AsJSON, os.Stdout)
		})
	}
	result, err := cl.Call(c.Req)
	if err != nil {
		return err
	}
	return Write(c.Verb.Name, result, c.AsJSON, os.Stdout)
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
		fmt.Fprintf(out, "  state dir   %s\n", rep.StateDir)
		fmt.Fprintf(out, "  config dir  %s\n", rep.ConfigDir)
		fmt.Fprintf(out, "  base pane   %s\n", or(rep.BasePane, "none: nothing is spawned and dispatch refuses"))
		fmt.Fprintf(out, "  workers     %d live%s, %d reserved, max %d\n",
			rep.Workers, held(rep.AwaitingReview), rep.Pending, rep.MaxWorkers)
		fmt.Fprintf(out, "  tick        every %s\n", rep.Interval)
		fmt.Fprintf(out, "  bindings    %s, %d re-adopted at start\n",
			or(rep.Bindings, "in memory only"), rep.Readopted)
		fmt.Fprintf(out, "  log         %s\n", or(rep.Log, "stdout only: no file could be opened"))
		fmt.Fprintf(out, "  verify      %s\n", verifyLane(rep.Verify))
		fmt.Fprintf(out, "  gate        %s\n", gateLane(rep.Gate))
		fmt.Fprintf(out, "  herdr api   %s\n", herdrLane(rep.Herdr))
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
	case "events":
		var rep daemon.EventsReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		if rep.Count == 0 {
			fmt.Fprintln(out, "the trail holds nothing for that read")
			return nil
		}
		for _, ev := range rep.Events {
			if err := WriteEvent(mustJSON(ev), false, out); err != nil {
				return err
			}
		}
	case "parked_list":
		var rep daemon.ParkedReport
		if err := json.Unmarshal(result, &rep); err != nil {
			return err
		}
		if rep.Count == 0 {
			fmt.Fprintln(out, "the policy gate has parked nothing")
			return nil
		}
		for _, p := range rep.Parked {
			fmt.Fprintf(out, "%s  %-18s %-10s %-8s %s\n",
				p.ID, p.Verb, or(p.Target, "-"), p.State, or(p.Reason, "no reason given"))
			if p.Error != "" {
				// A resolved action whose verb failed is not a finished
				// one, and the operator has to see which it was.
				fmt.Fprintf(out, "%s  and the verb did not run: %s\n", strings.Repeat(" ", len(p.ID)), p.Error)
			}
		}
	case "parked_resolve":
		var res daemon.ParkedResolution
		if err := json.Unmarshal(result, &res); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", res.ID, res.State)
		if len(res.Result) > 0 {
			fmt.Fprintln(out, string(res.Result))
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

// verifyLane is the verification lane in a line: on says what a submission
// buys and where it lands, off says so rather than leaving it to be inferred.
func verifyLane(v daemon.VerifyHealth) string {
	if !v.Enabled {
		return "off: a submission earns no self-review shot"
	}
	return "on: a submission earns one self-review shot in the worker's own pane"
}

// gateLane is the §9 policy gate in one line: whether one is configured, and
// what an operator has waiting because of it. An unconfigured gate allows
// every verb, which is what the line has to say rather than leave blank.
func gateLane(g daemon.GateHealth) string {
	line := "not configured: every verb is allowed (§9.2)"
	if g.Configured {
		line = strings.Join(g.Command, " ")
	}
	line += ", gating " + strings.Join(g.Verbs, " ")
	if g.Parked > 0 {
		line += fmt.Sprintf(", %d parked for you (hdis parked list)", g.Parked)
	}
	return line
}

// herdrLane is §11.2's feature detection in one line. A capability Herdr does
// not offer is what an operator needs BEFORE a verb refuses with UNSUPPORTED,
// which is the whole reason §10.3 puts the schema in doctor.
func herdrLane(h daemon.HerdrHealth) string {
	if !h.Detected {
		return "not read: " + or(h.Error, "nothing has asked herdr what it offers yet")
	}
	line := fmt.Sprintf("protocol %d, %d requests, %d events", h.Protocol, h.Requests, h.Events)
	if len(h.Missing) > 0 {
		return line + ", MISSING " + strings.Join(h.Missing, " ") + ": the verbs that need one refuse UNSUPPORTED"
	}
	return line + ", every capability this binary needs"
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// WriteEvent prints one event: as the daemon's own bytes when the caller
// asked for JSON, and as a line an operator reads otherwise.
func WriteEvent(raw json.RawMessage, asJSON bool, out io.Writer) error {
	if asJSON {
		_, err := fmt.Fprintln(out, string(raw))
		return err
	}
	var ev store.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}
	line := fmt.Sprintf("%s  %-34s %-26s %s",
		time.UnixMilli(ev.AtMS).UTC().Format(time.RFC3339), ev.Name, ev.EntityID, ev.ID)
	if detail := detailLine(ev.Detail); detail != "" {
		line += "  " + detail
	}
	_, err := fmt.Fprintln(out, line)
	return err
}

// detailLine is the event's own fields, in a stable order, so two events of
// the same kind print the same way.
func detailLine(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	keys := make([]string, 0, len(detail))
	for k := range detail {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, detail[k]))
	}
	return strings.Join(parts, " ")
}

// mustJSON re-renders one event for the shared renderer. It cannot fail on a
// document the daemon already encoded once, and an empty one renders as an
// event with no fields rather than taking the whole read down.
func mustJSON(ev store.Event) json.RawMessage {
	raw, err := json.Marshal(ev)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
