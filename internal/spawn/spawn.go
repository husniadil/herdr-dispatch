// Package spawn is the pipeline behind one Spawn action: a worker pane comes
// up, its agent starts with the task's goal already in its argv, and the
// pipeline confirms the goal took before the dispatcher records a binding.
//
// Five measured facts shape it, and every one of them is a fact to re-check
// rather than a constant to trust forever:
//
//   - herdr refuses an agent argument containing a newline, outright, before
//     anything starts. Everything that travels in the argv is one line.
//   - `agent start`'s return is inverted for a goal delivered this way. A
//     goal that registers drives the worker past interactive readiness, so
//     the command times out; a goal that is refused leaves the worker idle,
//     so the command succeeds. Delivery is confirmed from the pane, never
//     from that exit.
//   - a fresh directory raises Claude Code's trust-folder dialog, which
//     blocks startup. It is answered when it is seen and never blind.
//   - `pane run` returns when the line is typed, not when it finishes. On
//     the codex path that leaves the environment eval still owning the
//     shell, and `agent start` into a busy shell is refused outright.
//   - the launch line is TYPED into the pane, so its length is a risk, and
//     nothing long is allowed on it. The settings document leaves for a file
//     the worker reads, and the condition is a pointer to the board row
//     rather than the row's own text: see PointerGoal and TypedLineBudget.
package spawn

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
)

// Kind is the agent kind every worker starts as. Both providers run the same
// client; the codex provider only changes how it is routed.
const Kind = "claude"

// GoalPrefix turns a condition into the slash command that arms it.
const GoalPrefix = "/goal "

// DispatcherPaneVar is the environment variable every pane this pipeline
// splits carries: the pane of the daemon that spawned it, so a worker has an
// address to answer at without one being typed into its condition.
//
// It is an address and nothing more. Publishing it lets a worker send to the
// dispatcher; it never says who the worker is. The sender on anything the
// worker then writes is stamped by the mail daemon from HERDR_PANE_ID, which
// is Herdr's own word about the pane, and never from this variable.
const DispatcherPaneVar = "HDIS_DISPATCHER_PANE"

// ShortPromptCacheVar asks the client for the 5-minute prompt-cache TTL
// instead of the 1-hour one a REPL main thread would otherwise take. A
// worker is short-lived and disposable and rarely revisits its own prefix,
// so the long entry costs more than the work can spend; a native subagent
// already gets the short one for the same reason. The client reads this
// first and it short-circuits every other rule, so setting it here settles
// the question for a worker pane and for nothing else — the operator's own
// session never sees it, which is why it is set on the split rather than in
// the launcher's environment.
//
// It is inert on the codex path: a relayed cache_control has no equivalent
// upstream there and is not forwarded, so the variable only changes what a
// worker talking to the real Anthropic endpoint writes. Harmless otherwise.
const ShortPromptCacheVar = "FORCE_PROMPT_CACHING_5M"

// TabLabelPrefix is what every tab this dispatcher opens is labelled with,
// and it is the whole of the ownership evidence a restart still has.
//
// Herdr's agent name carries the same prefix, and until now it was the only
// evidence there was. It is not enough: an agent name is Herdr's record of a
// registration, and a pane whose agent Herdr has dropped — a client that
// exited and left the shell, a registration that never landed — is a pane
// with no name at all, while the pane and its work are still there. A tab
// label is written at CREATE time and belongs to the tab rather than to any
// process inside it, so it survives exactly the case the name does not.
//
// It is also the close guard. A tab without this prefix is a tab the
// operator made, and nothing here closes one — the same shape as the reap's
// root-and-prefix bound on a worktree directory.
// A tab's label is written for two readers at once, and both matter. An
// operator reads it to find the work — `hdis task 41` is something a person
// can pick out of a row of tabs, which is the one thing a tab has that a pane
// does not. This dispatcher reads it to know the tab is its own.
const TabLabelPrefix = "hdis "

// TabLabel is what a worker's tab is called.
func TabLabel(seq int) string { return fmt.Sprintf("%stask %d", TabLabelPrefix, seq) }

// OwnTab reports whether a tab label is one this dispatcher wrote. It is the
// only thing that earns a `tab close`.
func OwnTab(label string) bool { return strings.HasPrefix(label, TabLabelPrefix) }

