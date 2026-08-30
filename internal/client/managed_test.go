package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
)

// mark writes the marker a service manager leaves in the state dir.
func mark(t *testing.T, manager string) {
	t.Helper()
	if err := os.MkdirAll(config.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.StateDir(), config.ManagedFile),
		[]byte(manager+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The marker turns autostart off. On a box where launchd owns the daemon, a
// door that starts one under the caller's own environment gets a daemon with
// none of the service's configuration holding dispatch.lock, and the
// service's own daemon then meets ALREADY_RUNNING on every retry.
func TestAMarkedStateDirRefusesToStartADaemonAndSaysWhoOwnsIt(t *testing.T) {
	// Built before the fakes take over PATH, so what is measured is the
	// refusal and not a start that had nothing to start.
	bin := build(t)
	world(t)
	mark(t, "dev.herdr.hdis")

	c := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, c)
	_, err := c.Call(protocol.Request{Verb: "status", Door: "cli"})
	if err == nil {
		t.Fatal("the call answered, with no daemon running and the marker in place")
	}
	if c.Started != nil {
		t.Fatal("the marker did not stop the door starting a daemon")
	}
	if got, want := codes.Of(err), codes.Conflict; got != want {
		t.Errorf("call = %v (%q), want %q", err, got, want)
	}
	if got, want := codes.ReasonOf(err), codes.NotRunning; got != want {
		t.Errorf("call refuses for %q, want %q", got, want)
	}
	for what, want := range map[string]string{
		"the socket that is silent": config.SocketPath(),
		"the marker that refused":   config.ManagedPath(),
		"the manager it names":      "dev.herdr.hdis",
		"the lock at stake":         config.LockPath(),
		"what the operator does":    "restart the service",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %s (looked for %q): %v", what, want, err)
		}
	}
	if manager, ok := ManagedRefusal(err); !ok || manager != "dev.herdr.hdis" {
		t.Errorf("ManagedRefusal = %q, %v; want the manager and true", manager, ok)
	}
}

// The same door with no marker starts a daemon, which is what
// TestACallWithNoDaemonStartsOneAndWaitsForIt holds for the whole path. This
// is the narrow half of it: the marker, and nothing else, is the switch.
func TestAnUnmarkedStateDirStartsADaemonAsBefore(t *testing.T) {
	bin := build(t)
	world(t)

	c := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, c)
	if _, err := c.Call(protocol.Request{Verb: "doctor", Door: "cli"}); err != nil {
		t.Fatalf("doctor with no daemon and no marker: %v", err)
	}
	if c.Started == nil {
		t.Fatal("the call answered without starting a daemon, and none was running")
	}
	if _, ok := ManagedRefusal(nil); ok {
		t.Error("a nil failure reads as the managed refusal")
	}
}

// `stop` never started a daemon and its refusal is unchanged: a marker does
// not rewrite the answer to a verb that was never going to start anything.
func TestStopSaysWhatItAlwaysSaidUnderAMarker(t *testing.T) {
	world(t)
	mark(t, "dev.herdr.hdis")

	c := &Client{NoStart: true}
	_, err := c.Call(protocol.Request{Verb: "stop", Door: "cli"})
	if got, want := codes.ReasonOf(err), codes.NotRunning; got != want {
		t.Fatalf("stop = %v (%q), want %q", err, got, want)
	}
	if _, ok := ManagedRefusal(err); ok {
		t.Errorf("stop answered with the managed refusal: %v", err)
	}
}
