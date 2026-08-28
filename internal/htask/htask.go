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
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Principal is who the dispatcher acts as on the board when it has no pane
// of its own to name.
const Principal = "plugin:hdis"

// PrincipalFor is the principal a daemon running in the given pane writes
// with. The pane is what tells one daemon from another: a row the board is
// holding for `plugin:hdis@wM:p1` was reserved by the daemon in that pane,
// and a row held for any other suffix belongs to a peer that may well still
// be alive. §3.2 accepts any plugin principal that is one printable word.
func PrincipalFor(pane string) string {
	if pane == "" {
		return Principal
	}
	return Principal + "@" + pane
}

// Client runs the htask CLI.
type Client struct {
	// Bin is the binary to run; empty means `htask` off PATH.
	Bin string
	// Principal is who this client acts as; empty means the bare plugin
	// principal, which is a daemon with no pane to name.
	Principal string
}

func (c *Client) principal() string {
	if c.Principal != "" {
		return c.Principal
	}
	return Principal
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "htask"
}

// Task is the slice of a board row the dispatcher reads.
type Task struct {
	ID      string `json:"id"`
	Seq     int    `json:"seq"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	// Priority is the row's own number, which the board keeps as a
	// descriptive fact. The dispatcher ROUTES on it — which profile a
	// priority earns is this binary's config — and never writes it.
	Priority  int    `json:"priority"`
	ClaimedBy string `json:"claimed_by"`
	// PaneID is the pane the task was created from, or empty when nothing
	// with a pane created it. A task an operator files at a terminal has
	// none, and that is the ordinary case rather than a missing field.
	PaneID string `json:"pane_id"`
	// Feedback is what the review gate said when it sent the task back, and
	// it is on the row only between a rejection and the next submission: the
	// board clears it when the task is submitted again. A `doing` row that
	// carries it is a worker waiting on a rejection rather than a worker
	// that stopped without submitting, and nothing else here can tell those
	// two apart.
	Feedback string `json:"feedback"`
}

// Pane returns the pane the claiming principal runs in, or empty while the
// task is unclaimed or held by someone who is not an agent. htask names an
// agent claimant `agent:<pane id>`; the pane id is what is left when the
// prefix comes off.
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

// GetIn reads one task by the number a pane carries, inside one project.
//
// A number is unique only inside a project, which is exactly why the board
// refuses a bare number across every project — and that refusal is right, so
// the number is asked with the project it is unique in rather than widened
// until the board gives in. This is the only read the dispatcher scopes:
// everything it knows by id stays board-agnostic.
func (c *Client) GetIn(ctx context.Context, ref, project string) (Task, error) {
	if project == "" {
		return Task{}, fmt.Errorf("htask get %s: nothing names the project the number is unique in", ref)
	}
	var res struct {
		Task Task `json:"task"`
	}
	if err := c.json(ctx, &res, "get", ref, "--project", project, "--json"); err != nil {
		return Task{}, err
	}
	if res.Task.ID == "" {
		return Task{}, fmt.Errorf("htask get %s --project %s: no task in the response", ref, project)
	}
	return res.Task, nil
}

// Ready lists every unblocked, unclaimed todo task on every project. The
// dispatcher is not scoped to one repository; the board row names its own.
func (c *Client) Ready(ctx context.Context) ([]Task, error) {
	var page struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.json(ctx, &page, "list", "--ready", "--all-projects", "--json"); err != nil {
		return nil, err
	}
	return page.Tasks, nil
}

// Get reads one task by its id or its number, across every project. A task
// id belongs to the board, not to a repository, and the dispatcher is not
// scoped to one: the lookup looks exactly as wide as `task list --ready`
// already does, or a task filed on another project's board reads as a task
// that does not exist. `task get --json` answers with the row inside an
// envelope, where `task list` and `doctor` answer flat.
func (c *Client) Get(ctx context.Context, id string) (Task, error) {
	var res struct {
		Task Task `json:"task"`
	}
	if err := c.json(ctx, &res, "get", id, "--all-projects", "--json"); err != nil {
		return Task{}, err
	}
	if res.Task.ID == "" {
		return Task{}, fmt.Errorf("htask get %s: no task in the response", id)
	}
	return res.Task, nil
}

// Held lists every task on every project the board says this dispatcher's
// principal is holding, whatever its status. It is how a restart finds the
// reservations it left behind: the board is the one place a hold survives a
// process, and `--mine` is scoped to the principal, so a peer daemon's rows
// are never in the answer.
func (c *Client) Held(ctx context.Context) ([]Task, error) {
	var page struct {
		Tasks []Task `json:"tasks"`
	}
	if err := c.json(ctx, &page, "list", "--mine", "--all-projects", "--json"); err != nil {
		return nil, err
	}
	return page.Tasks, nil
}

// Release hands one task back with a note saying what is left.
//
// This is not the lease sweep and never becomes one: it names a single task
// this daemon's own principal is holding and nothing else. Pane-gone sweeps,
// the lease timer and the board's startup reconciliation stay htask's.
func (c *Client) Release(ctx context.Context, id, note string) error {
	_, err := c.run(ctx, "release", id, "--all-projects", "--note", note, "--json")
	return err
}

// Event is one entry of the BOARD's own trail, as this dispatcher reads it.
// It is htask's ledger and never copied into this one: the only reason to
// read it is to learn that somebody other than this daemon has acted on a
// row, which no board row itself records.
type Event struct {
	ID       string `json:"id"`
	Entity   string `json:"entity"`
	EntityID string `json:"entity_id"`
	Project  string `json:"project"`
	AtMS     int64  `json:"at"`
	Actor    string `json:"actor"`
	Kind     string `json:"kind"`
}

// Events reads the board's trail from a Unix millisecond, across every
// project.
//
// It is resumed rather than read whole on purpose: `htask events` with no
// --since starts at the beginning of everything the board still holds, and a
// daemon asking that on a tick would carry the board's whole history through
// a pipe for the sake of the handful of entries written since it last looked.
func (c *Client) Events(ctx context.Context, sinceMS int64) ([]Event, error) {
	var page struct {
		Events []Event `json:"events"`
	}
	if err := c.json(ctx, &page, "events", "--since", strconv.FormatInt(sinceMS, 10), "--all-projects", "--json"); err != nil {
		return nil, err
	}
	return page.Events, nil
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
	args = append(append([]string{}, args...), "--as", c.principal())
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = envWithoutPane(os.Environ())
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

// paneNames are the variables the board reads a caller's own position out of.
// They travel in the environment rather than in argv, so a child that inherits
// the daemon's environment arrives claiming to be the daemon's pane, on the
// daemon's board, no matter what argv declares.
//
// The first three are the pane. HERDR_PLUGIN_CONTEXT_JSON is the PROJECT:
// htask resolves its board from that document's focused-pane cwd before it
// falls back to the working directory (§4.2), and Herdr fills it in for the
// commands it spawns itself, this plugin's [[startup]] among them. A daemon
// started that way would scope every board call to whatever the operator was
// looking at when the plugin started — and a board scoped somewhere else
// looks exactly like a board with nothing ready, which is the failure that
// reports itself as nothing at all.
var paneNames = []string{
	"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_PLUGIN_CONTEXT_JSON",
}

// envWithoutPane is the daemon's environment minus where it happens to be
// running.
//
// Every call here declares a plugin principal, and a principal is what it
// declares INSTEAD of a pane: a call that carries both leaves the board
// unable to tell the dispatcher from an agent that claimed as plugin:hdis.
// The daemon keeps reading these names for its own purposes - the base pane
// it splits workers off - it just stops handing them to a child speaking as
// a plugin, and scopes each call with --project or --all-projects instead.
func envWithoutPane(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(paneNames, name) {
			continue
		}
		out = append(out, kv)
	}
	return out
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