// SettingsFileMode is what a spawn's settings file is created with. The
// document carries the proxy's auth token and the base URL of the daemon
// that answers with the operator's own quota, and the file lands in a
// directory the whole machine can list.
const SettingsFileMode = 0o600

// PointerGoal composes the /goal condition a worker boots with: a pointer to
// the board row, never the row's own text.
//
// The board's rendered goal document runs past a thousand characters, and it
// used to travel on this line whole. It cannot. The line is TYPED into the
// pane, character by character, and a long one intermittently arrives broken:
// two live codex runs proved it, one at roughly 2.2k characters and one at
// roughly 1.4k, both with the condition cut mid-word and the command's own
// start typed over what followed, the pane showing
//
//	htask task submit claude --settings '{"disabl
//
// while throwaway probes typed into a bare shell stayed clean to a megabyte.
// The fix is to stop typing long lines, not to trim the condition harder.
//
// So the pointer carries only what a worker cannot get anywhere else: how to
// take the task, where to read what it asks for, and the end state. The
// criteria stay on the board, where `htask task get` reads them whole and no
// shell ever types them, and the end state is named so /goal can still judge
// the transcript and finish.
func PointerGoal(seq int) string {
	return fmt.Sprintf("task %d is submitted for review: claim it with htask task claim %d, "+
		"read its full criteria with htask task get %d, do the work, then run "+
		"htask task submit %d with a report and evidence. "+
		"Reach the dispatcher at $"+DispatcherPaneVar+".", seq, seq, seq, seq)
}

// VerifierGoal composes the /goal condition a VERIFIER boots with. It is a
// pointer for the same reason a worker's is: the line is typed into the pane,
// and nothing long survives it.
//
// What it points at is the whole of the verifier's job — reread what was
// submitted, run the project's gate with nothing cached, check the report
// against the code, prove the gate can still fail, and send what it found.
// The last sentence is the boundary this binary does not cross: verification
// is delegated to a worker, judgment is not delegated at all.
//
// NOTHING it names of ours is a command to be found on PATH. A verifier's pane
// is opened in a detached worktree under the state directory, so a plugin
// binary that lives in the project's own bin/ is not on its PATH there: the
// first live verifier hit exactly that and reported through the mail MCP door
// instead, and the fallback the condition carried at the time, `htask note
// add`, was a second binary with the same problem. An MCP door is configured
// for the agent rather than resolved from the working directory, so it is the
// route that survives the worktree, so the board READ goes through
// mcp__herdr-tasks__get for the same reason the report routes do: htask
// happening to be go-installed on one machine is not something the lane
// guarantees. The one command the condition still names as a command is the
// project's own gate, which is the project's toolchain rather than one of our
// plugins. The fallback is the board's note tool
// because there is no task-scoped note verb: htask carries `task release`,
// which takes a note and hands the task back, and a separate `note` group for
// the board. A verifier holds nothing to hand back, so the board note fits.
//
// Rendered with a codex settings path and a profile of --agent claude --model
// sonnet --effort high, the whole typed line is 503 of the 512 budgeted for a
// two-digit task; TestTheVerifierLineFitsTheTypedBudget holds it there.
func VerifierGoal(seq int) string {
	return fmt.Sprintf("verify task %d: read its report: mcp__herdr-tasks__get, run "+
		"go clean -testcache then the gate CLAUDE.md names, check two claims against the "+
		"code, make one COMPILING mutation, show it caught, send findings with "+
		"the mail MCP send to $"+DispatcherPaneVar+", else mcp__herdr-tasks__note_add. "+
		"Never run task approve or task reject: you report, the operator judges.", seq)
}

// TypedLineBudget bounds the whole line herdr types into a worker's pane.
//
// Everything on that line is now hdis's own choice, so the whole line is what
// a budget can honestly bound. Measured on 2026-08-21 against the real tools,
// with a codex profile of --agent claude --model haiku --effort low and the
// pointer condition for a two-digit task:
//
//	the 232-character pointer condition, and the settings document
//	written to a file under this machine's TMPDIR (a 48-character
//	directory), render a whole line of 377 characters.
//
// The budget sits at 512: room for a longer temp path, another profile flag
// and a five-digit task number, and still less than a quarter of the ~1.4k
// line that came out broken on the live shell.
const TypedLineBudget = 512

