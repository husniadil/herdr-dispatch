package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
)

const htaskScript = `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") cat "$HDIS_FAKE_DIR/get.json" ;;
"doctor "*|"doctor") cat "$HDIS_FAKE_DIR/doctor.json" ;;
*) echo '{}' ;;
esac`

const herdrScript = `case "$1 $2" in
"pane split") echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","agent_status":"unknown","revision":1}}}' ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// stateDir keeps the socket path short. A unix socket path has a hard length
// limit near a hundred characters and the temp dir a test is handed can eat
// most of it on its own.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hdis-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv(config.EnvPrefix+"STATE_DIR", dir)
	return dir
}

func newDaemon(t *testing.T) (*Daemon, *fake.Fake) {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "htask", htaskScript)
	f.Bin(t, "herdr", herdrScript)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}],"count":1}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}}`)
	f.Write(t, "doctor.json", `{"version":"0.4.0","contract":"0.3","binary":"/bin/htask","socket_live":true,"herdr_reachable":true}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)
	f.Write(t, "screen.txt", "⎿  Goal set: do the thing\n  ◎ /goal active\n")
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"idle","interactive_ready":true,"revision":1}}}`)

	cfg := mustConfig(t)
	d := &Daemon{
		Loop: &loop.Loop{
			Board:  &htask.Client{},
			Herdr:  &herdr.Client{},
			Config: cfg,
			Policy: decide.Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2},
			Spawn: &spawn.Pipeline{
				Herdr: &herdr.Client{}, Proxy: &proxy.Client{},
				StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: 2 * time.Second,
				Poll: time.Second, Sleep: func(time.Duration) {},
			},
			BasePane: "wM:p1",
			Log:      log.New(io.Discard, "", 0),
		},
		Board:    &htask.Client{},
		Interval: time.Hour,
		Version:  "0.1.0",
		Log:      log.New(io.Discard, "", 0),
	}
	return d, f
}

func mustConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(`{"default":"worker","profiles":{"worker":{"provider":"claude"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func call(t *testing.T, d *Daemon, req protocol.Request) (json.RawMessage, error) {
	t.Helper()
	return d.Handle(context.Background(), req)
}

// One daemon per user. The second one meets the first one's lock and says so
// with a name, rather than binding a second socket over the same panes.
func TestASecondDaemonMeetsTheFirstOnesLock(t *testing.T) {
	stateDir(t)

	first, err := Lock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Close()

	second, err := Lock()
	if err == nil {
		second.Close()
		t.Fatal("a second daemon took the lock")
	}
	if got, want := codes.Of(err), codes.AlreadyRunning; got != want {
		t.Fatalf("second lock = %v (%q), want %q", err, got, want)
	}
}

// The lock is what stops two runners driving one board. A second runner that
// cannot take it never ticks, so it never spawns the task the first one is
// already spawning.
func TestASecondRunnerCannotDoubleSpawnATask(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)

	held, err := Lock()
	if err != nil {
		t.Fatalf("first runner: %v", err)
	}
	defer held.Close()

	// The second runner starts here, and gets no further.
	if _, err := Lock(); codes.Of(err) != codes.AlreadyRunning {
		t.Fatalf("the second runner started: %v", err)
	}

	if err := d.Loop.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	splits := 0
	for _, c := range f.Calls(t) {
		if len(c) >= 10 && c[:10] == "pane split" {
			splits++
		}
	}
	if splits != 1 {
		t.Fatalf("one ready task became %d panes", splits)
	}
}

func TestLockReleasesWhenTheDaemonEnds(t *testing.T) {
	stateDir(t)
	first, err := Lock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	first.Close()

	second, err := Lock()
	if err != nil {
		t.Fatalf("the lock outlived the daemon that held it: %v", err)
	}
	second.Close()
}

// doctor is what an operator runs when something else refused. It has to
// answer even when the board is the thing that is down.
func TestDoctorAnswersWhenTheBoardIsDown(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	f.Bin(t, "htask", `echo 'the daemon is not answering' >&2; exit 1`)

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.Board.Reachable {
		t.Error("doctor called a board that refused reachable")
	}
	if rep.Board.Error == "" {
		t.Error("doctor did not say why the board is unreachable")
	}
	if rep.Version != "0.1.0" || rep.BasePane != "wM:p1" || rep.MaxWorkers != 2 {
		t.Errorf("doctor report: %+v", rep)
	}
}

func TestDoctorReportsTheBoardItCanReach(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if !rep.Board.Reachable || rep.Board.Version != "0.4.0" || !rep.Board.HerdrReachable {
		t.Fatalf("doctor report: %+v", rep)
	}
}

func TestDispatchOverTheSocketAnswersWithAReservation(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)

	raw, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}, Pane: "wZ:p3", Door: "mcp"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var res loop.Reservation
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("dispatch json: %v", err)
	}
	if res.TaskID != "01AAA" || res.Seq != 7 {
		t.Fatalf("reservation: %+v", res)
	}
	for _, c := range f.Calls(t) {
		if len(c) >= 10 && c[:10] == "pane split" {
			t.Fatal("dispatch spawned before answering")
		}
	}
}

func TestDispatchRefusesWithTheCodeTheLoopGave(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	d.Loop.BasePane = ""

	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	if got, want := codes.Of(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
}

func TestStatusOverTheSocketAnswersWithTheBindings(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	if err := d.Loop.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	raw, err := call(t, d, protocol.Request{Verb: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var st loop.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].Seq != 7 {
		t.Fatalf("status: %+v", st)
	}
}

func TestAnUnknownVerbIsInvalid(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{Verb: "approve", Args: map[string]any{"task": "7"}})
	if got, want := codes.Of(err), codes.Invalid; got != want {
		t.Fatalf("unknown verb = %v (%q), want %q", err, got, want)
	}
}

func TestAMissingRequiredArgumentIsInvalid(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{Verb: "dispatch"})
	if got, want := codes.Of(err), codes.Invalid; got != want {
		t.Fatalf("dispatch with no task = %v (%q), want %q", err, got, want)
	}
}

func TestAnUndeclaredArgumentIsInvalid(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{Verb: "status", Args: map[string]any{"profile": "routed"}})
	if got, want := codes.Of(err), codes.Invalid; got != want {
		t.Fatalf("status with an argument it does not take = %v (%q), want %q", err, got, want)
	}
}

// The socket is a door onto the operator's own panes: nobody else on the
// machine gets to knock.
func TestTheSocketIsPrivateToTheUser(t *testing.T) {
	dir := stateDir(t)
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	fi, err := os.Stat(filepath.Join(dir, "hdis.sock"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
}

func TestListenReplacesASocketNoDaemonIsBehind(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "hdis.sock"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	ln.Close()
}

// A whole round trip on the socket, the way both doors make it.
func TestServeAnswersOnTheSocketAndStopsWithItsContext(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()

	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Request{Verb: "doctor", Door: "cli"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	conn.Close()
	if resp.Error != nil {
		t.Fatalf("doctor over the socket: %+v", resp.Error)
	}
	var rep DoctorReport
	if err := json.Unmarshal(resp.Result, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.Socket != config.SocketPath() {
		t.Errorf("doctor names socket %q, want %q", rep.Socket, config.SocketPath())
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop with its context")
	}
}

// A refusal reaches the caller as a named code on the wire, not as a dropped
// connection.
func TestARefusalTravelsAsANamedCode(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	d.Loop.BasePane = ""
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ln)

	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != string(codes.NoBasePane) {
		t.Fatalf("response: %+v", resp)
	}
}
