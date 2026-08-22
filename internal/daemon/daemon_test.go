package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/version"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

const htaskScript = `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") cat "$HDIS_FAKE_DIR/get.json" ;;
"doctor "*|"doctor") cat "$HDIS_FAKE_DIR/doctor.json" ;;
*) echo '{}' ;;
esac`

const herdrScript = `case "$1 $2" in
"pane split") echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","agent_status":"unknown","revision":1}}}' ;;
"tab create") echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"wM:t9","workspace_id":"wM","label":"hdis-7"},"root_pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t9","agent_status":"unknown","revision":0}}}' ;;
"tab list") echo '{"id":"x","result":{"type":"tab_list","tabs":[]}}' ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// stateDir keeps the socket path short. A unix socket path has a hard length
// limit near a hundred characters and the temp dir a test is handed can eat
// most of it on its own.
// gitScript is the loop package's fake git, kept here too because a daemon
// case stands up its own Loop.
const gitScript = `prev=""; last=""
for a in "$@"; do prev=$last; last=$a; done
case "$3" in
rev-parse)
  [ "$4" = --verify ] && exit 1
  echo "$2" ;;
worktree)
  case "$4" in
  add) mkdir -p "$prev" ;;
  remove) rm -rf "$last" ;;
  esac ;;
esac
exit 0`

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
	// A git that answers for a project which is not a real repository: it
	// makes the directory a `worktree add` names and removes the one a
	// `worktree remove` names, which is all a spawn needs to be observed.
	f.Bin(t, "git", gitScript)
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
			Store:     &store.Bindings{Path: filepath.Join(t.TempDir(), "hdis-bindings.json")},
			Worktrees: &worktree.Manager{Root: t.TempDir(), Git: filepath.Join(f.Dir, "git")},
			BasePane:  "wM:p1",
			Log:       log.New(io.Discard, "", 0),
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
		if len(c) >= 10 && c[:10] == "tab create" {
			splits++
		}
	}
	if splits != 1 {
		t.Fatalf("one ready task became %d tabs", splits)
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

// The plugin's own conformance is a distinct field from the board's relayed
// contract, and a caller branching on it needs the name to stay put.
func TestDoctorDeclaresTheContractThisPluginSatisfies(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var doc struct {
		Contract string `json:"contract"`
		Board    struct {
			Contract string `json:"contract"`
		} `json:"board"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if doc.Contract != version.Contract {
		t.Errorf("doctor declares contract %q, want %q", doc.Contract, version.Contract)
	}
	if doc.Board.Contract != "0.3" {
		t.Errorf("the board's own contract %q was not relayed beside it", doc.Board.Contract)
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
		if len(c) >= 10 && c[:10] == "tab create" {
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

// A daemon with no pane to split a worker off would fail every spawn on the
// same missing pane, forever. It does not try, and it says so.
func TestADaemonWithNoBasePaneDoesNotTick(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	d.Loop.BasePane = ""
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); d.Serve(ctx, ln) }()

	// One round trip is enough to know the daemon is up and the tick has had
	// its chance to run.
	if _, err := (&net.Dialer{}).Dial("unix", config.SocketPath()); err != nil {
		t.Fatalf("dial: %v", err)
	}
	raw, err := call(t, d, protocol.Request{Verb: "status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var st loop.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Workers) != 0 {
		t.Fatalf("a daemon with no base pane drove %d workers", len(st.Workers))
	}
	for _, c := range f.Calls(t) {
		if len(c) >= 10 && c[:10] == "tab create" {
			t.Fatalf("a daemon with no base pane tried to split one: %v", c)
		}
	}
	cancel()
	<-served
}

// awaitStartupTick waits until the tick a starting daemon always runs has
// made every call it is going to make.
//
// It is not a sleep. A tick always reads two things, the board's ready list
// and Herdr's panes (loop.snapshot); with nothing ready and no bindings
// there is nothing to act on afterwards, so those two reads ARE the whole of
// that tick. Both seen means the tick has no call left to make, and a test
// that wants to attribute later calls to something else can take its mark.
func awaitStartupTick(t *testing.T, f *fake.Fake) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var board, panes bool
		for _, c := range f.Calls(t) {
			board = board || strings.Contains(c, "task list --ready")
			panes = panes || strings.Contains(c, "pane list")
		}
		if board && panes {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the startup tick never finished; it made %q", f.Calls(t))
}

// The ask-to-stop path: a daemon a door started can only be reached over the
// socket, so stop has to be a verb. It answers, stops ticking, and takes its
// socket and lock with it.
func TestStopShutsTheDaemonDownCleanly(t *testing.T) {
	dir := stateDir(t)
	d, f := newDaemon(t)
	// Nothing ready to dispatch. This case is about what a daemon does on
	// the way out, and a board with work on it makes the startup tick a
	// spawn whose length is set by a poll loop. Empty, the tick is the two
	// reads a snapshot always makes and it ends where awaitStartupTick can
	// see it.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	lock, err := Lock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Close()
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()

	// Daemon.tick runs the loop before it ever reaches its ticker, so a
	// daemon that starts serving always ticks once. That tick is the
	// dispatcher STARTING, and its calls are none of this case's business:
	// wait it out and take the mark afterwards, rather than racing it
	// against the stop round trip.
	awaitStartupTick(t, f)
	before := len(f.Calls(t))

	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Request{Verb: "stop", Door: "cli"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("read the answer to stop: %v", err)
	}
	conn.Close()
	if resp.Error != nil {
		t.Fatalf("stop over the socket: %+v", resp.Error)
	}
	var rep StopReport
	if err := json.Unmarshal(resp.Result, &rep); err != nil {
		t.Fatalf("stop json: %v", err)
	}
	if !rep.Stopping || rep.Socket != config.SocketPath() {
		t.Fatalf("stop answered %+v", rep)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve after stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop when asked")
	}

	if _, err := os.Stat(filepath.Join(dir, "hdis.sock")); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the daemon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hdis.lock")); !os.IsNotExist(err) {
		t.Errorf("the lock file outlived the daemon: %v", err)
	}
	// The board is htask's, and a dispatcher going away writes nothing to
	// it: no release, no note, no claim handed back. The mark was taken
	// after the startup tick, so everything past it belongs to stopping.
	after := f.Calls(t)
	if len(after) != before {
		t.Errorf("stopping reached out: %q", after[before:])
	}
	for _, c := range after {
		for _, write := range []string{"task claim", "task release", "task submit", "task approve", "task reject", "note "} {
			if strings.Contains(c, write) {
				t.Errorf("stopping wrote to the board: %q", c)
			}
		}
	}
}

