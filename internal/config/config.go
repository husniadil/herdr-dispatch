// Package config is the dispatcher's own execution policy: the worker
// profiles a Spawn action is assembled from, and which profile a project
// gets. None of it belongs on the board — the ledger carries no profile
// field, deliberately, because which agent kind and model a worker gets is
// this binary's business and not a board fact.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Provider names how a worker is launched.
type Provider string

const (
	// ProviderClaude launches the plain binary.
	ProviderClaude Provider = "claude"
	// ProviderCodex launches through the proxy launcher named by Config's
	// Proxy field, which injects the routing environment and the settings
	// half. hdis carries no proxy logic of its own; it only runs the
	// launcher's two documented steps.
	ProviderCodex Provider = "codex"
)

// The defaults a document may leave out.
const (
	DefaultAgent  = "claude"
	DefaultEffort = "low"
	// DefaultProxy is the codex provider's launcher binary. It is a name the
	// config carries rather than a word compiled in here because that binary
	// has been renamed once already.
	DefaultProxy = "proxenos"
)

// Profile is the launch preset for one worker. Model is a tier alias, and an
// empty one means the CLI's own default rather than no model.
type Profile struct {
	Provider Provider `json:"provider"`
	Agent    string   `json:"agent"`
	Model    string   `json:"model"`
	Effort   string   `json:"effort"`
	Args     []string `json:"args"`
}

// Config is the whole document: named profiles, the one every project gets
// unless it says otherwise, and the projects that say otherwise.
type Config struct {
	Default  string             `json:"default"`
	Profiles map[string]Profile `json:"profiles"`
	Projects map[string]string  `json:"projects"`
	// Proxy is the codex provider's launcher binary, resolved off PATH
	// unless it is a path. Empty means DefaultProxy.
	Proxy string `json:"proxy"`
}

// Parse reads a config document and refuses one it could not resolve later.
func Parse(b []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("hdis config: %w", err)
	}

	if c.Proxy == "" {
		c.Proxy = DefaultProxy
	}
	if len(c.Profiles) == 0 {
		return Config{}, fmt.Errorf("hdis config: no profiles")
	}
	for name, p := range c.Profiles {
		switch p.Provider {
		case ProviderClaude, ProviderCodex:
		case "":
			return Config{}, fmt.Errorf("hdis config: profile %q names no provider", name)
		default:
			return Config{}, fmt.Errorf("hdis config: profile %q has provider %q, want %q or %q",
				name, p.Provider, ProviderClaude, ProviderCodex)
		}
		if p.Agent == "" {
			p.Agent = DefaultAgent
		}
		if p.Effort == "" {
			p.Effort = DefaultEffort
		}
		c.Profiles[name] = p
	}

	if c.Default == "" {
		return Config{}, fmt.Errorf("hdis config: no default profile")
	}
	if _, ok := c.Profiles[c.Default]; !ok {
		return Config{}, fmt.Errorf("hdis config: default profile %q is not defined", c.Default)
	}
	for project, name := range c.Projects {
		if _, ok := c.Profiles[name]; !ok {
			return Config{}, fmt.Errorf("hdis config: project %q names profile %q, which is not defined", project, name)
		}
	}
	return c, nil
}

// Load reads a config document from disk.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("hdis config %s: %w", path, err)
	}
	c, err := Parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ProfileFor resolves the profile a project's workers launch with: its own
// override if it has one, the global default otherwise.
func (c Config) ProfileFor(project string) (Profile, error) {
	name := c.Default
	if over, ok := c.Projects[project]; ok {
		name = over
	}
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("hdis config: profile %q is not defined", name)
	}
	return p, nil
}

// AgentArgs renders the profile as the argv herdr forwards to the worker
// after `--`. The goal is not in it: Claude Code takes its initial prompt
// positionally, so the caller appends it last.
func (p Profile) AgentArgs() []string {
	args := []string{"--agent", p.Agent}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "--effort", p.Effort)
	return append(args, p.Args...)
}

// HasSettingsArg reports whether the profile already carries a --settings of
// its own. The codex provider splices one in, and the client keeps only the
// last of two and drops the first without saying so.
func (p Profile) HasSettingsArg() bool {
	for _, a := range p.Args {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}
