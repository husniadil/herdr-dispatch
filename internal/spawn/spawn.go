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
//     blocks startup. It is answered when it is seen and never blind, and
//     the key it is answered with is READ off the screen: measured on
//     2026-08-29 against claude 2.1.251, the dialog opens with the caret on
//     "No, exit", where the Enter that was right for every earlier build
//     exits the worker. See TrustCursorMarker and trustDialogKeys.
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
	"unicode"
	"unicode/utf8"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
)

// Kind is the agent kind every worker starts as. Both providers run the same
// client; the codex provider only changes how it is routed.
const Kind = "claude"

// GoalPrefix turns a condition into the slash command that arms it.
const GoalPrefix = "/goal "

// DispatcherPaneVar is the environment variable every pane this pipeline
// opens carries: the DESK the task's report is owed at, so a worker has an
// address to answer at without one being typed into its condition.
//
// The name predates the rule and is kept because agents read it: it is not
// the daemon's own pane except as the last of desk()'s three rungs. What
// place() writes into it is the pane the board says the task was filed from,
// else a live pane already sitting in the task's project, else this daemon's
// pane.
//
// It is an address and nothing more. Publishing it lets a worker send to the
// desk; it never says who the worker is. The sender on anything the
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
// (that transcript predates htask flattening its task verbs, so it shows the
// then-current group spelling; the corruption it records is about the LENGTH
// of a typed line and nothing about which verb was on it)
//
// while throwaway probes typed into a bare shell stayed clean to a megabyte.
// The fix is to stop typing long lines, not to trim the condition harder.
//
// So the pointer carries only what a worker cannot get anywhere else: how to
// take the task, where to read what it asks for, and the end state. The
// criteria stay on the board, where `htask get` reads them whole and no
// shell ever types them, and the end state is named so /goal can still judge
// the transcript and finish.
//
// One rule rides along that is NOT on the board and cannot be: which checkout
// is the worker's own. hdis makes a worktree per task and opens the pane in
// it, so the pane's working directory already IS the answer — but three
// workers on 2026-08-24/25 committed past it anyway, twice onto the shared
// checkout's main and once into a SIBLING repository's checkout, and all
// three bypassed the review gate the branch exists to reach. Nothing on the
// board would have said otherwise: a task's criteria are about the work and
// never about where it is done. So the condition says it — the working
// directory is the only writable checkout, everything else is read-only, and
// a task that needs a sibling changed is filed on that sibling's board rather
// than edited in place.
func PointerGoal(seq int) string {
	return fmt.Sprintf("task %d is submitted for review: claim it with htask claim %d, "+
		"read its full criteria with htask get %d, do the work, then run "+
		"htask submit %d with a report and evidence. "+
		"Your working directory is the only writable checkout: never edit or commit "+
		"outside it. A sibling repo is read-only; file a task on its board. "+
		"Reach the dispatcher at $"+DispatcherPaneVar+".", seq, seq, seq, seq)
}