// TypedLine reconstructs the command line herdr types into a worker's pane
// for an agent argv: the client's own name, then every argument, quoted the
// way the live panes showed it — `claude --settings '{"disabl…`.
func TypedLine(agentArgs []string) string {
	parts := make([]string, 0, len(agentArgs)+1)
	parts = append(parts, Kind)
	for _, a := range agentArgs {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellSafe are the characters a shell passes through untouched, so an
// argument made only of them is typed bare.
const shellSafe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, shellSafe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TrustDialogMarkers are the phrases Claude Code's trust-folder dialog puts
// on screen. Seeing one is the only thing that earns an Enter.
var TrustDialogMarkers = []string{"do you trust the files in this folder"}

// GoalMarkers are what a registered /goal leaves on screen: the echo of the
// condition, or the status line that says it is driving.
var GoalMarkers = []string{"goal set:", "/goal active"}

// startCodesMeaningNotReady are the refusals that say only that the worker
// never reached interactive readiness. Both are ordinary here: a registered
// goal causes the first, and a startup dialog causes the second.
var startCodesMeaningNotReady = map[string]bool{"timeout": true, "agent_not_ready": true}

// PaneBusyCode is herdr's refusal when `agent start` finds the target pane's
// shell still running something. Measured verbatim against herdr 0.8.2:
//
//	{"error":{"code":"agent_pane_busy","message":"agent target pane wM:p13 is not an available shell"}}
//
// It arrives in about ten milliseconds and starts nothing on the way, which
// is what makes it safe to ask again.
const PaneBusyCode = "agent_pane_busy"

// DefaultShellCeiling bounds the wait for a pane's shell when the pipeline
// carries no bound of its own.
const DefaultShellCeiling = 30 * time.Second

// DefaultConfirmCeiling bounds the wait for a goal to show on screen when the
// pipeline carries no bound of its own.
//
// Measured on the live codex path — a pane split, `eval "$(proxenos
// env)"` run in its shell, then a claude started with the goal in its argv —
// the pane was listed by herdr at 1.14s and the goal marker was on screen at
// 2.23s. The live dispatcher run that this bound exists for was far slower
// than that: the goal registered only AFTER its 60s window, and the pane was
// retired out from under a worker that was still coming up. Two minutes
// covers the observed failure with room, and the cost of overshooting is now
// only a longer tick rather than a killed worker: past the ceiling the pane
// is kept and later ticks decide.
const DefaultConfirmCeiling = 2 * time.Minute

// KeepPaneError is a spawn failure the pipeline refuses to clean up after.
// It means the worker could not be READ, not that the goal was refused: a
// goal that registered puts the worker to work immediately, so a pane the
// dispatcher cannot see into may hold a worker already claiming its task.
// Closing that pane kills the task mid-flight, which is strictly worse than
// leaving a pane up and saying so. The pane and its binding both survive.
type KeepPaneError struct {
	Pane string
	Err  error
}

func (e *KeepPaneError) Error() string { return e.Err.Error() }
func (e *KeepPaneError) Unwrap() error { return e.Err }

// Pipeline holds the adapters and the bounds of every wait it makes.
type Pipeline struct {
	Herdr *herdr.Client
	Proxy *proxy.Client

	// SettingsDir is where a codex spawn writes its settings file; empty
	// means the system temp directory.
	SettingsDir string
	// StartTimeout is how long herdr waits for interactive readiness. It is
	// spent in full on every registering goal, which is the normal case.
	StartTimeout time.Duration
	// DialogCeiling bounds the wait for a startup dialog that may never come.
	DialogCeiling time.Duration
	// ConfirmCeiling bounds the wait for the goal to show up on screen;
	// zero means DefaultConfirmCeiling.
	ConfirmCeiling time.Duration
	// ShellCeiling bounds the wait for the pane's own shell to come free
	// before the agent is started in it; zero means DefaultShellCeiling.
	ShellCeiling time.Duration
	// Poll is the gap between two reads of the pane.
	Poll time.Duration
	// ReadLines is how much of the pane each read asks for; zero means 200.
	ReadLines int
	// Log is where a placement fallback is reported; nil says nothing.
	Log *log.Logger
	// MaxPanesPerTab is how many workers may share one of this
	// dispatcher's tabs before the next one opens a tab of its own. Zero
	// means config.DefaultMaxPanesPerTab. It exists because every pane
	// added to a tab narrows the others, and a pane narrow enough stops
	// being readable — see config.MeasuredReadableColumns.
	MaxPanesPerTab int
	// Sleep is time.Sleep unless a test replaces it.
	Sleep func(time.Duration)

	// settings is the pane each spawn's settings file was written for, and
	// the only thing keeping the file findable: nothing else in this repo
	// knows the path, so a pane retired any other way leaks it.
	mu       sync.Mutex
	settings map[string]string
	// tabs is the tab each spawn opened, by the pane that came up in it.
	// Retiring a worker closes the TAB, because the tab is what the spawn
	// created; closing only the pane would leave an empty tab behind for
	// every task this dispatcher ever ran.
	tabs map[string]string
}

// Request is one worker to bring up.
type Request struct {
	// Name is the agent name herdr registers, unique among live agents.
	Name string
	// BasePane is the pane to split the worker's off.
	BasePane string
	// Cwd is the worker's working directory: the task's own project.
	Cwd string
	// Profile is the launch preset the argv is assembled from.
	Profile config.Profile
	// Goal is the one-line condition, without its slash command.
	Goal string
	// Label is what the worker's tab is called, for an operator to find and
	// for this dispatcher to recognise. Empty means the request's Name.
	Label string
	// OriginPane is the pane the task was created from, and the address the
	// report is owed at. It is empty for a task nothing with a pane filed,
	// and then BasePane is the only address there is.
	OriginPane string
}

// reportPane is where whatever comes up in the pane owes its report: the
// pane the task came from when the board named one, and the daemon's own
// pane otherwise. It is never the pane the worker is split off — this
// daemon has only its own pane to split from, wherever the task came from.
// label is the tab name this request asks for.
func (r Request) label() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

func (r Request) reportPane() string {
	if r.OriginPane != "" {
		return r.OriginPane
	}
	return r.BasePane
}

// Run brings up one worker and returns the pane it lives in. A failure the
// pipeline could READ retires the pane behind it: a half-built worker is
// worse than none, because the board would see a pane that never claims. A
// failure it could not read hands the pane back alongside the error, so the
// dispatcher keeps the binding and the operator decides.
func (p *Pipeline) Run(ctx context.Context, req Request) (string, error) {
	agentArgs := req.Profile.AgentArgs()

	// Step zero for the codex provider, before anything is built: the proxy
	// publishes the client-policy half, and a down daemon says so here.
	var settingsDoc string
	if req.Profile.Provider == config.ProviderCodex {
		if req.Profile.HasSettingsArg() {
			return "", fmt.Errorf("spawn %s: the profile already carries --settings, which the codex provider must splice itself; the client keeps only the last of two and drops the first without saying so", req.Name)
		}
		doc, err := p.Proxy.Settings(ctx)
		if err != nil {
			return "", fmt.Errorf("spawn %s: %w", req.Name, err)
		}
		settingsDoc = doc
	}

	// Claude Code takes its initial prompt positionally, and the goal is the
	// whole of it. It stays here whole: it has to reach the slash-command
	// parser, and shortening the typed line is the settings document's job.
	agentArgs = append(agentArgs, GoalPrefix+req.Goal)

	// The report address travels in the pane's environment rather than on the
	// typed line: it costs nothing there, and the condition stays a pointer
	// that does not go stale when the daemon moves pane.
	pane, err := p.place(ctx, req)
	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", req.Name, err)
	}

	if err := p.build(ctx, req, pane, settingsDoc, agentArgs); err != nil {
		var keep *KeepPaneError
		if errors.As(err, &keep) {
			// The settings file stays with the pane: the worker may be at
			// work already, and Claude Code re-reads it during a session.
			return pane, err
		}
		if closeErr := p.Retire(ctx, pane); closeErr != nil {
			return "", fmt.Errorf("%w (and the pane could not be retired: %v)", err, closeErr)
		}
		return "", err
	}
	return pane, nil
}

// place brings up the pane a worker will live in, and returns it.
//
// A worker never shares a HUMAN's tab. That is the whole rule, and everything
// else here follows from it: this dispatcher's own tabs are the only ones it
// will add a pane to, and when none of them has room it opens another.
//
// A tab also belongs to ONE task. Its label already carries the task number,
// so placement is a comparison rather than new state: a worker goes in the
// tab opened for ITS OWN task, or a new tab is opened for it. A verifier
// belongs with the worker it verifies, which is the same task, so the same
// comparison puts it in the same tab with no special case.
//
// Inside that tab the splits follow config.GridSplit, so four panes come out
// as four equal rectangles rather than a column beside a stack.
//
// The env travels on whichever call opens the pane, so a worker carries its
// report address and its cache TTL whether it was the first in a tab or the
// fifth.
func (p *Pipeline) place(ctx context.Context, req Request) (string, error) {
	env := []string{
		DispatcherPaneVar + "=" + req.reportPane(),
		ShortPromptCacheVar + "=1",
	}
	ws := p.workspaceFor(ctx, req)

	if tab, panes := p.roomInOwnTab(ctx, ws, req.label()); tab != "" {
		target, direction := config.GridSplit(len(panes))
		pane, err := p.Herdr.PaneSplit(ctx, panes[target-1], direction, "0.5", req.Cwd, env...)
		if err == nil {
			p.remember(pane, tab)
			return pane, nil
		}
		// A tab that would not take another pane is not a reason to give up
		// on the worker. A tab of its own always works.
		p.logf("task pane could not be added to tab %s, opening a tab of its own instead: %v", tab, err)
	}

	tab, pane, err := p.Herdr.TabCreate(ctx, ws, req.Cwd, req.label(), env...)
	if err != nil {
		return "", err
	}
	p.remember(pane, tab.TabID)
	return pane, nil
}

// roomInOwnTab finds the tab this dispatcher opened FOR THIS TASK in the
// given workspace that is still under the pane cap, and the panes already in
// it, in order. It returns an empty tab id when there is none, which is the
// ordinary case for a task's first worker and for a workspace that is all the
// operator's.
//
// The comparison is against the whole label and not its prefix. A tab the
// operator made is never a candidate, and neither is a tab this dispatcher
// opened for a DIFFERENT task: the label is the operator's signpost to the
// work, and a tab holding two tasks names only one of them.
func (p *Pipeline) roomInOwnTab(ctx context.Context, ws, label string) (tab string, held []string) {
	tabs, err := p.Herdr.Tabs(ctx)
	if err != nil {
		return "", nil
	}
	panes, err := p.Herdr.PaneList(ctx)
	if err != nil {
		return "", nil
	}

	inTab := make(map[string][]string)
	for _, row := range panes {
		if row.TabID != "" {
			inTab[row.TabID] = append(inTab[row.TabID], row.PaneID)
		}
	}
	for _, t := range tabs {
		if !OwnTab(t.Label) || t.Label != label || (ws != "" && t.WorkspaceID != ws) {
			continue
		}
		n := len(inTab[t.TabID])
		if n == 0 || n >= p.maxPanesPerTab() {
			continue
		}
		return t.TabID, inTab[t.TabID]
	}
	return "", nil
}

func (p *Pipeline) maxPanesPerTab() int {
	if p.MaxPanesPerTab > 0 {
		return p.MaxPanesPerTab
	}
	return config.DefaultMaxPanesPerTab
}

// logf tells the operator what happened when a fallback was taken. A nil Log
// is a test that does not care.
func (p *Pipeline) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log.Printf(format, args...)
	}
}

