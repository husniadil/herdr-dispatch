// Package htask is the board adapter. It shells out to `htask <verb> --json`
// exactly as the htask README's "Driving htask from another program" section
// declares that surface, and never opens the daemon's socket. Every board
// fact the dispatcher acts on is read through here; none of them are kept.
package htask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Principal is who the dispatcher acts as on the board.
const Principal = "plugin:hdis"

// Client runs the htask CLI.
type Client struct {
	// Bin is the binary to run; empty means `htask` off PATH.
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "htask"
}

// Task is the slice of a board row the dispatcher reads.
type Task struct {
	ID        string `json:"id"`
	Seq       int    `json:"seq"`
	Project   string `json:"project"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	ClaimedBy string `json:"claimed_by"`
}

// Pane returns the pane the claiming principal runs in, or empty while the
// task is unclaimed or held by someone who is not an agent. htask names an
// agent claimant `agent:<workspace>:<pane>`; the pane id is what is left when
// the prefix comes off.
func (t Task) Pane() string {
	pane, ok := strings.CutPrefix(t.ClaimedBy, "agent:")
	if !ok {
		return ""
	}
	return pane
}

// Refusal is the board answering that it will not do something, in its own
// words. htask writes it as a JSON error envelope on stdout with a non-zero
// exit; a call that fails without one did not reach a board that answered,
// and carries no Refusal.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// Doctor is the board's own report of itself: enough to say at startup
// whether the dispatcher can work at all, and in whose words it cannot.
type Doctor struct {
	Version        string `json:"version"`
	Contract       string `json:"contract"`
	Binary         string `json:"binary"`
	Project        string `json:"project"`
	Principal      string `json:"principal"`
	SocketLive     bool   `json:"socket_live"`
	HerdrReachable bool   `json:"herdr_reachable"`
	LeaseSeconds   int    `json:"lease_seconds"`
}

// Doctor reports the board's version, dirs, socket and anything degraded.
func (c *Client) Doctor(ctx context.Context) (Doctor, error) {
	var d Doctor
	err := c.json(ctx, &d, "doctor", "--json")
	return d, err
}

// Ready lists every unblocked, unclaimed todo task on every project. The
// dispatcher is not scoped to one repository; the board row names its own.
func (c *Client) Ready(ctx context.Context) ([]Task, error) {
	var page struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.json(ctx, &page, "task", "list", "--ready", "--all-projects", "--json"); err != nil {
		return nil, err
	}
	return page.Tasks, nil
}

// Get reads one task by its id or its number. `task get --json` answers with
// the row inside an envelope, where `task list` and `doctor` answer flat.
func (c *Client) Get(ctx context.Context, id string) (Task, error) {
	var res struct {
		Task Task `json:"task"`
	}
	if err := c.json(ctx, &res, "task", "get", id, "--json"); err != nil {
		return Task{}, err
	}
	if res.Task.ID == "" {
		return Task{}, fmt.Errorf("htask task get %s: no task in the response", id)
	}
	return res.Task, nil
}

func (c *Client) json(ctx context.Context, into any, args ...string) error {
	out, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, into); err != nil {
		return fmt.Errorf("htask %s: unreadable json: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	args = append(append([]string{}, args...), "--as", Principal)
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		line := "htask " + strings.Join(args, " ")
		if refusal, ok := refusalIn(stdout.Bytes()); ok {
			return nil, fmt.Errorf("%s: %w", line, refusal)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s: %s", line, msg)
		}
		return nil, fmt.Errorf("%s: %w", line, err)
	}
	return stdout.Bytes(), nil
}

// refusalIn reads the board's error envelope out of a failed call's stdout.
// Anything else — a door that could not parse its own flags, a binary that
// is not there — leaves nothing to read, and is not the board speaking.
func refusalIn(out []byte) (*Refusal, bool) {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &body); err != nil || body.Error.Code == "" {
		return nil, false
	}
	return &Refusal{Code: body.Error.Code, Message: body.Error.Message}, true
}