// SelfReviewCondition composes the second condition a worker is prompted with
// when its own task reaches review. It is the whole of the verification lane:
// nothing separate launches, and the pane that produced the work is the pane
// that checks it.
//
// It does not say "review your work", and that is the point. Five rejections
// in a row — tasks 42, 48, 51, 73 and the earlier 40 — were the same shape: a
// guard the report documented with no test behind it. Both the worker and an
// INDEPENDENT verifier read past all five, so rereading is not what catches
// them; a compiling mutation is. Delete the guard, run the named tests, read
// the exit code. Believing the work is finished does not change it, which is
// why a reader who knows the work is not disqualified from running it.
//
// The last clause is not decoration either. A mutation that does not bite is
// ambiguous — a missing test, or aim that never touched the guard — and
// silence about it hides exactly the case the operator has to judge, so the
// worker is asked which one it thinks it is.
//
// It names WHERE the findings go, and that is not decoration either. Asking
// for a report and naming no route is how a lane built to catch undelivered
// claims makes one of its own: htask refuses `task submit` on a row that is
// not doing, so a worker whose task is in review cannot amend the report it
// already sent, and findings with nowhere to go die in the pane while the
// board stays green.
//
// The route is the mail door at $HDIS_DISPATCHER_PANE, which is the desk that
// owns the work — the pane the task was created from, not whoever started the
// daemon. Three reasons it beats a board note. The address is already in the
// pane's environment and already named by the condition the worker booted
// with, so shot two reuses an address the conversation is holding rather than
// introducing a second place to look. A board note is not task-scoped: htask
// has no task-scoped note verb, so the findings would land on the notes board
// as a pre-decision idea, detached from the row the operator is about to
// judge. And durability, the one real argument for the note, is already the
// mail store's: `inbox` lists what was sent whether or not the pane marker
// ever arrived, so an unread notify sits there rather than evaporating.
//
// It is the DOOR and never `hmail`, for the reason the verifier's condition
// recorded before it: a dispatched pane is opened in a worktree under the
// state directory, where a plugin binary kept in a project's own bin/ is not
// on PATH. Measured from a live worker pane on 2026-08-23: `hmail` came back
// `command not found` while the mail MCP door answered. An MCP door is
// configured for the agent rather than resolved from the working directory.
//
// It travels through `herdr agent prompt` rather than the typed spawn line,
// so TypedLineBudget does not bound it; it is kept near a nudge's length
// anyway, because everything hdis puts into a pane goes through a terminal.
//
// It says what to DO with a finding, because a condition that asks for
// mutations and stops there leaves the worker to invent the rule, and two
// workers in a row did. The answer is KEEP FIXING. The shot is cheap exactly
// because the pane is warm and the context is still there, so a rule that
// says find the gap then sit on it defers the cheapest moment to fix anything
// and throws away what the lane was built for. A moved head costs the
// operator one more gate run, which is machine time; holding costs real gaps
// left unpinned to protect a hash, and the hash is the wrong thing to
// protect. The new head is named in the FIRST line of the message so the
// operator reads it before choosing what to gate.
//
// Two boundaries travel with it, because keep fixing must not read as keep
// building: a gap a mutation proved, or something the operator named. A new
// idea, a widened scope, a second thought about the design goes in the
// message as a proposal and waits for a verdict. Both of task 78's workers
// stayed inside that line unprompted, so the wording describes that behaviour
// rather than restricting it.
//
// And nothing is promised for later. hdis retires a bound pane the moment its
// task reaches a terminal status, so a finding a worker files "after this
// lands" dies with the pane that owed it — on herdr-tasks task 78 the
// operator filed the note in its place. The lane cannot hold the pane open
// instead: hdis stops at review and never learns the verdict's timing, so
// holding would mean holding indefinitely, and the binding is the one thing
// this daemon closes on a terminal row. So the condition spends the message
// it is already sending: everything the worker has, including what it will
// not fix, goes out before the pane closes.
//
// One half of this is the operator's and hdis cannot enforce it, so it is
// recorded here as prose rather than pinned: verify the head the MESSAGE
// names, and read the message before starting the gate. Getting that
// backwards is what made task 78's first moved head cost anything at all.
//
// One pass is not aimed at the report at all, and that is the point of it.
// Everything above is scoped to what the report CLAIMS, so a defect nobody
// claimed passes the whole mechanical half structurally. The two findings
// worth the most on 2026-08-23 both came from outside that scope and both
// were worker initiative: a mutation that skipped registering two newly
// published tools failed three tests, probing a claim the report only made
// implicitly, and reverting a documentation entry to its old reading left
// everything green, which proved a condition the operator had set was
// resting on nothing. Neither is reachable from "for every guard your report
// claims".
//
// So the open pass names the DIFF and says it is not the report, because
// rereading a frame finds what the frame already contains. It reads the diff
// against a THIRD frame as well, the task's own acceptance criteria, because
// the report is the worker's frame and the criteria are the operator's and
// neither contains the other: a report can be internally consistent, survive
// every mutation above, and still leave a criterion with nothing implementing
// it. And the pass carries a floor of its own, because an open invitation has
// none: every observation is either proved with a mutation or a run, or
// labelled plainly as a suspicion that could not be proved. An unproven
// suspicion is worth sending — sifting it costs the operator one read. One
// dressed as a finding costs the round this lane was built to save.
//
// Recusal is untouched: this produces no verdict. The task stays in review
// and the operator still approves or rejects.
func SelfReviewCondition(seq int) string {
	return fmt.Sprintf("Task %d is submitted and not yet judged. For every guard, refusal or "+
		"invariant your report claims, write a COMPILING mutation that removes it, run the "+
		"tests your report names, and confirm they FAIL. Revert each one. Then report which "+
		"mutations bit and which did not, and for each that did not, say whether you believe "+
		"it is a missing test or bad aim, with the mail MCP send to $"+DispatcherPaneVar+". "+
		"KEEP FIXING what a mutation proved unpinned, and name the new head in the first line "+
		"of that message. Fix only a gap a mutation proved or something the operator named; "+
		"send anything else as a proposal that waits for a verdict. Then read the diff itself, "+
		"not your report of it, against what the task asked for: a criterion the diff never "+
		"implements, a gap your report never claimed, a case the code does not handle. Prove "+
		"each with a mutation or a run, or say plainly that it is a suspicion you could not "+
		"prove. Send everything you have "+
		"before your pane closes with the task; nothing waits for later.", seq)
}

