package mcpdoor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
)

// unstartableState is a state dir with no daemon in it that cannot be created
// either: its parent is a regular file, so os.MkdirAll refuses. Nothing here
// depends on WHY a start would fail — what it buys is a door that tries to
// start one failing loudly at that step instead of spawning this test binary
// as a daemon, which is what an autostart in a test package would otherwise
// do. It is what tells the two outcomes apart below: a verb that refuses
// before the start says NOT_RUNNING, and a verb that reaches the start says
// the state dir could not be made.
func unstartableState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvPrefix+"STATE_DIR", filepath.Join(blocked, "state"))
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", dir)
}

// envelope is the §6.2 failure document a tool error carries.
func envelope(t *testing.T, res *mcp.CallToolResult) (code, message string) {
	t.Helper()
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(res)), &doc); err != nil {
		t.Fatalf("failure envelope: %v (body %q)", err, text(res))
	}
	return doc.Error.Code, doc.Error.Message
}

// `stop` is the registry's NoAutostart verb, and that is a property of the
// verb rather than of the door it arrived at: starting a daemon just to ask it
// to go away leaves the operator with a process that ran a tick and a spawn on
// its way in. The CLI has refused this since the client grew NoStart; the MCP
// door built its client with the field at its zero and started one.
func TestStopThroughTheDoorDoesNotStartADaemon(t *testing.T) {
	unstartableState(t)
	sess := session(t, nil)

	res, err := sess.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "stop", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !res.IsError {
		t.Fatalf("stop answered with no daemon listening: %s", text(res))
	}
	code, message := envelope(t, res)
	if got, want := code, string(codes.Conflict); got != want {
		t.Errorf("envelope code = %q, want %q (%q)", got, want, message)
	}
	if !strings.Contains(message, "no hdis daemon is listening") {
		t.Errorf("the envelope is not the NOT_RUNNING refusal: %q", message)
	}
	// The refusal came before the start, not out of it: a door that reached
	// the start would be reporting the state dir it could not make.
	if strings.Contains(message, "state dir") {
		t.Errorf("stop reached the start path and failed there: %q", message)
	}
}

// The other half: every verb the registry does not mark still starts a daemon
// when none is listening, which is what makes the door usable from a cold
// machine. It gets as far as the start and fails there, on this state dir,
// rather than being refused before it.
func TestAnOrdinaryVerbThroughTheDoorStillStartsADaemon(t *testing.T) {
	unstartableState(t)
	sess := session(t, nil)

	res, err := sess.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !res.IsError {
		t.Fatalf("status answered with no daemon listening: %s", text(res))
	}
	_, message := envelope(t, res)
	if strings.Contains(message, "no hdis daemon is listening") {
		t.Fatalf("status was refused instead of starting a daemon: %q", message)
	}
	if !strings.Contains(message, "state dir") {
		t.Fatalf("status did not reach the start path: %q", message)
	}
}
