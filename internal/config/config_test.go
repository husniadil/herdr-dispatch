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