// workspaceFor is the workspace a worker's tab is opened in: the one the
// task was FILED from when the board named a pane and that pane is still
// alive, and this daemon's own otherwise.
//
// Following the origin pane is what puts a worker where the person who asked
// for it is looking. It is a preference and never a requirement: a task filed
// from a pane that has since gone is an ordinary task, and refusing to spawn
// for it would strand work on nothing worse than a closed window. Every
// failure here — an origin pane the board never named, a pane list that
// cannot be read, a daemon whose own pane Herdr does not list — falls back
// one step at a time and ends at the empty string, which lets Herdr choose.
func (p *Pipeline) workspaceFor(ctx context.Context, req Request) string {
	panes, err := p.Herdr.PaneList(ctx)
	if err != nil {
		return ""
	}
	at := make(map[string]string, len(panes))
	for _, pane := range panes {
		at[pane.PaneID] = pane.WorkspaceID
	}
	if ws := at[req.OriginPane]; req.OriginPane != "" && ws != "" {
		return ws
	}
	return at[req.BasePane]
}

// TabOf is the tab a spawn placed a pane in, for the dispatcher to record on
// the binding. Empty when this process did not place it.
func (p *Pipeline) TabOf(pane string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tabs[pane]
}

// Adopt puts back the pane-to-tab mapping a previous process wrote down,
// which is how a restart can still give a tab back. Without it the tab is
// only re-derivable from the pane's own tab id, and that is not enough: a tab
// holds several workers, so closing it on the strength of one pane would take
// the others with it.
func (p *Pipeline) Adopt(pane, tab string) {
	if pane == "" || tab == "" {
		return
	}
	p.remember(pane, tab)
}

