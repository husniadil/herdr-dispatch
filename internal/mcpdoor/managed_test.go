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

// This door is the one that autostarted the orphan: a gateway holds `hdis mcp`
// open, a hub status read arrives while the service's daemon is down, and the
// door starts one under its own environment. New(call: nil) is that door — the
// default caller is the only autostart call site the MCP surface has.
func markedDoor(t *testing.T, manager string) *mcp.ClientSession {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvPrefix+"STATE_DIR", dir)
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, config.ManagedFile), []byte(manager+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return session(t, nil)
}

func TestTheDoorsOwnCallerRefusesToStartAManagedDaemon(t *testing.T) {
	sess := markedDoor(t, "dev.herdr.hdis")

	res, err := sess.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !res.IsError {
		t.Fatalf("status answered with no daemon and the marker in place: %s", text(res))
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(res)), &envelope); err != nil {
		t.Fatalf("failure envelope: %v", err)
	}
	if got, want := envelope.Error.Code, string(codes.Conflict); got != want {
		t.Errorf("envelope code = %q, want %q", got, want)
	}
	for what, want := range map[string]string{
		"the manager":  "dev.herdr.hdis",
		"the marker":   config.ManagedFile,
		"the way back": "restart the service",
	} {
		if !strings.Contains(envelope.Error.Message, want) {
			t.Errorf("the envelope does not carry %s (looked for %q): %q", what, want, envelope.Error.Message)
		}
	}
}

// doctor is the exception on this door too: it reports the manager rather
// than refusing, because a caller asking whether the dispatcher is up has an
// answer even when nothing is listening.
func TestTheDoorsDoctorReportsTheManagerInsteadOfRefusing(t *testing.T) {
	sess := markedDoor(t, "dev.herdr.hdis")

	res, err := sess.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "doctor", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if res.IsError {
		t.Fatalf("doctor refused under the marker: %s", text(res))
	}
	var rep struct {
		Managed         string `json:"managed"`
		DaemonAnswering bool   `json:"daemon_answering"`
	}
	if err := json.Unmarshal([]byte(text(res)), &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.DaemonAnswering {
		t.Error("the door says a daemon answered, and none is listening")
	}
	if got, want := rep.Managed, "dev.herdr.hdis"; got != want {
		t.Errorf("managed = %q, want %q", got, want)
	}
}
