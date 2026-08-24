package daemon

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
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
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/version"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

const htaskScript = `case "$1" in
"list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"get") cat "$HDIS_FAKE_DIR/get.json" ;;
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

func newDaemon(t *testing.T) (*Daemon, *testenv.Fake) {
	t.Helper()
	f := testenv.New(t)
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
			Herdr:  &herdrclient.Client{},
			Config: cfg,
			Policy: decide.Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2},
			Spawn: &spawn.Pipeline{
				Herdr: &herdrclient.Client{}, Proxy: &proxy.Client{},
				StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: 2 * time.Second,
				Poll: time.Second, Sleep: func(time.Duration) {},
			},
			Store:     &store.Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")},
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
	cfg, err := config.Parse([]byte(`default = "worker"
[profiles.worker]
provider = "claude"
`))
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
	if got, want := codes.ReasonOf(err), codes.AlreadyRunning; got != want {
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
	if _, err := Lock(); codes.ReasonOf(err) != codes.AlreadyRunning {
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

// §10.3: doctor prints the state dir and the config dir. It printed neither,
// so an operator whose override was not taking effect — a daemon started from
// a shell that exported a different DISPATCH_STATE_DIR, a config edited in the
// wrong one — had no way to ask the running daemon which pair it resolved.
func TestDoctorNamesTheDirectoriesItResolved(t *testing.T) {
	state := stateDir(t)
	cfg := t.TempDir()
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", cfg)
	d, _ := newDaemon(t)

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.StateDir != state {
		t.Errorf("doctor names state dir %q, want %q", rep.StateDir, state)
	}
	if rep.ConfigDir != cfg {
		t.Errorf("doctor names config dir %q, want %q", rep.ConfigDir, cfg)
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
	if got, want := codes.ReasonOf(err), codes.NoBasePane; got != want {
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
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
		t.Fatalf("unknown verb = %v (%q), want %q", err, got, want)
	}
}

func TestAMissingRequiredArgumentIsInvalid(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{Verb: "dispatch"})
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
		t.Fatalf("dispatch with no task = %v (%q), want %q", err, got, want)
	}
}

func TestAnUndeclaredArgumentIsInvalid(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{Verb: "status", Args: map[string]any{"profile": "routed"}})
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
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

	fi, err := os.Stat(filepath.Join(dir, "dispatch.sock"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
}

func TestListenReplacesASocketNoDaemonIsBehind(t *testing.T) {
	dir := stateDir(t)
	if err := os.WriteFile(filepath.Join(dir, "dispatch.sock"), []byte("stale"), 0o600); err != nil {
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
	served := serve(t, d, ctx, ln)

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
	serve(t, d, ctx, ln)

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
	if resp.Error == nil || resp.Error.Code != string(codes.Unsupported) ||
		!strings.HasPrefix(resp.Error.Message, string(codes.NoBasePane)+": ") {
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
	served := serve(t, d, ctx, ln)

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

// The plugin-started daemon's whole story: it comes up before Herdr has a
// pane, so it inherits none and adopts none on its first round; once a pane
// exists it adopts one and the loop starts running against the board.
func TestAPaneLessDaemonAdoptsABaseAndStartsTicking(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	d.Loop.BasePane = ""
	d.Interval = 20 * time.Millisecond
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := serve(t, d, ctx, ln)
	defer func() { cancel(); <-served }()

	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1K:p1","workspace_id":"w1K","tab_id":"w1K:t1","agent_status":"idle"}]}}`)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d.Loop.Base() == "w1K:p1" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the daemon never adopted a base pane; base = %q", d.Loop.Base())
}

// awaitStartupTick waits until the tick a starting daemon always runs has
// made every call it is going to make.
//
// It is not a sleep. A tick always reads two things, the board's ready list
// and Herdr's panes (loop.snapshot); with nothing ready and no bindings
// there is nothing to act on afterwards, so those two reads ARE the whole of
// that tick. Both seen means the tick has no call left to make, and a test
// that wants to attribute later calls to something else can take its mark.
func awaitStartupTick(t *testing.T, f *testenv.Fake) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var board, panes bool
		for _, c := range f.Calls(t) {
			board = board || strings.Contains(c, "list --ready")
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
	d.Lock = lock
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := serve(t, d, ctx, ln)

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

	if _, err := os.Stat(filepath.Join(dir, "dispatch.sock")); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the daemon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dispatch.lock")); !os.IsNotExist(err) {
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
		for _, write := range []string{"claim", "release", "submit", "approve", "reject", "note "} {
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
	served := serve(t, d, ctx, ln)

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

// Doctor says whether the verification lane is on. An operator asking why a
// submission earned no self-review shot reads it here.
func TestDoctorReportsTheVerificationLane(t *testing.T) {
	d, _ := newDaemon(t)
	rep := doctorOf(t, d)
	if rep.Verify.Enabled {
		t.Fatalf("the lane reads as on: %+v", rep.Verify)
	}

	cfg, err := config.Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
[verify]
enabled = true
`))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg
	rep = doctorOf(t, d)
	if !rep.Verify.Enabled {
		t.Fatalf("the lane reads as %+v", rep.Verify)
	}
}

// The operator sets max_panes_per_tab in the config, so it has to be
// readable back off the running daemon like the floor beside it.
func TestDoctorReportsTheMaxPanesPerTab(t *testing.T) {
	d, _ := newDaemon(t)
	cfg, err := config.Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
[layout]
max_panes_per_tab = 2
`))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg
	if rep := doctorOf(t, d); rep.MaxPanesPerTab != 2 {
		t.Fatalf("doctor reports max_panes_per_tab %d, want 2", rep.MaxPanesPerTab)
	}
}

