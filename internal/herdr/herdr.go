// Package herdr drives Herdr the same way the board is driven: shell out to
// `herdr <verb>`, never open its socket, and read what it answers with — a
// JSON envelope for every verb but `pane read`, which answers with the
// terminal's own text.
// There is no detection logic here — Herdr's own agent_status is the only
// truth about a worker this repo accepts.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client runs the herdr CLI.
type Client struct {
	// Bin is the binary to run; empty means `herdr` off PATH.
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "herdr"
}

// Error is a refusal from the Herdr server, carrying the code the caller
// needs in order to tell one refusal from another.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message) }

// Agent is one worker as Herdr sees it.
type Agent struct {
	Name             string `json:"name"`
	PaneID           string `json:"pane_id"`
	Agent            string `json:"agent"`
	Status           string `json:"agent_status"`
	InteractiveReady bool   `json:"interactive_ready"`
}

// The agent_status values this repo acts on.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// PaneSplit opens a worker pane beside an existing one, without moving the
// operator's focus, and returns the new pane's id. Each env entry is a
// KEY=VALUE herdr sets for the process it launches in the new pane, which
// the agent started there inherits.
func (c *Client) PaneSplit(ctx context.Context, pane, direction, cwd string, env ...string) (string, error) {
	var res struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	argv := []string{"pane", "split", "--pane", pane, "--direction", direction, "--cwd", cwd, "--no-focus"}
	for _, e := range env {
		argv = append(argv, "--env", e)
	}
	if err := c.result(ctx, &res, argv...); err != nil {
		return "", err
	}
	if res.Pane.PaneID == "" {
		return "", fmt.Errorf("herdr pane split: no pane id in the response")
	}
	return res.Pane.PaneID, nil
}

// PaneRun sends a command line and Enter to a pane's interactive shell.
func (c *Client) PaneRun(ctx context.Context, pane, command string) error {
	return c.result(ctx, nil, "pane", "run", pane, command)
}

// PaneRead returns the pane's detection snapshot: the plain-text bottom
// buffer, which is where a startup dialog and a goal confirmation both show.
//
// This is the one verb whose answer is not a JSON envelope. `herdr pane read`
// writes the terminal's own text to stdout — box drawing, wrapping and all —
// so it is taken raw. Parsing it as JSON is what retired a live worker that
// had already claimed its task.
func (c *Client) PaneRead(ctx context.Context, pane string, lines int) (string, error) {
	return c.text(ctx, "pane", "read", pane, "--source", "detection", "--lines", strconv.Itoa(lines))
}

// PaneSendKeys presses logical keys in a pane.
func (c *Client) PaneSendKeys(ctx context.Context, pane string, keys ...string) error {
	return c.result(ctx, nil, append([]string{"pane", "send-keys", pane}, keys...)...)
}

// PaneClose closes a worker pane.
func (c *Client) PaneClose(ctx context.Context, pane string) error {
	return c.result(ctx, nil, "pane", "close", pane)
}

// StartRequest is one `agent start`: a named agent of some kind, in a pane
// that is already at its shell prompt, with the profile's argv forwarded to
// the agent after the separator.
type StartRequest struct {
	Name      string
	Kind      string
	Pane      string
	Timeout   time.Duration
	AgentArgs []string
}

// AgentStart starts a worker. Its error is handed back whole, code included:
// for a goal delivered in the initial argv, `timeout` is the ordinary answer
// and means the worker went to work rather than that the dispatch failed.
// Deciding that is the spawn pipeline's job, not this adapter's.
func (c *Client) AgentStart(ctx context.Context, req StartRequest) (Agent, error) {
	args := []string{"agent", "start", req.Name, "--kind", req.Kind, "--pane", req.Pane}
	if req.Timeout > 0 {
		args = append(args, "--timeout", strconv.FormatInt(req.Timeout.Milliseconds(), 10))
	}
	if len(req.AgentArgs) > 0 {
		args = append(args, "--")
		args = append(args, req.AgentArgs...)
	}
	var res struct {
		Agent Agent `json:"agent"`
	}
	err := c.result(ctx, &res, args...)
	return res.Agent, err
}

