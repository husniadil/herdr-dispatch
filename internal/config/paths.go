package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Name is the binary's short name. It names the state dir, the socket, the
// lock and the config document.
const Name = "hdis"

// EnvPrefix is this binary's own override prefix, and how a test isolates
// its daemon from the operator's.
const EnvPrefix = "HDIS_"

// StateDir is HDIS_STATE_DIR, else ${XDG_STATE_HOME:-~/.local/state}/hdis.
// The socket, the lock, the log and the bindings live there. No board fact
// does: the bindings are the dispatcher's only state, and they are the one
// thing about them that is not derivable from the board or from Herdr.
func StateDir() string {
	return dirFrom(EnvPrefix+"STATE_DIR", "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// ConfigDir is HDIS_CONFIG_DIR, else ${XDG_CONFIG_HOME:-~/.config}/hdis.
func ConfigDir() string {
	return dirFrom(EnvPrefix+"CONFIG_DIR", "XDG_CONFIG_HOME", ".config")
}

// SocketPath is <state_dir>/hdis.sock: the private door every client of the
// daemon dials.
func SocketPath() string { return filepath.Join(StateDir(), Name+".sock") }

// LockPath is <state_dir>/hdis.lock, the file whose flock elects the one
// daemon. It is held for the daemon's lifetime and released by the kernel
// when the process ends, so a crash leaves nothing to clean up.
func LockPath() string { return filepath.Join(StateDir(), Name+".lock") }

// ConfigPath is <config_dir>/hdis.json, where the profiles already live.
func ConfigPath() string { return filepath.Join(ConfigDir(), Name+".json") }

// BindingsPath is <state_dir>/hdis-bindings.json, the pane-to-task mapping
// the dispatcher re-adopts at start. It is the plugin's own state dir, per
// §5.1, and a JSON document rather than the section's SQLite file for the
// reason the README records.
func BindingsPath() string { return filepath.Join(StateDir(), Name+"-bindings.json") }

// WorktreeDir is <state_dir>/worktrees, where a verifier's throwaway
// checkout is made. It is outside every project on purpose: the tree a
// verifier works in must not be one a worker or the operator is holding.
func WorktreeDir() string { return filepath.Join(StateDir(), "worktrees") }

// LogPath is <state_dir>/hdis.log, where a daemon started by a door writes:
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