// PromptedGoalBudget bounds a /goal that arrives through `herdr agent
// prompt` rather than through the spawn line.
//
// It is a SECOND ceiling and not TypedLineBudget, which bounds a different
// path for a different reason: the typed spawn line is typed into a shell by
// herdr, and it broke at ~1.4k on a live pane. A prompted /goal never reaches
// a shell; what bounds it is what the client's own prompt box accepts before
// the slash command stops registering.
//
// The number is the operator's measurement, recorded here rather than
// rederived: 1024 is the ceiling and 1023 is safe. So this is the whole
// delivered text — GoalPrefix and the condition together — and not the
// condition alone, because the prefix is on the line the client parses.
const PromptedGoalBudget = 1023

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
// The budget sat at 512 until the condition took on the isolation rule, which
// added 127 characters and left a five-digit task number no room. Re-measured
// on 2026-08-25 with the same profile, the whole line is 505 characters for a
// two-digit task.
//
// The worker's own doors then joined the line: `--mcp-config <path>
// --strict-mcp-config` is a fixed 33 characters plus the path. Measured on
// 2026-08-27 with the same profile and the default document at
// <state_dir>/worker.mcp.json (50 characters here), the whole line is 590
// characters for a two-digit task and 602 for a five-digit one — 84 more than
// the 506 the same shape rendered without them, and still inside this budget,
// so it did not move.
//
// It sits at 640 now: the same headroom the 512 was chosen for — a longer temp
// path, another profile flag and a five-digit task number — and still under
// half of the ~1.4k line that came out broken on the live shell. What bounds
// this is the measured break, never the budget's own roundness, and the test
// that types the corrupted shape still refuses it at this ceiling.
const TypedLineBudget = 640

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
//
// It is a SET, the way GoalMarkers is, because claude rewords this dialog
// between builds and a single phrase expires with the build it was read from.
// claude 2.1.239 dropped "do you trust the files in this folder" for "Quick
// safety check: Is this a project you created or one you trust?", which left
// the detector matching nothing at any pane width; keeping both means an
// operator on either build is answered. The phrase taken from the newer
// dialog is its selectable option rather than its prose, because the option
// is what the dialog is FOR and the sentence around it is what churns.
//
// Adding a longer phrase here raises config.MeasuredReadableColumns, which is
// derived from the longest marker; the test that derives it will say so.
var TrustDialogMarkers = []string{
	"yes, i trust this folder",
	"do you trust the files in this folder",
}

// TrustCursorMarker is the caret claude draws on the selected line of a menu.
// The line carrying it is the only thing on the screen that says what an
// Enter would take, which is why the answer is read there rather than assumed
// from the wording.
const TrustCursorMarker = "❯"