// AgentGet reads one worker by pane id or by agent name.
func (c *Client) AgentGet(ctx context.Context, target string) (Agent, error) {
	var res struct {
		Agent Agent `json:"agent"`
	}
	err := c.result(ctx, &res, "agent", "get", target)
	return res.Agent, err
}

// Panes maps every live pane to its agent_status. A bound pane missing from
// the map is a pane that is gone.
//
// It reads `pane list` rather than `agent list`, and the difference is the
// whole reason this exists. A pane herdr has not attached an agent to yet —
// a worker pane in the seconds between `pane split` and the agent
// registering — is listed here with agent_status "unknown", and is absent
// from `agent list` altogether. Reading that absence as "the pane is gone"
// unbound a live worker and let its task be dispatched a second time.
func (c *Client) Panes(ctx context.Context) (map[string]string, error) {
	var res struct {
		Panes []Agent `json:"panes"`
	}
	if err := c.result(ctx, &res, "pane", "list"); err != nil {
		return nil, err
	}
	byPane := make(map[string]string, len(res.Panes))
	for _, p := range res.Panes {
		byPane[p.PaneID] = p.Status
	}
	return byPane, nil
}

// Agents lists every agent Herdr has registered, with the name it was
// registered under. `pane list` says which panes are alive but not who is in
// them; the name is the only place a worker's task number survives a
// restart, so this is what says which live panes are this daemon's.
//
// An agent whose pane has already gone is still listed here until Herdr
// notices, so the answer is only ever used against `pane list`.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var res struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.result(ctx, &res, "agent", "list"); err != nil {
		return nil, err
	}
	return res.Agents, nil
}

// AgentPrompt submits a prompt to a worker. It carries a nudge and never a
// goal: a slash command long enough to be a goal cannot arrive this way.
func (c *Client) AgentPrompt(ctx context.Context, target, text string) error {
	return c.result(ctx, nil, "agent", "prompt", target, text)
}

// Notify puts a message in front of the operator.
func (c *Client) Notify(ctx context.Context, title, body string) error {
	return c.result(ctx, nil, "notification", "show", title, "--body", body)
}

// text runs a verb whose answer is the terminal's own output rather than a
// JSON envelope, and hands back stdout whole.
func (c *Client) text(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if runErr := cmd.Run(); runErr != nil {
		return "", refusal(runErr, args, stderr)
	}
	return stdout.String(), nil
}

// result runs a verb and unmarshals its `result` object into into, which may
// be nil when the verb answers with nothing worth reading.
func (c *Client) result(ctx context.Context, into any, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if runErr := cmd.Run(); runErr != nil {
		return refusal(runErr, args, stderr)
	}

	if into == nil {
		return nil
	}
	var body struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &body); err != nil {
		return fmt.Errorf("herdr %s: unreadable json: %w", strings.Join(args, " "), err)
	}
	if err := json.Unmarshal(body.Result, into); err != nil {
		return fmt.Errorf("herdr %s: unreadable result: %w", strings.Join(args, " "), err)
	}
	return nil
}

// refusal reads what herdr said when a verb exited non-zero: a JSON error
// body on stderr, carrying the code the caller needs to tell one refusal
// from another, or failing that whatever it wrote there instead.
func refusal(runErr error, args []string, stderr bytes.Buffer) error {
	var body struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stderr.Bytes()), &body); err == nil && body.Error != nil {
		return fmt.Errorf("herdr %s: %w", strings.Join(args, " "), body.Error)
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("herdr %s: %s", strings.Join(args, " "), msg)
	}
	return fmt.Errorf("herdr %s: %w", strings.Join(args, " "), runErr)
}
