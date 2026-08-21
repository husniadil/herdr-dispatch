// Package herdr drives Herdr the same way the board is driven: shell out to
// `herdr <verb>`, never open its socket, and parse the JSON it answers with.
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
// operator's focus, and returns the new pane's id.
func (c *Client) PaneSplit(ctx context.Context, pane, direction, cwd string) (string, error) {
	var res struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	if err := c.result(ctx, &res, "pane", "split", "--pane", pane, "--direction", direction, "--cwd", cwd, "--no-focus"); err != nil {
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
func (c *Client) PaneRead(ctx context.Context, pane string, lines int) (string, error) {
	var res struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	err := c.result(ctx, &res, "pane", "read", pane, "--source", "detection", "--lines", strconv.Itoa(lines))
	return res.Read.Text, err
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

// Agents maps every live worker's pane to its agent_status. A bound pane
// missing from the map is a pane that is gone.
func (c *Client) Agents(ctx context.Context) (map[string]string, error) {
	var res struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.result(ctx, &res, "agent", "list"); err != nil {
		return nil, err
	}
	byPane := make(map[string]string, len(res.Agents))
	for _, a := range res.Agents {
		byPane[a.PaneID] = a.Status
	}
	return byPane, nil
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

// result runs a verb and unmarshals its `result` object into into, which may
// be nil when the verb answers with nothing worth reading.
func (c *Client) result(ctx context.Context, into any, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// A server refusal is a JSON error body on stderr with a non-zero exit.
	if runErr != nil {
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
