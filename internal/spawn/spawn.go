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

	// Direction the worker pane is split in; empty means to the right.
	Direction string
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
	// Sleep is time.Sleep unless a test replaces it.
	Sleep func(time.Duration)

	// settings is the pane each spawn's settings file was written for, and
	// the only thing keeping the file findable: nothing else in this repo
	// knows the path, so a pane retired any other way leaks it.
	mu       sync.Mutex
	settings map[string]string
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

	// The dispatcher's address travels in the pane's environment rather than
	// on the typed line: it costs nothing there, and the condition stays a
	// pointer that does not go stale when the daemon moves pane.
	pane, err := p.Herdr.PaneSplit(ctx, req.BasePane, p.direction(), req.Cwd,
		DispatcherPaneVar+"="+req.BasePane)
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

// Retire closes a worker's pane and removes the settings file its spawn
// wrote. It is the only way a pane this pipeline opened should be closed:
// closing it any other way leaves the file behind, and nothing else knows
// where it is.
func (p *Pipeline) Retire(ctx context.Context, pane string) error {
	err := p.Herdr.PaneClose(ctx, pane)
	p.Discard(pane)
	return err
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

func (p *Pipeline) direction() string {
	if p.Direction != "" {
		return p.Direction
	}
	return "right"
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