// serve starts the daemon and holds the test open until Serve has returned.
// A Serve nobody waits on outlives the test that started it: when it finally
// wakes, cancelled, it tears down against whatever state dir the environment
// names by then, which is the NEXT test's.
func serve(t *testing.T, d *Daemon, ctx context.Context, ln net.Listener) <-chan error {
	t.Helper()
	served := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		served <- d.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("a Serve goroutine outlived its test")
		}
	})
	return served
}

// Teardown removes the socket and the lock this daemon itself opened. It
// resolves neither from the environment: a daemon that reads the state dir
// again on the way out deletes whatever that dir holds by then, which is
// somebody else's socket the moment the dir has changed underneath it.
func TestCleanupRemovesWhatTheDaemonOpenedNotWhatTheStateDirNamesNow(t *testing.T) {
	opened := stateDir(t)
	lock, err := Lock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Close()
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	d, _ := newDaemon(t)
	d.Lock = lock

	// The state dir moves under the daemon, exactly as a t.Setenv in the
	// next test moves it. The new dir holds a live daemon's files.
	moved := stateDir(t)
	for _, name := range []string{"dispatch.sock", "dispatch.lock"} {
		if err := os.WriteFile(filepath.Join(moved, name), []byte("live"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	d.Cleanup(ln)

	for _, name := range []string{"dispatch.sock", "dispatch.lock"} {
		if _, err := os.Stat(filepath.Join(opened, name)); !os.IsNotExist(err) {
			t.Errorf("%s the daemon opened outlived its teardown: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
			t.Errorf("teardown removed %s in a state dir it never opened: %v", name, err)
		}
	}
}

// The same defect, driven through Serve on purpose rather than waited for.
// The tick is held inside the fake htask, so Serve cannot reach its teardown
// until this test lets it: the state dir is moved while Serve is pinned
// short of Cleanup, and the daemon still removes only its own files.
func TestAServeThatTearsDownLateLeavesTheNewStateDirAlone(t *testing.T) {
	opened := stateDir(t)
	d, f := newDaemon(t)
	// A tick that blocks until this test says so, which is what pins Serve
	// short of its teardown: Serve waits for the tick before it cleans up.
	f.Bin(t, "htask", `if [ "$1" = "list" ]; then
  : > "$HDIS_FAKE_DIR/ticking"
  n=0
  while [ ! -f "$HDIS_FAKE_DIR/release" ] && [ $n -lt 400 ]; do sleep 0.05; n=$((n+1)); done
fi
`+htaskScript)
	lock, err := Lock()
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Close()
	ln, err := Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	d.Lock = lock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := serve(t, d, ctx, ln)

	await(t, filepath.Join(f.Dir, "ticking"), "the tick to start")
	cancel()

	// Serve is now past its listener and blocked on the tick, so the state
	// dir can move with no race about where it is when teardown runs.
	moved := stateDir(t)
	for _, name := range []string{"dispatch.sock", "dispatch.lock"} {
		if err := os.WriteFile(filepath.Join(moved, name), []byte("live"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.Dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not return once its tick was let go")
	}

	for _, name := range []string{"dispatch.sock", "dispatch.lock"} {
		if _, err := os.Stat(filepath.Join(moved, name)); err != nil {
			t.Errorf("a late teardown removed %s in a state dir it never opened: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(opened, name)); !os.IsNotExist(err) {
			t.Errorf("%s the daemon opened outlived its late teardown: %v", name, err)
		}
	}
}

// await waits for a file the fake writes, so an ordering this package needs
// is a fact the test observed rather than a sleep it hoped was long enough.
func await(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Every Serve this package starts is started through serve, which is what
// waits for it. A test that fires one and forgets it is the ordering defect
// this file already carries a case for, so the rule is checked in the source
// rather than left to whoever writes the next case.
func TestNoTestStartsAServeItNeverWaitsFor(t *testing.T) {
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no test files found to check")
	}
	found := 0
	for _, name := range names {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Serve" {
					return true
				}
				found++
				if fn.Name.Name != "serve" {
					t.Errorf("%s calls Serve directly; start it through serve, which waits for it",
						fn.Name.Name)
				}
				return true
			})
		}
	}
	if found == 0 {
		t.Fatal("no Serve call found in this package's tests; the check proves nothing")
	}
}

// §3.5: the boundary is the local user account — whoever can open the socket
// is trusted as the user — so a plugin MUST create its socket 0600. Here that
// door reaches `pane split`, `agent start` and `agent prompt` on the
// operator's own Herdr, so a mode another account can open is that account
// holding the operator's panes.
//
// Listen chmods after net.Listen because the umask applies to the bind, so the
// constant alone proves nothing: this asserts the mode on disk.
func TestTheSocketIsCreatedPrivateToTheUser(t *testing.T) {
	// A short path on purpose: a unix socket is bounded by sun_path, and
	// t.TempDir() names itself after the test.
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("DISPATCH_STATE_DIR", dir)
	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	info, err := os.Stat(config.SocketPath())
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	// The literal, not the constant. Comparing the file against SocketMode
	// moves both sides of the check together: a mutation that opened the
	// constant to 0666 opened the socket to 0666 and stayed green.
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("the socket is %04o, want 0600 (§3.5): it is a door onto the operator's panes", got)
	}
	if SocketMode != 0o600 {
		t.Errorf("SocketMode is %04o; §3.5 fixes it at 0600", SocketMode)
	}
}

// §4.2: the door resolved --project to a canonical path, and the daemon has
// to hand it to the loop. Without this the flag parses, travels the socket
// and changes nothing — a caller believing they named one board while every
// board answers, which is the failure a flag that is quietly dropped always
// is. The ready row this fake serves is on /src/p, so naming another board
// must refuse.
func TestTheProjectAJobNamesReachesTheLoop(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Project: "/src/elsewhere"})
	if err == nil {
		t.Fatal("a task on /src/p was dispatched under --project /src/elsewhere")
	}
	if !strings.Contains(err.Error(), "/src/elsewhere") {
		t.Errorf("the refusal does not name the board it looked on: %v", err)
	}

	raw, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Project: "/src/p"})
	if err != nil {
		t.Fatalf("dispatch --project /src/p 7: %v", err)
	}
	var res loop.Reservation
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("dispatch json: %v", err)
	}
	if res.TaskID != "01AAA" {
		t.Fatalf("reservation: %+v", res)
	}
}