// TrustCursorAccepts are the option texts that, under the caret, mean Enter
// accepts the folder. They are the OPTIONS of both wordings rather than the
// prose around them, and they are deliberately not TrustDialogMarkers: that
// set is what earns an answer at all, and one of its two phrases is a
// sentence that never sits on a cursor line.
//
// Nothing here widens a read. The cursor line is inside the same block the
// dialog marker was already matched in, so it costs no extra rows or columns
// and does not move config.MeasuredReadableColumns.
var TrustCursorAccepts = []string{"i trust this folder", "yes, proceed"}

// TrustCursorRefuses is the word that, under the caret, means Enter would
// exit the worker. It is matched as a whole word so an option line reading
// "not now" or "I know this folder" is never mistaken for "No, exit".
const TrustCursorRefuses = "no"

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
	Herdr *herdrclient.Client
	Proxy *proxy.Client

	// SettingsDir is where a codex spawn writes its settings file; empty
	// means the system temp directory.
	SettingsDir string
	// WorkerMCPPath is where the default MCP document is written for a
	// worker no profile configured one for; empty means
	// config.WorkerMCPConfigPath().
	WorkerMCPPath string
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
	// OwnTrees is the directory this dispatcher creates its checkouts
	// under, the same bound the reap is drawn at. A pane sitting under it
	// is one of this daemon's own workers, never a desk to report to.
	// Empty leaves nothing excluded, which is right for a daemon that
	// hands out no checkouts.
	OwnTrees string

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
	// OriginPane is the pane the task was created from, and the first
	// address the report is owed at. It is empty for a task nothing with a
	// pane filed, and then the desk is looked for in the pane list.
	OriginPane string
	// MCPConfig is the MCP document this worker's doors are read from, as
	// the config resolved it: the profile's own, else the fleet's. Empty is
	// nothing configured, and then the default file this pipeline writes
	// once is used instead.
	MCPConfig string
	// Project is the task's project directory, as the board records it. It
	// is what a live pane's cwd is measured against to find the desk that
	// owns the work; empty means no desk can be found that way. It is NOT
	// the worker's Cwd, which is a checkout of its own.
	Project string
}

// label is the tab name this request asks for.
func (r Request) label() string {
	if r.Label != "" {
		return r.Label
	}
	return r.Name
}

// desk is the pane that owns a task's work, and the ONE answer behind both
// questions a placement asks: where the report is owed, and which workspace
// the worker's tab opens in. Three rungs, in order:
//
//  1. the pane the board says the task was filed from. Someone with a pane
//     asked for this work, and they are the desk.
//  2. a LIVE pane whose cwd resolves to the task's project. A task filed at a
//     terminal names no pane, but the session already sitting in that
//     repository is the desk that owns the work — evidence Herdr keeps,
//     rather than a field somebody has to remember to set.
//  3. this daemon's own pane. A machine with nothing live still answers
//     somewhere, and that is the operator who started the daemon.
//
// It is never the pane the worker is split off — this daemon has only its own
// pane to split from, wherever the task came from.
func (p *Pipeline) desk(panes []herdrclient.Agent, req Request) string {
	if req.OriginPane != "" {
		return req.OriginPane
	}
	if pane := p.inProject(panes, req.Project); pane != "" {
		return pane
	}
	return req.BasePane
}

// inProject is the lowest pane id among the live panes sitting in the given
// project, and empty when none is.
//
// The LOWEST id is the rule, and the reason is that it is stable: the same
// project resolves to the same desk on every tick, so reports for one
// repository do not wander between windows as panes come and go. Most
// recently active would be a guess about which human is watching, and
// whatever a map iteration hands back first is not a rule at all.
//
// A checkout of this dispatcher's own is excluded before anything is
// compared. Task 40 puts every worker in a worktree of its task's project, so
// without that bound the first worker for a project would become the desk and
// every later report for it would be delivered to a worker.
func (p *Pipeline) inProject(panes []herdrclient.Agent, project string) string {
	if project == "" {
		return ""
	}
	best := ""
	for _, pane := range panes {
		if pane.Cwd == "" || under(pane.Cwd, p.OwnTrees) || !under(pane.Cwd, project) {
			continue
		}
		if best == "" || pane.PaneID < best {
			best = pane.PaneID
		}
	}
	return best
}

