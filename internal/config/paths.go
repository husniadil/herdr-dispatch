package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name is this plugin's SHORT NAME (§13.2), which is what the contract names
// the directories, the socket, the lock and the config document after — not
// the binary abbreviation `hdis`, which §13.2 leaves to each plugin and which
// only ever names the executable. The two are different words here on
// purpose: a policy that gates `dispatch.dispatch` and a config at
// `~/.config/dispatch/dispatch.toml` are the same plugin under the same name,
// and a sibling reading the contract can find both without knowing what this
// binary happens to be called.
const Name = "dispatch"

// EnvPrefix is this plugin's own override prefix (§10.1: the uppercase short
// name), and how a test isolates its daemon from the operator's.
const EnvPrefix = "DISPATCH_"

// StateDir is DISPATCH_STATE_DIR, else
// ${XDG_STATE_HOME:-~/.local/state}/dispatch (§5.1).
// The socket, the lock, the log and the bindings live there. No board fact
// does: the bindings are the dispatcher's only state, and they are the one
// thing about them that is not derivable from the board or from Herdr.
func StateDir() string {
	dir := dirFrom(EnvPrefix+"STATE_DIR", "XDG_STATE_HOME", filepath.Join(".local", "state"))
	guardOperatorStateDir(dir)
	return dir
}

// realHome is the operator's own home directory as the process found it,
// captured at package init — before any test can move HOME — because that is
// the only moment it is still knowable.
var realHome = func() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}()

// underTest reports whether this binary is a `go test` binary. The flag the
// testing package registers before it runs anything is the evidence: importing
// `testing` into a production package to ask the question outright would put
// the test harness into the shipped binary.
func underTest() bool { return flag.Lookup("test.v") != nil }

// guardOperatorStateDir stops a TEST resolving the operator's own state
// directory, and it is the whole of what keeps a suite off the live fleet.
//
// Everything this daemon writes lives under StateDir — the bindings, the
// worktrees a worker is checked out into, the default worker MCP document, the
// log, the socket and the lock — so one guard here covers all of them. Nothing
// in a test names those paths: config.WorkerMCPConfigPath() reads the
// environment wherever it is called from, and on 2026-08-29 a spawn case that
// mentioned no path at all wrote the operator's live
// ~/.local/state/dispatch/worker.mcp.json with a `htask` door pointing at a
// fake binary in a t.TempDir the test then deleted. Every worker spawned on
// that machine afterwards had a dead tasks door.
//
// A panic rather than a returned error, and only ever inside a test binary: it
// is a programming error in the SUITE — a package with no
// testenv.RunIsolated — and it must fail the run rather than be handled. A
// shipped hdis never reaches it.
func guardOperatorStateDir(dir string) {
	if !underTest() || realHome == "" || !underRoot(dir, realHome) {
		return
	}
	panic(fmt.Sprintf("config.StateDir() resolved to %s, inside the operator's own home %s, "+
		"from a test binary: a test that wrote there would edit the live fleet's state. "+
		"Give the package a TestMain that calls testenv.RunIsolated, or set %sSTATE_DIR "+
		"or XDG_STATE_HOME to a temporary directory for the case.",
		dir, realHome, EnvPrefix))
}

// underRoot says whether dir is root or sits inside it. The separator is what
// makes it a directory test rather than a string test.
func underRoot(dir, root string) bool {
	root = strings.TrimSuffix(root, string(os.PathSeparator))
	if root == "" {
		return false
	}
	return dir == root || strings.HasPrefix(dir, root+string(os.PathSeparator))
}

// ConfigDir is DISPATCH_CONFIG_DIR, else
// ${XDG_CONFIG_HOME:-~/.config}/dispatch (§10.1).
func ConfigDir() string {
	return dirFrom(EnvPrefix+"CONFIG_DIR", "XDG_CONFIG_HOME", ".config")
}

// SocketPath is <state_dir>/dispatch.sock (§2.2): the private door every
// client of the daemon dials.
func SocketPath() string { return filepath.Join(StateDir(), Name+".sock") }

// LockPath is <state_dir>/dispatch.lock, the file whose flock elects the one
// daemon. It is held for the daemon's lifetime and released by the kernel
// when the process ends, so a crash leaves nothing to clean up.
func LockPath() string { return filepath.Join(StateDir(), Name+".lock") }

// ConfigPath is <config_dir>/dispatch.toml (§10.1), where the profiles live.
func ConfigPath() string { return filepath.Join(ConfigDir(), Name+".toml") }

// BindingsPath is <state_dir>/dispatch-bindings.json, the pane-to-task mapping
// the dispatcher re-adopts at start. It is the plugin's own state dir, per
// §5.1, and a JSON document rather than the section's SQLite file for the
// reason the README records.
func BindingsPath() string { return filepath.Join(StateDir(), Name+"-bindings.json") }

// WorktreeDir is <state_dir>/worktrees, where a worker's checkout is made. It
// is outside every project on purpose: the tree a worker edits and commits in
// must not be one the operator or another worker is holding.
func WorktreeDir() string { return filepath.Join(StateDir(), "worktrees") }

// ManagedFile is the marker a service manager writes beside the socket and the
// lock to say that starting this daemon is ITS job. It is named after nothing
// this plugin owns on purpose: an operator wiring hdis into launchd or systemd
// writes one file with a name it can guess and never has to know a config key.
const ManagedFile = "managed"

// UnnamedManager is what an empty marker is reported as. The FILE is the
// contract; its text only says who to name, and a marker with nothing in it
// still means the daemon is not this door's to start.
const UnnamedManager = "a service manager"

// ManagedPath is <state_dir>/managed.
func ManagedPath() string { return filepath.Join(StateDir(), ManagedFile) }

// Managed reports the manager the marker names and whether there is one. The
// content is free text — a launchd label, a systemd unit — and only its first
// line is read, because it is repeated inside a one-line refusal.
func Managed() (string, bool) {
	b, err := os.ReadFile(ManagedPath())
	if err != nil {
		return "", false
	}
	first, _, _ := strings.Cut(string(b), "\n")
	if name := strings.TrimSpace(first); name != "" {
		return name, true
	}
	return UnnamedManager, true
}

// LogPath is <state_dir>/dispatch.log, where a daemon started by a door writes:
// it has no terminal, and a dispatcher nobody can hear is worse than none.
func LogPath() string { return filepath.Join(StateDir(), Name+".log") }

// BasePaneOr resolves the pane worker panes are split off: what the caller
// was given, else what the config names. A daemon started inside a Herdr
// pane inherits one through HERDR_PANE_ID, which is the flag's default; a
// daemon started anywhere else has only these two.
func (c Config) BasePaneOr(given string) string {
	if given != "" {
		return given
	}
	return c.Pane
}

// EnsureStateDir creates the state dir, private to the user: the socket in
// it is a door onto the operator's own panes.
func EnsureStateDir() error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state dir %s: %w", dir, err)
	}
	// MkdirAll leaves a directory that already exists exactly as it found
	// it, so a dir created before this mode was chosen — or by anything
	// else — keeps whatever it had. The mode is asserted rather than
	// requested: the socket in here is a door onto the operator's panes.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("state dir %s: %w", dir, err)
	}
	return nil
}

func dirFrom(own, xdg, home string) string {
	if d := os.Getenv(own); d != "" {
		return d
	}
	if d := os.Getenv(xdg); d != "" {
		return filepath.Join(d, Name)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = "."
	}
	return filepath.Join(h, home, Name)
}
