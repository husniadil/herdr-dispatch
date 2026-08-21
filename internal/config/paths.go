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
// The socket, the lock and the log of a daemon nobody is watching live
// there. No board fact does: the bindings are the dispatcher's only state
// and they stay in memory.
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

// LogPath is <state_dir>/hdis.log, where a daemon started by a door writes:
// it has no terminal, and a dispatcher nobody can hear is worse than none.
func LogPath() string { return filepath.Join(StateDir(), Name+".log") }

// EnsureStateDir creates the state dir, private to the user: the socket in
// it is a door onto the operator's own panes.
func EnsureStateDir() error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
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