// under says whether dir is root or sits inside it. The separator is what
// makes it a directory test rather than a string test: a sibling checkout
// named after the project with something appended is a different repository.
func under(dir, root string) bool {
	if root == "" {
		return false
	}
	root = strings.TrimSuffix(root, string(os.PathSeparator))
	return dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator))
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

	// The doors, before a pane is opened: a document the operator named and
	// never wrote is a worker with no doors at all under
	// --strict-mcp-config, and refusing here costs nothing while refusing
	// after the split costs a pane and a checkout.
	mcpConfig, err := p.workerMCPConfig(req.MCPConfig)
	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", req.Name, err)
	}
	agentArgs = append([]string{"--mcp-config", mcpConfig, "--strict-mcp-config"}, agentArgs...)

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
		// The compensation runs on a context the caller's cancellation
		// cannot reach. A shutdown is exactly when this path is taken, and
		// a retire that inherits the canceled context does nothing at all:
		// the pane and its agent survive a daemon that has already written
		// them off. See cleanup.
		down, stop := cleanup(ctx)
		defer stop()
		if closeErr := p.Retire(down, pane); closeErr != nil {
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
// tab opened for ITS OWN task, or a new tab is opened for it.
//
// Inside that tab the splits follow config.GridSplit, so four panes come out
// as four equal rectangles rather than a column beside a stack.
//
// The env travels on whichever call opens the pane, so a worker carries its
// report address and its cache TTL whether it was the first in a tab or the
// fifth.
func (p *Pipeline) place(ctx context.Context, req Request) (string, error) {
	// One read of the pane list, one desk, and both answers taken from it.
	panes, err := p.Herdr.PaneList(ctx)
	if err != nil {
		// An unreadable pane list costs the evidence, not the worker: the
		// board's own pane of origin still answers, and the daemon's pane
		// is behind it.
		p.logf("the pane list could not be read, so the desk is whatever the board named: %v", err)
	}
	desk := p.desk(panes, req)
	env := []string{
		DispatcherPaneVar + "=" + desk,
		ShortPromptCacheVar + "=1",
	}
	ws := workspaceOf(panes, desk, req.BasePane)

	if tab, held := p.roomInOwnTab(ctx, panes, ws, req.label()); tab != "" {
		target, direction := config.GridSplit(len(held))
		pane, err := p.Herdr.PaneSplit(ctx, held[target-1], direction, "0.5", req.Cwd, env...)
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
func (p *Pipeline) roomInOwnTab(ctx context.Context, panes []herdrclient.Agent, ws, label string) (tab string, held []string) {
	tabs, err := p.Herdr.Tabs(ctx)
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

// workspaceOf is the workspace a worker's tab is opened in: the desk's own
// when Herdr is holding that pane, and this daemon's otherwise.
//
// Opening beside the desk is what puts a worker where the person who owns the
// work is looking. It is a preference and never a requirement: a task filed
// from a pane that has since gone is an ordinary task, and refusing to spawn
// for it would strand work on nothing worse than a closed window. Every
// failure here — a desk Herdr does not list, a pane list that could not be
// read, a daemon whose own pane is not listed either — falls back one step at
// a time and ends at the empty string, which lets Herdr choose.
func workspaceOf(panes []herdrclient.Agent, desk, base string) string {
	at := make(map[string]string, len(panes))
	for _, pane := range panes {
		at[pane.PaneID] = pane.WorkspaceID
	}
	if ws := at[desk]; ws != "" {
		return ws
	}
	return at[base]
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

// workerMCPConfig is the MCP document a worker is launched against, and it is
// the whole of the doors it gets: `--strict-mcp-config` makes the client read
// this file and nothing else, so the operator's own `~/.mcp.json` — which may
// hold only the hub connector, or anything else they like — never reaches a
// worker.
//
// A configured path is checked and never created. An operator who named a
// document is owed a refusal naming it rather than a default one written over
// their intent. Nothing configured takes the default file, written once at
// spawn time so the commands in it are resolved against a PATH the daemon
// still has.
func (p *Pipeline) workerMCPConfig(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("the configured mcp config %s cannot be read: %w", configured, err)
		}
		return configured, nil
	}
	path := p.WorkerMCPPath
	if path == "" {
		path = config.WorkerMCPConfigPath()
	}
	if err := config.EnsureWorkerMCPConfig(path); err != nil {
		return "", err
	}
	return path, nil
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
		if err := p.Herdr.PaneRun(ctx, pane, p.Proxy.EnvCommand(req.Profile.Account)); err != nil {
			return fmt.Errorf("spawn %s: %w", req.Name, err)
		}
	}

	// The budget is measured here, on the line as typed, and not only in
	// the test that fixed it: a long profile or temp path past it would
	// otherwise reach the shell as the corrupted line the budget exists to
	// refuse.
	if line := TypedLine(agentArgs); len(line) > TypedLineBudget {
		return fmt.Errorf("spawn %s: the typed line is %d characters, over the %d the shell is known to take whole",
			req.Name, len(line), TypedLineBudget)
	}

	err := p.startWhenShellIsFree(ctx, herdrclient.StartRequest{
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
func (p *Pipeline) startWhenShellIsFree(ctx context.Context, req herdrclient.StartRequest) error {
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
			keys, why := trustDialogKeys(text)
			p.logf("trust dialog in pane %s: %s, pressing %s", pane, why, strings.Join(keys, " then "))
			return p.Herdr.PaneSendKeys(ctx, pane, keys...)
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
			if a.Status == herdrclient.StatusWorking {
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
	if status != herdrclient.StatusIdle {
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
		return herdrclient.StatusUnknown
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

// trustDialogKeys are the keys that answer the trust-folder dialog on the
// screen just read, and the reason the operator is told.
//
// Which key is right is a fact on the screen rather than a constant, and it
// changed under this pipeline: measured live on 2026-08-29 against claude
// 2.1.251, the dialog opens with the caret on "No, exit", so the bare Enter
// that answered every earlier build exits the worker. The caret is read
// instead. A cursor line naming the refusing option is walked down one first;
// a cursor line naming the trusting one is confirmed where it stands; a
// screen with no cursor line on it keeps the bare Enter, which is what
// answered the pre-2.1.239 wording and is the only thing left to try.
//
// Lines that carry the caret but name neither option — a prompt box caught in
// the same read — are passed over rather than answered.
func trustDialogKeys(text string) (keys []string, why string) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, TrustCursorMarker) {
			continue
		}
		if contains(line, TrustCursorAccepts) {
			return []string{"enter"}, "the cursor is on the trusting option"
		}
		if hasWord(line, TrustCursorRefuses) {
			return []string{"down", "enter"}, "the cursor is on the refusing option"
		}
	}
	return []string{"enter"}, "no cursor line was on screen"
}

// hasWord is a whole-word, case-insensitive match, so a substring inside a
// longer word never counts as the word itself.
func hasWord(text, word string) bool {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, f := range fields {
		if f == word {
			return true
		}
	}
	return false
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
		cut := len(text) - max
		// Back up to a rune boundary: a box-drawing glyph split down the
		// middle would put invalid UTF-8 into the error this feeds.
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		text = "…" + text[cut:]
	}
	return strings.Join(strings.Fields(text), " ")
}

// paneBusy reports whether herdr refused a start because the pane's shell was
// still occupied — the one refusal worth asking again about.
func paneBusy(err error) bool {
	var herr *herdrclient.Error
	return errors.As(err, &herr) && herr.Code == PaneBusyCode
}

func notReady(err error) bool {
	var herr *herdrclient.Error
	if !errors.As(err, &herr) {
		return false
	}
	return startCodesMeaningNotReady[herr.Code]
}

// cleanup is the context a teardown compensation runs on: detached from the
// caller's cancellation, bounded so a wedged herdr cannot hold a shutdown
// open. Its whole reason is the shutdown case — the caller's context is
// already canceled by the time the compensation is reached, and every call
// made on it fails before it leaves the process.
func cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), CleanupCeiling)
}

// CleanupCeiling bounds a teardown compensation. It is the wait an operator
// is asked to sit through at shutdown, so it is short.
const CleanupCeiling = 10 * time.Second
