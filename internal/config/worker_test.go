package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The top-level table is the fleet-wide answer: every worker of every profile
// reads its doors from the file it names.
func TestWorkerMCPConfigIsReadFromTheTopLevelTable(t *testing.T) {
	const doc = `
default = "worker"

[worker]
mcp_config = "/etc/hdis/worker.mcp.json"

[profiles.worker]
provider = "claude"
`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := c.Worker.MCPConfig, "/etc/hdis/worker.mcp.json"; got != want {
		t.Fatalf("worker.mcp_config: got %q, want %q", got, want)
	}
	p, err := c.ProfileFor("/src/anything")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got, want := c.MCPConfigFor(p), "/etc/hdis/worker.mcp.json"; got != want {
		t.Fatalf("resolved: got %q, want %q", got, want)
	}
}

// A profile may say otherwise, and its own path is what its workers get. A
// profile that says nothing still gets the fleet's.
func TestAProfileMCPConfigOverridesTheTopLevelOne(t *testing.T) {
	const doc = `
default = "worker"

[worker]
mcp_config = "/etc/hdis/fleet.mcp.json"

[profiles.worker]
provider = "claude"

[profiles.narrow]
provider = "claude"
mcp_config = "/etc/hdis/narrow.mcp.json"
`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	narrow, err := c.ProfileNamed("narrow")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got, want := c.MCPConfigFor(narrow), "/etc/hdis/narrow.mcp.json"; got != want {
		t.Fatalf("overriding profile: got %q, want %q", got, want)
	}
	plain, err := c.ProfileNamed("worker")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got, want := c.MCPConfigFor(plain), "/etc/hdis/fleet.mcp.json"; got != want {
		t.Fatalf("silent profile: got %q, want %q", got, want)
	}
}

// Neither table says anything, so nothing is configured and the default file
// is what a spawn writes and uses.
func TestNoMCPConfigAnywhereResolvesToNothingConfigured(t *testing.T) {
	const doc = `
default = "worker"

[profiles.worker]
provider = "claude"
`
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, err := c.ProfileFor("/src/anything")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got := c.MCPConfigFor(p); got != "" {
		t.Fatalf("resolved: got %q, want the empty string", got)
	}
}

// An empty path is not an unconfigured one: it is a file nobody can read, and
// a worker launched against it would have no doors at all. The operator who
// meant the default deletes the key.
func TestAnEmptyMCPConfigIsRefused(t *testing.T) {
	for _, doc := range []string{
		"default = \"worker\"\n\n[worker]\nmcp_config = \"\"\n\n[profiles.worker]\nprovider = \"claude\"\n",
		"default = \"worker\"\n\n[profiles.worker]\nprovider = \"claude\"\nmcp_config = \"\"\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil {
			t.Fatalf("an empty mcp_config was accepted:\n%s", doc)
		}
		if !strings.Contains(err.Error(), "mcp_config") {
			t.Fatalf("the refusal does not name the key: %v", err)
		}
	}
}

// The default document is exactly the four plugin doors, each a stdio server
// running that plugin's own `mcp` verb.
func TestTheDefaultWorkerMCPDocumentHoldsTheFourDoors(t *testing.T) {
	b, err := RenderWorkerMCPConfig(func(string) (string, error) { return "", exec.ErrNotFound })
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]string{
		"herdr-tasks":    "htask",
		"herdr-mail":     "hmail",
		"herdr-dispatch": "hdis",
		"herdr-sched":    "hsched",
	}
	if len(doc.Servers) != len(want) {
		t.Fatalf("the document holds %d servers, want %d: %s", len(doc.Servers), len(want), b)
	}
	for name, command := range want {
		got, ok := doc.Servers[name]
		if !ok {
			t.Fatalf("door %q is missing: %s", name, b)
		}
		if got.Type != "stdio" {
			t.Errorf("door %q is %q, want stdio", name, got.Type)
		}
		if got.Command != command {
			t.Errorf("door %q runs %q, want the bare %q when PATH holds nothing", name, got.Command, command)
		}
		if !reflect.DeepEqual(got.Args, []string{"mcp"}) {
			t.Errorf("door %q takes args %v, want [mcp]", name, got.Args)
		}
	}
}

// A command PATH can resolve travels as its absolute path: a worker's pane is
// opened in a worktree under the state directory, where a plugin binary kept
// in a project's own bin/ is not on PATH.
func TestTheDefaultWorkerMCPDocumentUsesAbsolutePathsWhenPATHHasThem(t *testing.T) {
	b, err := RenderWorkerMCPConfig(func(name string) (string, error) {
		if name == "htask" {
			return "/opt/fleet/bin/htask", nil
		}
		return "", exec.ErrNotFound
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(b), `"/opt/fleet/bin/htask"`) {
		t.Fatalf("the resolved command is not absolute: %s", b)
	}
	if !strings.Contains(string(b), `"hmail"`) {
		t.Fatalf("an unresolvable command must stay bare: %s", b)
	}
}

// The file is written once. A second spawn finds it and leaves it exactly as
// it is, because an operator who edited the doors edited them on purpose.
func TestTheDefaultWorkerMCPFileIsWrittenOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.mcp.json")
	if err := EnsureWorkerMCPConfig(path); err != nil {
		t.Fatalf("first write: %v", err)
	}
	edited := []byte(`{"mcpServers":{}}`)
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := EnsureWorkerMCPConfig(path); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(edited) {
		t.Fatalf("the file was rewritten: %s", got)
	}
}

// The default path is the plugin's own state directory (§5.1), beside the
// bindings and the log.
func TestTheDefaultWorkerMCPPathIsInTheStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvPrefix+"STATE_DIR", dir)
	if got, want := WorkerMCPConfigPath(), filepath.Join(dir, "worker.mcp.json"); got != want {
		t.Fatalf("path: got %q, want %q", got, want)
	}
}

// A file that could not be written is said so, naming the path: a worker
// launched against doors that are not there has none at all.
func TestEnsureWorkerMCPConfigReportsAPathItCannotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "worker.mcp.json")
	err := EnsureWorkerMCPConfig(path)
	if err == nil {
		t.Fatal("a path with no directory under it must not be reported as written")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("the error does not name the path: %v", err)
	}
}