// remember records the tab a spawn opened against the pane inside it.
func (p *Pipeline) remember(pane, tab string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tabs == nil {
		p.tabs = make(map[string]string)
	}
	p.tabs[pane] = tab
}

// Retire closes the worker's TAB and removes the settings file its spawn
// wrote. It is the only way a pane this pipeline opened should be closed:
// closing it any other way leaves the file behind, and nothing else knows
// where it is.
//
// Which tab is asked of Herdr rather than only of memory, because memory
// does not survive a restart and the tabs of a daemon that went down are
// exactly the ones that need retiring. Whatever the tab is found to be, it
// is closed only if its LABEL says this dispatcher opened it: a worker pane
// the operator has since dragged into a tab of their own closes as a pane,
// and their tab stays. That guard is the point — this verb is reached with
// panes, and a pane is not a licence over whatever tab happens to hold it.
func (p *Pipeline) Retire(ctx context.Context, pane string) error {
	tab, alone := p.tabFor(ctx, pane)
	p.Discard(pane)
	if tab == "" || !alone {
		// Either the tab is not this dispatcher's to close, or another
		// worker is still in it. Closing the pane retires this worker
		// either way, and takes the tab with it only when nothing is left.
		return p.Herdr.PaneClose(ctx, pane)
	}
	return p.Herdr.TabClose(ctx, tab)
}

