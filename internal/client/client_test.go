package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
)

// build compiles the real binary before the fakes take over PATH, because the
// Go toolchain is not one of the fakes.
func build(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hdis-bin-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "hdis")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/husniadil/herdr-dispatch/cmd/hdis").CombinedOutput()
	if err != nil {
		t.Fatalf("build hdis: %v\n%s", err, out)
	}
	return bin
}

// world puts a fake board and a fake herdr on PATH, a config where the daemon
// will look for one, and a short state dir for the socket.
func world(t *testing.T) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "hdis-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	t.Setenv(config.EnvPrefix+"STATE_DIR", state)
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", state)
	// A daemon started here must not inherit the operator's own pane.
	t.Setenv("HERDR_PANE_ID", "")

	f := fake.New(t)
	// The fake `herdr` on PATH is not enough on its own: §11.1 has the
	// client read HERDR_BIN_PATH at construction, and an operator's shell
	// exports it, so a daemon started here would call the operator's live
	// Herdr — and adopt a base pane off their real screen.
	t.Setenv("HERDR_BIN_PATH", filepath.Join(f.Dir, "herdr"))
	f.Bin(t, "htask", `case "$1" in
doctor) echo '{"version":"0.4.0","contract":"0.3","binary":"/bin/htask","socket_live":true,"herdr_reachable":true}' ;;
*) echo '{"tasks":[],"count":0}' ;;
esac`)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"pane_list","panes":[]}}'`)
	if err := os.WriteFile(filepath.Join(state, "dispatch.toml"),
		[]byte(`default = "worker"
[profiles.worker]
provider = "claude"
`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// reap stops a daemon a test brought up, so no test leaves one behind.
func reap(t *testing.T, c *Client) {
	t.Helper()
	t.Cleanup(func() {
		if c.Started != nil {
			c.Started.Kill()
			c.Started.Wait()
		}
	})
}

// A door that finds no live socket starts the daemon and waits for it,
// bounded, rather than fail.
func TestACallWithNoDaemonStartsOneAndWaitsForIt(t *testing.T) {
	bin := build(t)
	world(t)

	c := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, c)
	raw, err := c.Call(protocol.Request{Verb: "doctor", Door: "cli"})
	if err != nil {
		t.Fatalf("doctor with no daemon running: %v", err)
	}
	if c.Started == nil {
		t.Fatal("the call answered without starting a daemon, and none was running")
	}
	var rep struct {
		Version string `json:"version"`
		Socket  string `json:"socket"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.Socket != config.SocketPath() {
		t.Errorf("the daemon answers on %q, want %q", rep.Socket, config.SocketPath())
	}
	if _, err := os.Stat(config.LogPath()); err != nil {
		t.Errorf("a daemon nobody is watching wrote no log: %v", err)
	}
}

// The second door finds the first door's daemon. One daemon per user is the
// whole point, and starting a second would meet the lock anyway.
func TestASecondCallReusesTheDaemonTheFirstStarted(t *testing.T) {
	bin := build(t)
	world(t)

	first := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, first)
	if _, err := first.Call(protocol.Request{Verb: "doctor"}); err != nil {
		t.Fatalf("first call: %v", err)
	}

	second := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, second)
	if _, err := second.Call(protocol.Request{Verb: "status"}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second.Started != nil {
		t.Fatal("the second call started a second daemon")
	}
}

// A refusal from the daemon reaches the caller with its name on it.
func TestARefusalKeepsItsNameThroughTheSocket(t *testing.T) {
	bin := build(t)
	world(t)

	c := &Client{Bin: bin, Timeout: 10 * time.Second}
	reap(t, c)
	// No pane was inherited and none is configured, so this is the daemon
	// saying it has nowhere to put a worker.
	_, err := c.Call(protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	if got, want := codes.ReasonOf(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
}

// The wait is bounded: a daemon that never comes up is an answer, not a hang.
func TestACallGivesUpBoundedWhenNoDaemonComesUp(t *testing.T) {
	world(t)

	c := &Client{Bin: "/usr/bin/true", Timeout: 300 * time.Millisecond}
	start := time.Now()
	_, err := c.Call(protocol.Request{Verb: "doctor"})
	if got, want := codes.Of(err), codes.Unavailable; got != want {
		t.Fatalf("call = %v (%q), want %q", err, got, want)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the call waited %s for a daemon that never came", waited)
	}
}

// Stop is the one verb that must not autostart a daemon. Starting one just to
// ask it to go away leaves the operator with a process that ran a tick and a
// spawn on its way in, which is the opposite of what they asked for.
func TestStopWithNoDaemonDoesNotStartOne(t *testing.T) {
	bin := build(t)
	world(t)

	c := &Client{Bin: bin, Timeout: 10 * time.Second, NoStart: true}
	reap(t, c)
	_, err := c.Call(protocol.Request{Verb: "stop", Door: "cli"})
	if got, want := codes.ReasonOf(err), codes.NotRunning; got != want {
		t.Fatalf("stop with no daemon = %v (%q), want %q", err, got, want)
	}
	if c.Started != nil {
		t.Fatal("stop started a daemon just to stop it")
	}
	if _, err := os.Stat(config.SocketPath()); !os.IsNotExist(err) {
		t.Errorf("stop left a socket behind: %v", err)
	}
}