// Nothing is ticking after a stop: the loop's goroutine is gone with it.
func TestStopEndsTheTick(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	d.Interval = 10 * time.Millisecond
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()

	conn, err := net.Dial("unix", config.SocketPath())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	json.NewEncoder(conn).Encode(protocol.Request{Verb: "stop"})
	var resp protocol.Response
	json.NewDecoder(conn).Decode(&resp)
	conn.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop when asked")
	}

	settled := len(f.Calls(t))
	time.Sleep(100 * time.Millisecond)
	if got := len(f.Calls(t)); got != settled {
		t.Fatalf("the tick ran %d more times after the daemon stopped", got-settled)
	}
}

// doctor names the file the bindings live in and how many of them a restart
// took back, which is the only place an operator can see either.
func TestDoctorSaysWhereTheBindingsLiveAndHowManyCameBack(t *testing.T) {
	d, f := newDaemon(t)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","agent":"claude","agent_status":"working","revision":1}]}}`)
	if err := d.Loop.Store.Save(store.State{Bindings: []decide.Binding{{
		TaskID: "01AAA", Pane: "wM:p9", PromptedAt: time.Now().UTC(), Prompts: 1,
	}}}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	if _, err := d.Loop.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.Bindings != d.Loop.Store.Path {
		t.Errorf("doctor says the bindings live at %q, want %q", rep.Bindings, d.Loop.Store.Path)
	}
	if rep.Readopted != 1 {
		t.Errorf("doctor reports %d re-adopted, want 1", rep.Readopted)
	}
	if rep.Workers != 1 {
		t.Errorf("doctor reports %d workers, want the re-adopted one", rep.Workers)
	}
}

// doctorOf runs the doctor verb through the daemon and decodes its report.
func doctorOf(t *testing.T, d *Daemon) DoctorReport {
	t.Helper()
	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	return rep
}

// Doctor says whether the verification lane is on, and with which profile.
// An operator asking why a submission earned no verifier reads it here.
func TestDoctorReportsTheVerificationLane(t *testing.T) {
	d, _ := newDaemon(t)
	rep := doctorOf(t, d)
	if rep.Verify.Enabled {
		t.Fatalf("the lane reads as on: %+v", rep.Verify)
	}

	cfg, err := config.Parse([]byte(`{"default":"w","profiles":{"w":{"provider":"claude"},"v":{"provider":"claude","model":"sonnet"}},"verify":{"enabled":true,"profile":"v"}}`))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg
	rep = doctorOf(t, d)
	if !rep.Verify.Enabled || rep.Verify.Profile != "v" {
		t.Fatalf("the lane reads as %+v", rep.Verify)
	}
}