// tabFor is the tab this pipeline may close for a pane, or empty when there
// is none it may. A tab this process opened is known outright; otherwise the
// pane is traced through Herdr to its tab and the tab's label is what
// decides. A label that is not this dispatcher's own is an operator's tab and
// yields nothing, whatever the pane inside it is.
func (p *Pipeline) tabFor(ctx context.Context, pane string) (tab string, alone bool) {
	p.mu.Lock()
	known := p.tabs[pane]
	delete(p.tabs, pane)
	p.mu.Unlock()

	panes, err := p.Herdr.PaneList(ctx)
	if err != nil {
		// Nothing can be said about how many panes the tab holds, and
		// closing a tab on that guess could take another live worker with
		// it. The pane is closed instead, which is always safe.
		return "", false
	}
	tabOf := make(map[string]string, len(panes))
	held := make(map[string]int, len(panes))
	for _, row := range panes {
		tabOf[row.PaneID] = row.TabID
		held[row.TabID]++
	}

	id := known
	if id == "" {
		id = tabOf[pane]
	}
	if id == "" {
		return "", false
	}

	// A tab this process opened is known to be its own; any other has to
	// prove it by its label, which is what keeps an operator's tab safe
	// when a pane inside it happens to be one of ours.
	if known == "" {
		tabs, err := p.Herdr.Tabs(ctx)
		if err != nil {
			return "", false
		}
		var ours bool
		for _, t := range tabs {
			if t.TabID == id && OwnTab(t.Label) {
				ours = true
			}
		}
		if !ours {
			return "", false
		}
	}
	return id, held[id] <= 1
}

// Discard removes the settings file a spawn wrote for a pane, if it wrote
// one. It is for the pane that is already gone, which has no worker left to
// read it; Retire is for the pane that still has to be closed.
func (p *Pipeline) Discard(pane string) {
	p.mu.Lock()
	path := p.settings[pane]
	delete(p.settings, pane)
	p.mu.Unlock()
	if path != "" {
		os.Remove(path)
	}
}

