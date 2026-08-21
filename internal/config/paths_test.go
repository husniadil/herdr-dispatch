package config

import (
	"path/filepath"
	"testing"
)

func TestStateDirPrefersThePluginsOwnOverride(t *testing.T) {
	t.Setenv("HDIS_STATE_DIR", "/tmp/somewhere")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, want := StateDir(), "/tmp/somewhere"; got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirFallsBackToTheXdgBase(t *testing.T) {
	t.Setenv("HDIS_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, want := StateDir(), filepath.Join("/tmp/xdg", Name); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestSocketAndLockSitInTheStateDir(t *testing.T) {
	t.Setenv("HDIS_STATE_DIR", "/tmp/state")
	if got, want := SocketPath(), "/tmp/state/hdis.sock"; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := LockPath(), "/tmp/state/hdis.lock"; got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}
}

func TestConfigPathKeepsTheDocumentWhereItAlreadyLives(t *testing.T) {
	t.Setenv("HDIS_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/cfg")
	if got, want := ConfigPath(), "/tmp/cfg/hdis/hdis.json"; got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestEnsureStateDirMakesItPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("HDIS_STATE_DIR", dir)
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir twice: %v", err)
	}
}