// doctor said whether the board and Herdr were reachable but not the proxy,
// so the first thing an operator learned about a down proxenos was a failed
// codex spawn. It answers here now, before one is tried.
func TestDoctorReportsTheProxyItCanReach(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	f.Bin(t, "proxenos", `echo '{"auth":{"account":"work-codex"}}'`)

	rep := doctorOf(t, d)
	if !rep.Proxy.Installed || !rep.Proxy.Reachable {
		t.Fatalf("proxy report: %+v", rep.Proxy)
	}
	if got, want := rep.Proxy.Account, "work-codex"; got != want {
		t.Fatalf("account: got %q, want %q", got, want)
	}
	if rep.Proxy.Binary != "proxenos" || rep.Proxy.Error != "" {
		t.Fatalf("proxy report: %+v", rep.Proxy)
	}
}

// A proxy that is down is reported in its own words. Swallowing them would
// leave the operator with the one fact they already had.
func TestDoctorReportsADownProxyInItsOwnWords(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	f.Bin(t, "proxenos", `echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1`)

	rep := doctorOf(t, d)
	if rep.Proxy.Reachable {
		t.Fatal("doctor called a proxy that refused reachable")
	}
	if !rep.Proxy.Installed {
		t.Fatal("a proxy that answered at all is installed")
	}
	if !strings.Contains(rep.Proxy.Error, "proxenos run") {
		t.Fatalf("the proxy's own words are gone: %q", rep.Proxy.Error)
	}
}

// A proxy nobody installed is not a failure: no configured profile launches
// through it here, and doctor says both facts rather than one that reads as
// an outage.
func TestDoctorSaysWhenNoProxyIsInstalledAndNoneIsNeeded(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)

	rep := doctorOf(t, d)
	if rep.Proxy.Installed || rep.Proxy.Reachable {
		t.Fatalf("proxy report: %+v", rep.Proxy)
	}
	if len(rep.Proxy.Profiles) != 0 {
		t.Fatalf("the claude profile path never touches the proxy: %+v", rep.Proxy.Profiles)
	}
	if rep.Proxy.Error == "" {
		t.Fatal("doctor says why it got no answer")
	}
}

// Which profiles launch through the proxy is what turns its state into a
// consequence: the same down proxy is an outage for a codex profile and
// nothing at all for a claude one.
func TestDoctorNamesTheProfilesThatLaunchThroughTheProxy(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	f.Bin(t, "proxenos", `echo '{"auth":{"account":"work-codex"}}'`)
	cfg, err := config.Parse([]byte(`default = "worker"
[profiles.worker]
provider = "claude"
[profiles.cheap]
provider = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg

	rep := doctorOf(t, d)
	if got := rep.Proxy.Profiles; len(got) != 1 || got[0] != "cheap" {
		t.Fatalf("profiles: got %+v", got)
	}
}
