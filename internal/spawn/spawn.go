// Package spawn is the pipeline behind one Spawn action: a worker pane comes
// up, its agent starts with the task's goal already in its argv, and the
// pipeline confirms the goal took before the dispatcher records a binding.
//
// Three measured facts shape it, and every one of them is a fact to re-check
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
package spawn

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// Pipeline holds the adapters and the bounds of every wait it makes.
type Pipeline struct {
	Herdr *herdr.Client
	Proxy *proxy.Client

	// Direction the worker pane is split in; empty means to the right.
	Direction string
	// StartTimeout is how long herdr waits for interactive readiness. It is
	// spent in full on every registering goal, which is the normal case.
	StartTimeout time.Duration
	// DialogCeiling bounds the wait for a startup dialog that may never come.
	DialogCeiling time.Duration
	// ConfirmCeiling bounds the wait for the goal to show up on screen.
	ConfirmCeiling time.Duration
	// Poll is the gap between two reads of the pane.
	Poll time.Duration
	// ReadLines is how much of the pane each read asks for; zero means 200.
	ReadLines int
	// Sleep is time.Sleep unless a test replaces it.
	Sleep func(time.Duration)
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

// Run brings up one worker and returns the pane it lives in. Every failure
// after the pane exists retires that pane: a half-built worker is worse than
// none, because the board would see a pane that never claims.
func (p *Pipeline) Run(ctx context.Context, req Request) (string, error) {
	agentArgs := req.Profile.AgentArgs()

	// Step zero for the codex provider, before anything is built: the proxy
	// publishes the client-policy half, and a down daemon says so here.
	if req.Profile.Provider == config.ProviderCodex {
		if req.Profile.HasSettingsArg() {
			return "", fmt.Errorf("spawn %s: the profile already carries --settings, which the codex provider must splice itself; the client keeps only the last of two and drops the first without saying so", req.Name)
		}
		settings, err := p.Proxy.Settings(ctx)
		if err != nil {
			return "", fmt.Errorf("spawn %s: %w", req.Name, err)
		}
		agentArgs = append([]string{"--settings", settings}, agentArgs...)
	}

	// Claude Code takes its initial prompt positionally, and the goal is the
	// whole of it.
	agentArgs = append(agentArgs, GoalPrefix+req.Goal)

	pane, err := p.Herdr.PaneSplit(ctx, req.BasePane, p.direction(), req.Cwd)
	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", req.Name, err)
	}

	if err := p.build(ctx, req, pane, agentArgs); err != nil {
		if closeErr := p.Herdr.PaneClose(ctx, pane); closeErr != nil {
			return "", fmt.Errorf("%w (and the pane could not be retired: %v)", err, closeErr)
		}
		return "", err
	}
	return pane, nil
}

func (p *Pipeline) build(ctx context.Context, req Request, pane string, agentArgs []string) error {
	// The environment half of the codex launch belongs to the pane's shell,
	// which the agent then inherits as a direct child.
	if req.Profile.Provider == config.ProviderCodex {
		if err := p.Herdr.PaneRun(ctx, pane, p.Proxy.EnvCommand()); err != nil {
			return fmt.Errorf("spawn %s: %w", req.Name, err)
		}
	}

	_, err := p.Herdr.AgentStart(ctx, herdr.StartRequest{
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
		return fmt.Errorf("spawn %s: %w", req.Name, err)
	}
	return nil
}

// answerStartupDialog watches for the trust-folder dialog and answers it once
// if it comes. A dialog that never appears is not a failure — the ceiling
// ends the wait, and no key is ever pressed on a screen that did not ask.
func (p *Pipeline) answerStartupDialog(ctx context.Context, pane string) error {
	for i, n := 0, attempts(p.DialogCeiling, p.Poll); i < n; i++ {
		text, err := p.Herdr.PaneRead(ctx, pane, p.readLines())
		if err != nil {
			return err
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
func (p *Pipeline) confirmGoal(ctx context.Context, pane string) error {
	var last string
	for i, n := 0, attempts(p.ConfirmCeiling, p.Poll); i < n; i++ {
		text, err := p.Herdr.PaneRead(ctx, pane, p.readLines())
		if err != nil {
			return err
		}
		if contains(text, GoalMarkers) {
			return nil
		}
		if strings.TrimSpace(text) != "" {
			last = text
		}
		if a, err := p.Herdr.AgentGet(ctx, pane); err == nil && a.Status == herdr.StatusWorking {
			return nil
		}
		p.sleep(p.Poll)
	}
	return fmt.Errorf("the goal never registered in pane %s within %s; the pane last showed: %s",
		pane, p.ConfirmCeiling, tail(last))
}

func (p *Pipeline) direction() string {
	if p.Direction != "" {
		return p.Direction
	}
	return "right"
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

func notReady(err error) bool {
	var herr *herdr.Error
	if !errors.As(err, &herr) {
		return false
	}
	return startCodesMeaningNotReady[herr.Code]
}