// writeSettings puts the proxy's document where the worker can read it and
// remembers the path against the pane, so retiring the pane takes the file
// with it.
func (p *Pipeline) writeSettings(pane, doc string) (string, error) {
	f, err := os.CreateTemp(p.SettingsDir, "hdis-settings-*.json")
	if err != nil {
		return "", fmt.Errorf("settings file: %w", err)
	}
	path := f.Name()
	err = f.Chmod(SettingsFileMode)
	if err == nil {
		_, err = f.WriteString(doc)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("settings file %s: %w", path, err)
	}

	p.mu.Lock()
	if p.settings == nil {
		p.settings = make(map[string]string)
	}
	p.settings[pane] = path
	p.mu.Unlock()
	return path, nil
}

func (p *Pipeline) build(ctx context.Context, req Request, pane, settingsDoc string, agentArgs []string) error {
	if req.Profile.Provider == config.ProviderCodex {
		// The settings half travels as a file rather than inline, because
		// the argv is typed into the pane and the document is most of what
		// made that line long enough to break. `claude --settings <path>`
		// loads a path exactly as it loads the same JSON inline, measured
		// against claude 2.1.238: the same document in a file and on the
		// command line produced the same behaviour, and a path that does
		// not exist is refused with "Settings file not found: <path>".
		path, err := p.writeSettings(pane, settingsDoc)
		if err != nil {
			return fmt.Errorf("spawn %s: %w", req.Name, err)
		}
		agentArgs = append([]string{"--settings", path}, agentArgs...)

		// The environment half belongs to the pane's shell, which the agent
		// then inherits as a direct child.
		if err := p.Herdr.PaneRun(ctx, pane, p.Proxy.EnvCommand()); err != nil {
			return fmt.Errorf("spawn %s: %w", req.Name, err)
		}
	}

	err := p.startWhenShellIsFree(ctx, herdr.StartRequest{
		Name:      req.Name,
		Kind:      Kind,
		Pane:      pane,
		Timeout:   p.StartTimeout,
		AgentArgs: agentArgs,
	})
	if err != nil && !notReady(err) {
		return fmt.Errorf("spawn %s: %w", req.Name, err)
	}

	if err := p.answerStartupDialog(ctx, pane); err != nil {
		return fmt.Errorf("spawn %s: %w", req.Name, err)
	}
	if err := p.confirmGoal(ctx, pane); err != nil {
		var keep *KeepPaneError
		if errors.As(err, &keep) {
			keep.Err = fmt.Errorf("spawn %s: %w", req.Name, keep.Err)
			return keep
		}
		return fmt.Errorf("spawn %s: %w", req.Name, err)
	}
	return nil
}

// startWhenShellIsFree types the agent into its pane only once herdr agrees
// the pane's shell is free to take it, and herdr's own refusal is the signal.
// Retrying agent_pane_busy asks herdr the exact question herdr will act on,
// which no reading of the pane's process table can promise: between seeing an
// idle shell and starting into it, the shell can go busy again.
//
// The codex path is what makes the wait ordinary. Its environment half runs
// in the pane's shell, and `pane run` returns as soon as the line is typed,
// so the eval is still running when the start would otherwise arrive. A pane
// whose shell is already free costs one attempt and no sleeping.
func (p *Pipeline) startWhenShellIsFree(ctx context.Context, req herdr.StartRequest) error {
	var err error
	for i, n := 0, attempts(p.shellCeiling(), p.Poll); i < n; i++ {
		if _, err = p.Herdr.AgentStart(ctx, req); !paneBusy(err) {
			return err
		}
		p.sleep(p.Poll)
	}
	return err
}

// answerStartupDialog watches for the trust-folder dialog and answers it once
// if it comes. A dialog that never appears is not a failure — the ceiling
// ends the wait, and no key is ever pressed on a screen that did not ask.
func (p *Pipeline) answerStartupDialog(ctx context.Context, pane string) error {
	for i, n := 0, attempts(p.DialogCeiling, p.Poll); i < n; i++ {
		text, err := p.Herdr.PaneRead(ctx, pane, p.readLines())
		if err != nil {
			// A screen that cannot be read is not a dialog, and it is not a
			// verdict either. Confirming the goal is where unreadability
			// gets to mean something.
			p.sleep(p.Poll)
			continue
		}
		if contains(text, GoalMarkers) {
			return nil // already past any dialog
		}
		if contains(text, TrustDialogMarkers) {
			return p.Herdr.PaneSendKeys(ctx, pane, "enter")
		}
		p.sleep(p.Poll)
	}
	return nil
}

