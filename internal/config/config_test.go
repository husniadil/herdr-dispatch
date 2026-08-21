package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A config carrying a global default and a per-project override resolves the
// right profile for each project, and fills in the two documented defaults.
func TestProfileResolvesGlobalDefaultAndProjectOverride(t *testing.T) {
	const doc = `{
	  "default": "worker",
	  "profiles": {
	    "worker": {"provider": "claude"},
	    "routed": {
	      "provider": "codex",
	      "agent": "implementer",
	      "model": "sonnet",
	      "effort": "high",
	      "args": ["--verbose"]
	    }
	  },
	  "projects": {"/src/herdr-dispatch": "routed"}
	}`

	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := c.ProfileFor("/src/somewhere-else")
	if err != nil {
		t.Fatalf("default profile: %v", err)
	}
	want := Profile{Provider: ProviderClaude, Agent: DefaultAgent, Effort: DefaultEffort}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default profile: got %+v, want %+v", got, want)
	}

	got, err = c.ProfileFor("/src/herdr-dispatch")
	if err != nil {
		t.Fatalf("override profile: %v", err)
	}
	want = Profile{
		Provider: ProviderCodex,
		Agent:    "implementer",
		Model:    "sonnet",
		Effort:   "high",
		Args:     []string{"--verbose"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override profile: got %+v, want %+v", got, want)
	}
}

// A profile becomes the argv herdr forwards after `--`: the agent name always,
// the model only when the profile names one, the effort always, then the
// profile's own extra arguments, in that order.
func TestAgentArgsRendersTheProfile(t *testing.T) {
	p := Profile{Provider: ProviderClaude, Agent: "claude", Effort: "low"}
	if got, want := p.AgentArgs(), []string{"--agent", "claude", "--effort", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bare profile: got %v, want %v", got, want)
	}

	p = Profile{Provider: ProviderClaude, Agent: "reviewer", Model: "opus", Effort: "high", Args: []string{"--add-dir", "/tmp"}}
	want := []string{"--agent", "reviewer", "--model", "opus", "--effort", "high", "--add-dir", "/tmp"}
	if got := p.AgentArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("full profile: got %v, want %v", got, want)
	}
}

// The codex provider splices in its own --settings, so a profile that already
// carries one has to be caught before the two collide.
func TestHasSettingsArgSeesBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--settings", "{}"},
		{"--settings={}"},
		{"--verbose", "--settings", "/tmp/s.json"},
	} {
		if !(Profile{Args: args}).HasSettingsArg() {
			t.Fatalf("%v: want detected", args)
		}
	}
	if (Profile{Args: []string{"--verbose", "--settings-dir", "/tmp"}}).HasSettingsArg() {
		t.Fatal("--settings-dir is not --settings")
	}
}

// Every way a config can be wrong is refused at parse time, named.
func TestParseRefusesAConfigItCannotResolve(t *testing.T) {
	cases := map[string]string{
		"unknown provider":     `{"default":"a","profiles":{"a":{"provider":"gemini"}}}`,
		"missing provider":     `{"default":"a","profiles":{"a":{}}}`,
		"default names no one": `{"default":"b","profiles":{"a":{"provider":"claude"}}}`,
		"project names no one": `{"default":"a","profiles":{"a":{"provider":"claude"}},"projects":{"/p":"b"}}`,
		"no default":           `{"profiles":{"a":{"provider":"claude"}}}`,
		"no profiles":          `{"default":"a"}`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

// Load reads the same document off disk and says which file it could not read.
func TestLoadReportsThePathItFailedOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hdis.json")
	const doc = `{"default":"a","profiles":{"a":{"provider":"claude"}}}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := c.ProfileFor("/anywhere"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	missing := filepath.Join(dir, "absent.json")
	_, err = Load(missing)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("want the path in the error, got %v", err)
	}
}

// The codex provider's launcher is a name the config carries, not a word
// compiled into this binary. It has been renamed once already.
func TestTheProxyLauncherDefaultsToProxenosAndCanBeOverridden(t *testing.T) {
	if DefaultProxy != "proxenos" {
		t.Fatalf("the default launcher is %q", DefaultProxy)
	}

	c, err := Parse([]byte(`{"default":"a","profiles":{"a":{"provider":"codex"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Proxy; got != DefaultProxy {
		t.Fatalf("a config that names no launcher must resolve to %q, got %q", DefaultProxy, got)
	}

	c, err = Parse([]byte(`{"default":"a","proxy":"/opt/bin/proxenos-next","profiles":{"a":{"provider":"codex"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Proxy, "/opt/bin/proxenos-next"; got != want {
		t.Fatalf("the override was dropped: got %q, want %q", got, want)
	}
}

// The verification lane is off in a document that says nothing about it.
func TestTheVerificationLaneIsOffUnlessTheDocumentTurnsItOn(t *testing.T) {
	c, err := Parse([]byte(`{"default":"w","profiles":{"w":{"provider":"claude"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Verify.Enabled {
		t.Fatal("the lane came on without being asked for")
	}
	if _, err := c.VerifyProfile(); err == nil {
		t.Fatal("a profile was resolved for a lane that is off")
	}
}

// On, the lane names a profile of the same shape a worker's is.
func TestTheVerificationLaneNamesItsOwnProfile(t *testing.T) {
	c, err := Parse([]byte(`{"default":"w","profiles":{"w":{"provider":"claude"},"v":{"provider":"claude","model":"sonnet","effort":"high"}},"verify":{"enabled":true,"profile":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.VerifyProfile()
	if err != nil {
		t.Fatalf("verify profile: %v", err)
	}
	if p.Model != "sonnet" || p.Effort != "high" {
		t.Fatalf("verifier profile: %+v", p)
	}
}

// A lane turned on with nothing to launch is refused at parse, not at the
// first review that would have needed it.
func TestAVerificationLaneWithNoProfileIsRefused(t *testing.T) {
	for _, doc := range []string{
		`{"default":"w","profiles":{"w":{"provider":"claude"}},"verify":{"enabled":true}}`,
		`{"default":"w","profiles":{"w":{"provider":"claude"}},"verify":{"enabled":true,"profile":"nope"}}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Fatalf("accepted %s", doc)
		}
	}
}
