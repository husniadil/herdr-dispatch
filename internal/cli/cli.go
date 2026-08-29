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
		// Who this very call is, as the daemon recorded it (§10.3). It is the
		// line §7.5 rests on: an operator reads it to see which of their
		// registrations speak for them.
		fmt.Fprintf(out, "  principal   %s\n", rep.Principal)
		fmt.Fprintf(out, "  state dir   %s\n", rep.StateDir)
		fmt.Fprintf(out, "  config dir  %s\n", rep.ConfigDir)
		fmt.Fprintf(out, "  base pane   %s\n", or(rep.BasePane, "none yet: one is adopted when a live pane can be, and until then nothing is spawned and dispatch refuses"))
		fmt.Fprintf(out, "  workers     %d live%s, %d reserved, max %d\n",
			rep.Workers, held(rep.AwaitingReview), rep.Pending, rep.MaxWorkers)
		fmt.Fprintf(out, "  tick        every %s\n", rep.Interval)
		fmt.Fprintf(out, "  bindings    %s, %d re-adopted at start\n",
			or(rep.Bindings, "in memory only"), rep.Readopted)
		fmt.Fprintf(out, "  log         %s\n", or(rep.Log, "stdout only: no file could be opened"))
		fmt.Fprintf(out, "  verify      %s\n", verifyLane(rep.Verify))
		fmt.Fprintf(out, "  gate        %s\n", gateLane(rep.Gate))
		fmt.Fprintf(out, "  herdr api   %s\n", herdrLane(rep.Herdr))
		fmt.Fprintf(out, "  proxy       %s\n", proxyLane(rep.Proxy))
		// Only where something launches through the proxy: a claude-only
		// fleet spends no account here, so there is no quota to report.
		if len(rep.Proxy.Profiles) > 0 {
			fmt.Fprintf(out, "  quota       %s\n", quotaLane(rep.Proxy.Quota))
		}
		// Only where there is something to say: a fleet whose profiles
		// name no account, or one whose names the store all holds, has
		// no finding and gets no line.
		if line := accountsLane(rep.Proxy); line != "" {
			fmt.Fprintf(out, "  accounts    %s\n", line)
		}
		// One line per profile that names a fallback, and none at all for a
		// fleet whose profiles name none. A chain is invisible otherwise:
		// an operator whose fleet is quietly running everything through the
		// second profile in one has no other way to see the first one's
		// account is spent.
		for _, chain := range rep.Profiles {
			fmt.Fprintf(out, "  profiles    %s\n", profilesLane(chain))
		}
		// The same rule, for the same reason: a fleet that has lost no
		// worker has nothing here. When it has, this is the only line
		// that says why a ready task is sitting still.
		if line := diedLane(rep.WorkersDied); line != "" {
			fmt.Fprintf(out, "  died        %s\n", line)
		}
		fmt.Fprintf(out, "  layout      min_pane_columns %d, max_panes_per_tab %d per task\n",
			rep.MinPaneColumns, rep.MaxPanesPerTab)
		// Every worker is launched with --strict-mcp-config against this
		// document, so it is the whole of the doors it has. A configured
		// path that is not there refuses the spawn, which is what makes it
		// a doctor line rather than a config echo.
		fmt.Fprintf(out, "  worker      %s\n", workerLane(rep.Worker))
		// The names a worker's pane carries beyond what this dispatcher
		// sets, and never their values: what an operator exports there is
		// theirs, and a gate command or a token read out loud is a doctor
		// line that costs something.
		fmt.Fprintf(out, "  worker env  %s\n", workerEnvLane(rep.Worker))
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
			if w.Deaths > 0 {
				// Said on the row because it is a fact about the task
				// this worker is the next attempt at, and an operator
				// reading a second worker on the same task is owed the
				// first one's ending.
				title += fmt.Sprintf("  (%d worker agent(s) died here before)", w.Deaths)
			}
			if w.AskedProfile != "" {
				// Said on the row for the same reason: the profile column
				// names what is really running in that pane, and an
				// operator who configured the other one is owed the fact
				// that a quota moved it.
				title += fmt.Sprintf("  (asked for %s, which was at quota)", w.AskedProfile)
			}
			fmt.Fprintf(out, "#%-4d %-10s %-8s %-10s %-10s %-23s prompted %s x%d notified=%t  %s\n",
				w.Seq, w.Pane, or(w.Tab, "-"), or(state, "unknown"), or(w.Profile, "-"), branch,
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
	case "parked.list":
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
	case "parked.resolve":
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

// workerLane is the worker's doors in one line: which MCP document a worker
// is launched against, whether it is there, and any profile that names one of
// its own.
//
// A path that is not there says what it costs. Configured and missing is a
// spawn refused; unconfigured and missing is the ordinary state of a
// dispatcher that has not spawned yet, because the default file is written at
// the first spawn that needs it.
func workerLane(w daemon.WorkerHealth) string {
	line := "mcp_config " + w.MCPConfig
	switch {
	case w.MCPExists:
		line += ", present"
	case w.MCPConfigured:
		line += ", MISSING: a spawn against it is refused until it is written"
	default:
		line += ", not written yet: the default doors are written at the first spawn"
	}
	for _, p := range w.Profiles {
		state := "present"
		if !p.Exists {
			state = "MISSING"
		}
		line += fmt.Sprintf("; profile %s reads %s, %s", p.Profile, p.MCPConfig, state)
	}
	return line
}

// workerEnvLane is what a worker's pane is given beyond what this dispatcher
// sets: the fleet-wide names first, then every profile that adds names of its
// own. KEYS only — the values are the operator's, and a gate command or a
// token belongs in their document rather than on a line doctor prints.
//
// A fleet that exports nothing says so rather than printing a blank: on a
// laptop, nothing exported is exactly the finding — a worker's own doors read
// their policy gate from their environment, and an empty one is an ungated
// door.
func workerEnvLane(w daemon.WorkerHealth) string {
	line := "none: a worker's pane carries only what this dispatcher sets"
	if len(w.Env) > 0 {
		line = strings.Join(w.Env, " ")
	}
	for _, p := range w.ProfileEnv {
		line += fmt.Sprintf("; profile %s adds %s", p.Profile, strings.Join(p.Env, " "))
	}
	return line
}

// gateLane is the §9 policy gate in one line: whether one is configured, and
// what an operator has waiting because of it. An unconfigured gate allows
// every verb, which is what the line has to say rather than leave blank.
//
// The parked figure is the board the call named, so on a project it can be
// smaller than what the daemon holds. Both are printed when they differ: the
// first is what `hdis parked list` here will hand over, the second is the
// daemon's own, and one daemon serves every board. A board with nothing and a
// daemon with something is that same difference, and the line says it rather
// than falling silent on a zero.
func gateLane(g daemon.GateHealth) string {
	line := "not configured: every verb is allowed (§9.2)"
	if g.Configured {
		line = strings.Join(g.Command, " ")
	}
	line += ", gating " + strings.Join(g.Verbs, " ")
	switch {
	case g.Parked > 0:
		line += fmt.Sprintf(", %d parked for you (hdis parked list)", g.Parked)
		if g.ParkedEverywhere > g.Parked {
			line += fmt.Sprintf(", %d across every board", g.ParkedEverywhere)
		}
	case g.ParkedEverywhere > 0:
		line += fmt.Sprintf(", none parked on this board, %d across every board", g.ParkedEverywhere)
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

// proxyLane is the codex provider's launcher in one line. Its state alone is
// not the answer an operator needs: the same down proxy is step zero of a
// codex spawn failing and nothing at all where every profile is a claude one,
// so the line says which of the two this dispatcher is in.
func proxyLane(p daemon.ProxyHealth) string {
	bin := or(p.Binary, "proxenos")
	var line string
	switch {
	case p.Reachable:
		line = bin + " reachable, active account " + or(p.Account, "none selected")
	case p.Installed:
		line = bin + " is down: " + or(p.Error, "it gave no answer and no reason")
	default:
		line = bin + " is not installed: " + or(p.Error, "it could not be resolved on PATH")
	}
	if len(p.Profiles) == 0 {
		return line + ", and no configured profile launches through it: the claude path never touches it"
	}
	return line + ", launching " + strings.Join(p.Profiles, " ")
}

// accountsLane is the per-profile account check as one line: the profiles
// whose configured account the launcher does not hold, or why nothing could
// be checked. Empty means there is nothing to report, and the line is left
// out rather than printed blank.
//
// It is a finding and not an outage: the fleet is running, and every profile
// naming an account the store does hold launches exactly as it did.
func accountsLane(p daemon.ProxyHealth) string {
	if p.AccountsError != "" {
		return "not checked: " + p.AccountsError
	}
	if len(p.MissingAccounts) == 0 {
		return ""
	}
	named := make([]string, 0, len(p.MissingAccounts))
	for _, f := range p.MissingAccounts {
		named = append(named, fmt.Sprintf("%s wants %q", f.Profile, f.Account))
	}
	return strings.Join(named, ", ") +
		", which " + or(p.Binary, "proxenos") + " does not hold: its worker is refused at launch and again at the turn"
}

// profilesLane is one fallback chain as one line: what the document says,
// what following it works out to, and which step a spawn asked for the head
// would launch through right now.
//
// The chain's quota state is on it because the answer an operator wants is
// not "there is a fallback" but "which profile is my fleet really running
// on, and why". Where the proxy could not be asked, no step is refused and
// the line reads as the document alone — an unread quota gates nothing.
func profilesLane(h daemon.ProfileHealth) string {
	steps := make([]string, 0, len(h.Chain))
	for _, s := range h.Chain {
		steps = append(steps, profileStep(s))
	}
	line := fmt.Sprintf("%s fallback -> %s: %s", h.Profile, h.Fallback, strings.Join(steps, " -> "))
	switch {
	case h.Launches == "":
		return line + ", and no step of it can launch: a spawn asked for " +
			h.Profile + " is refused AT_QUOTA naming every one"
	case h.Launches == h.Profile:
		return line + ", launching " + h.Launches
	default:
		return line + ", launching " + h.Launches + " because " + h.Profile + " is at quota"
	}
}

// profileStep is one profile of a chain and what its own quota says. A
// profile that spends no proxy quota says that rather than a figure, because
// there is none to read and it is eligible whatever the proxy reports.
func profileStep(s daemon.ProfileStep) string {
	if !s.Gated {
		return s.Profile + " (spends no proxy quota, always eligible)"
	}
	who := or(s.Account, "the account the proxy serves")
	if s.Refusal != "" {
		return fmt.Sprintf("%s (%s) AT QUOTA: %s", s.Profile, who, s.Refusal)
	}
	return fmt.Sprintf("%s (%s) clear", s.Profile, who)
}

// diedLane is the tasks nothing will dispatch again, as one line: the number
// an operator reads, the count that held it back, and what ends the hold.
// Empty when nothing is held back, so the line is absent rather than
// reassuring.
func diedLane(held []loop.DeadTask) string {
	if len(held) == 0 {
		return ""
	}
	named := make([]string, 0, len(held))
	for _, t := range held {
		name := t.TaskID
		if t.Seq > 0 {
			name = fmt.Sprintf("#%d", t.Seq)
		}
		named = append(named, fmt.Sprintf("%s has lost %d worker agent(s)", name, t.Deaths))
	}
	return strings.Join(named, ", ") +
		": no more are spent on them until somebody releases or amends the row"
}

// quotaLane is the proxy's quota as one line: what the account has spent
// against the threshold, or the refusal a codex spawn would meet right now.
// An operator whose fleet has stopped reads the reason here rather than by
// trying a dispatch and reading the message off the failure.
func quotaLane(q daemon.QuotaHealth) string {
	if !q.Known {
		return "no quota to read for " + or(q.Account, "the account the proxy serves") +
			": a metered key has no ceiling and an unreachable proxy leaves none read, and nothing is gated on it"
	}
	spent := fmt.Sprintf("%s at %s%% of its window",
		or(q.Account, "the account the proxy serves"), strconv.FormatFloat(q.UsedPercent, 'g', -1, 64))
	if q.Plan != "" {
		spent += " on " + q.Plan
	}
	if q.MaxUsedPercent > 0 {
		spent += fmt.Sprintf(", max_used_percent %d", q.MaxUsedPercent)
	} else {
		spent += ", no max_used_percent set"
	}
	if q.Refusal != "" {
		return spent + ": no codex worker is spawned, because " + q.Refusal
	}
	return spent
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