// confirmGoal reads the pane until the goal shows, and falls back to the
// worker's own status: a registered goal puts it to work immediately.
//
// Running out of ceiling is not a verdict. Only one thing on this screen is:
// herdr calling the worker idle means it is sitting at its prompt box with
// nothing armed, and no goal is coming. Everything else — a read that never
// came back, a worker herdr is still calling unknown or blocked — is a worker
// that may register seconds later, and retiring it is how one task ends up
// with two live workers: the pane dies, the task goes back to ready, and the
// next tick spawns another on top of it. Those panes are kept, and the ticks
// that follow decide.
func (p *Pipeline) confirmGoal(ctx context.Context, pane string) error {
	var last, status string
	var readErr error
	for i, n := 0, attempts(p.confirmCeiling(), p.Poll); i < n; i++ {
		text, err := p.Herdr.PaneRead(ctx, pane, p.readLines())
		readErr = err
		if err == nil {
			if contains(text, GoalMarkers) {
				return nil
			}
			if strings.TrimSpace(text) != "" {
				last = text
			}
		}
		if a, err := p.Herdr.AgentGet(ctx, pane); err == nil {
			if a.Status == herdr.StatusWorking {
				return nil
			}
			status = a.Status
		}
		p.sleep(p.Poll)
	}
	if readErr != nil {
		return &KeepPaneError{Pane: pane, Err: fmt.Errorf(
			"pane %s could not be read in %s, so whether the goal registered is unknown: %w; "+
				"the pane is kept because a registered goal means a worker already at work",
			pane, p.confirmCeiling(), readErr)}
	}
	if status != herdr.StatusIdle {
		return &KeepPaneError{Pane: pane, Err: fmt.Errorf(
			"the goal has not registered in pane %s within %s and herdr calls the worker %q, "+
				"so it is still coming up rather than done with the question; the pane is kept "+
				"and later ticks decide. It last showed: %s",
			pane, p.confirmCeiling(), statusOrUnknown(status), tail(last))}
	}
	return fmt.Errorf("the goal never registered in pane %s within %s and herdr calls the worker idle, "+
		"so it is at its prompt with nothing armed; the pane last showed: %s",
		pane, p.confirmCeiling(), tail(last))
}

// statusOrUnknown names what herdr said, including when it never answered.
func statusOrUnknown(status string) string {
	if status == "" {
		return herdr.StatusUnknown
	}
	return status
}

func (p *Pipeline) shellCeiling() time.Duration {
	if p.ShellCeiling > 0 {
		return p.ShellCeiling
	}
	return DefaultShellCeiling
}

func (p *Pipeline) confirmCeiling() time.Duration {
	if p.ConfirmCeiling > 0 {
		return p.ConfirmCeiling
	}
	return DefaultConfirmCeiling
}

func (p *Pipeline) readLines() int {
	if p.ReadLines > 0 {
		return p.ReadLines
	}
	return 200
}

func (p *Pipeline) sleep(d time.Duration) {
	if p.Sleep != nil {
		p.Sleep(d)
		return
	}
	time.Sleep(d)
}

// attempts turns a ceiling into a number of reads, always at least one: a
// bound of zero would mean never looking at all.
func attempts(ceiling, poll time.Duration) int {
	if poll <= 0 || ceiling <= poll {
		return 1
	}
	return int(ceiling / poll)
}

func contains(text string, markers []string) bool {
	lower := strings.ToLower(text)
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// tail is the end of what the pane showed, which is where a refusal lands.
func tail(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "(nothing)"
	}
	const max = 400
	if len(text) > max {
		text = "…" + text[len(text)-max:]
	}
	return strings.Join(strings.Fields(text), " ")
}

// paneBusy reports whether herdr refused a start because the pane's shell was
// still occupied — the one refusal worth asking again about.
func paneBusy(err error) bool {
	var herr *herdr.Error
	return errors.As(err, &herr) && herr.Code == PaneBusyCode
}

func notReady(err error) bool {
	var herr *herdr.Error
	if !errors.As(err, &herr) {
		return false
	}
	return startCodesMeaningNotReady[herr.Code]
}
