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

// The failure this exists for, measured on 2026-08-29: a worker given a vague
// task in the Herdr checkout, running in `bypassPermissions`, ran
// `herdr workspace close w3` — closing this daemon's own base pane, another
// task's live worker and a workspace, none of which the task asked for.
//
// A deny rule is the only rule shape that reaches a bypass-mode worker:
// Claude Code's own documentation says "Deny rules block in every mode,
// including bypassPermissions" and "Allow rules have no effect in
// bypassPermissions" (code.claude.com/docs/en/permission-modes, read
// 2026-08-29), and deny is evaluated before ask and allow at every scope.
func TestTheWorkerSettingsDenyTheCommandsThatTakeTheFleetDown(t *testing.T) {
	doc, err := WorkerSettings("")
	if err != nil {
		t.Fatalf("worker settings: %v", err)
	}
	var got struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("worker settings is not readable json: %v (%s)", err, doc)
	}
	have := make(map[string]bool, len(got.Permissions.Deny))
	for _, rule := range got.Permissions.Deny {
		have[rule] = true
	}
	// The command that did the damage, and the rest of the destructive half
	// of `herdr --help` on herdr 0.8.2.
	for _, want := range []string{
		"Bash(herdr workspace close*)",
		"Bash(herdr tab close*)",
		"Bash(herdr pane close*)",
		"Bash(herdr server*)",
		"Bash(herdr pane move*)",
		"Bash(herdr session stop*)",
		"Bash(herdr worktree remove*)",
	} {
		if !have[want] {
			t.Errorf("a worker is not denied %s: %v", want, got.Permissions.Deny)
		}
	}
	// Denied as a scoped rule and never as a bare tool name: a bare `Bash`
	// deny removes the shell from the worker's context entirely, and a
	// worker that cannot run a command cannot do the task either.
	if have["Bash"] || have["Bash(*)"] {
		t.Errorf("the whole Bash tool is denied, which leaves the worker unable to work: %v", got.Permissions.Deny)
	}
}

// The launcher's own document is passed through whole. A codex spawn's
// `proxenos settings` already carries a `permissions.deny` of its own, and
// dropping an entry of it here would silently undo a rule somebody else set.
func TestTheWorkerSettingsKeepEverythingTheLauncherSet(t *testing.T) {
	const launcher = `{"disableClaudeAiConnectors":true,"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"},"permissions":{"deny":["Skill(claude-api)"],"defaultMode":"bypassPermissions"}}`
	doc, err := WorkerSettings(launcher)
	if err != nil {
		t.Fatalf("worker settings: %v", err)
	}
	var got struct {
		DisableConnectors bool              `json:"disableClaudeAiConnectors"`
		Env               map[string]string `json:"env"`
		Permissions       struct {
			Deny        []string `json:"deny"`
			DefaultMode string   `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("worker settings is not readable json: %v (%s)", err, doc)
	}
	if !got.DisableConnectors || got.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:8787" {
		t.Errorf("the launcher's own document did not survive: %s", doc)
	}
	if got.Permissions.DefaultMode != "bypassPermissions" {
		t.Errorf("the launcher's permission mode was rewritten: %s", doc)
	}
	var kept bool
	for _, rule := range got.Permissions.Deny {
		if rule == "Skill(claude-api)" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the launcher's own deny rule was dropped: %v", got.Permissions.Deny)
	}
	if len(got.Permissions.Deny) != len(WorkerDeniedBash)+1 {
		t.Errorf("the two deny lists did not merge cleanly: %v", got.Permissions.Deny)
	}
}

// Composed twice, the same document: a rule already there is never added
// again, so a launcher that has since adopted one of these does not produce a
// list with it in twice.
func TestTheWorkerSettingsAreTheSameHoweverOftenTheyAreComposed(t *testing.T) {
	once, err := WorkerSettings(`{"permissions":{"deny":["Bash(herdr server*)"]}}`)
	if err != nil {
		t.Fatalf("worker settings: %v", err)
	}
	twice, err := WorkerSettings(once)
	if err != nil {
		t.Fatalf("worker settings: %v", err)
	}
	if once != twice {
		t.Fatalf("composing twice changed the document:\n%s\n%s", once, twice)
	}
}

// herdr refuses an agent argument containing a newline, and the path this
// document is written to travels on a line herdr TYPES into the pane.
func TestTheWorkerSettingsAreOneLine(t *testing.T) {
	doc, err := WorkerSettings("{\n  \"env\": {}\n}")
	if err != nil {
		t.Fatalf("worker settings: %v", err)
	}
	if strings.ContainsAny(doc, "\n\r") {
		t.Fatalf("the worker settings document was not compacted: %q", doc)
	}
}

// A launcher document that cannot be read is a refusal rather than a document
// composed without it: silently launching a worker with only half the policy
// somebody set is the failure this whole file is about.
func TestAnUnreadableLauncherDocumentRefusesRatherThanBeingDropped(t *testing.T) {
	for name, doc := range map[string]string{
		"not json":                  `not a document`,
		"permissions not an object": `{"permissions":[]}`,
		"deny not a list":           `{"permissions":{"deny":"everything"}}`,
	} {
		if _, err := WorkerSettings(doc); err == nil {
			t.Errorf("%s was composed rather than refused", name)
		}
	}
}
