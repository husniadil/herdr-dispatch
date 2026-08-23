package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirPrefersThePluginsOwnOverride(t *testing.T) {
	t.Setenv("DISPATCH_STATE_DIR", "/tmp/somewhere")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, want := StateDir(), "/tmp/somewhere"; got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirFallsBackToTheXdgBase(t *testing.T) {
	t.Setenv("DISPATCH_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, want := StateDir(), filepath.Join("/tmp/xdg", Name); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestSocketAndLockSitInTheStateDir(t *testing.T) {
	t.Setenv("DISPATCH_STATE_DIR", "/tmp/state")
	if got, want := SocketPath(), "/tmp/state/dispatch.sock"; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := LockPath(), "/tmp/state/dispatch.lock"; got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}
}

// §10.1 with §13.2: the config is TOML at <config_dir>/<name>.toml, where
// <name> is the plugin's SHORT NAME. `hdis` is the binary abbreviation §13.2
// leaves to each plugin, and it never names a directory: a policy author
// gating `dispatch.dispatch` and an operator opening
// ~/.config/dispatch/dispatch.toml are looking at the same plugin under the
// same word.
func TestTheConfigIsTomlUnderTheShortName(t *testing.T) {
	t.Setenv("DISPATCH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/cfg")
	if got, want := ConfigPath(), "/tmp/cfg/dispatch/dispatch.toml"; got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
	if Name != "dispatch" {
		t.Errorf("the short name is %q, and §13.2 fixes it at \"dispatch\"", Name)
	}
	if EnvPrefix != "DISPATCH_" {
		t.Errorf("the env prefix is %q, and §10.1 makes it the uppercase short name", EnvPrefix)
	}
}

// §3.5: the trust boundary is the local user account, and a plugin MUST create
// its state dir 0700. The socket in it is a door onto the operator's own panes,
// so a mode anyone can traverse hands that door to every account on the box.
//
// The mode is what this asserts. It called EnsureStateDir twice and checked
// only that neither returned an error, which is idempotence — a true thing,
// and not the one the name claims.
func TestEnsureStateDirMakesItPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("DISPATCH_STATE_DIR", dir)
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir %s is %04o, want 0700 (§3.5)", dir, got)
	}
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir twice: %v", err)
	}
	if got, _ := os.Stat(dir); got.Mode().Perm() != 0o700 {
		t.Errorf("the second call left the state dir %04o", got.Mode().Perm())
	}
}

// A state dir that already exists is the ordinary case — an earlier hdis, an
// XDG base another tool made — and MkdirAll leaves whatever mode it finds. The
// privacy of the socket in it cannot depend on who created the directory.
func TestEnsureStateDirClosesAnAlreadyOpenStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("DISPATCH_STATE_DIR", dir)
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("an existing state dir stayed %04o, want 0700 (§3.5)", got)
	}
}

// §5.1 and §10.1: a plugin MUST NOT resolve state_dir or config_dir from
// HERDR_PLUGIN_STATE_DIR / HERDR_PLUGIN_CONFIG_DIR. Herdr injects those only
// into what Herdr itself spawns — startup commands, actions, plugin panes —
// and into no managed pane, so honouring them would give one plugin two state
// dirs that never see each other's bindings, and a dispatcher that forgot half
// its workers on restart.
//
// Both names are set to somewhere that would be obvious in the answer, and the
// answer must not be either of them.
func TestTheHerdrPluginDirsAreNotRead(t *testing.T) {
	for _, tc := range []struct {
		env  string
		dir  func() string
		what string
	}{
		{"HERDR_PLUGIN_STATE_DIR", StateDir, "state dir (§5.1)"},
		{"HERDR_PLUGIN_CONFIG_DIR", ConfigDir, "config dir (§10.1)"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			injected := filepath.Join(t.TempDir(), "herdr-injected")
			own := filepath.Join(t.TempDir(), "own")
			t.Setenv(tc.env, injected)
			t.Setenv("DISPATCH_STATE_DIR", own)
			t.Setenv("DISPATCH_CONFIG_DIR", own)
			if got := tc.dir(); got == injected {
				t.Fatalf("the %s resolved to %s, which Herdr injected: §5.1 and §10.1 forbid reading %s",
					tc.what, got, tc.env)
			}
		})
	}
	// And with nothing of this plugin's own set either, so the injected value
	// cannot win by being the only candidate left.
	for _, tc := range []struct {
		env string
		dir func() string
	}{
		{"HERDR_PLUGIN_STATE_DIR", StateDir},
		{"HERDR_PLUGIN_CONFIG_DIR", ConfigDir},
	} {
		injected := filepath.Join(t.TempDir(), "herdr-injected")
		t.Setenv(tc.env, injected)
		t.Setenv("DISPATCH_STATE_DIR", "")
		t.Setenv("DISPATCH_CONFIG_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		if got := tc.dir(); got == injected {
			t.Errorf("with nothing else set, %s answered %s", tc.env, got)
		}
	}
}

func TestTheConfigCanNameTheBasePaneADaemonCannotInherit(t *testing.T) {
	c, err := Parse([]byte(`default = "worker"
pane = "wM:p1"
[profiles.worker]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := c.BasePaneOr(""), "wM:p1"; got != want {
		t.Errorf("BasePaneOr(\"\") = %q, want %q", got, want)
	}
	if got, want := c.BasePaneOr("wZ:p3"), "wZ:p3"; got != want {
		t.Errorf("the flag did not win: %q, want %q", got, want)
	}
}

func TestWithoutAConfiguredPaneThereIsNone(t *testing.T) {
	c, err := Parse([]byte(`default = "worker"
[profiles.worker]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := c.BasePaneOr(""); got != "" {
		t.Errorf("BasePaneOr(\"\") = %q, want empty", got)
	}
}
