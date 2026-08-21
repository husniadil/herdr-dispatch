package loop

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// The verification lane needs two panes at once, so this herdr hands out a
// fresh pane id per split rather than the one id the worker cases reuse.
const herdrTwoPanes = `case "$1 $2" in
"pane split")
  n=$(cat "$HDIS_FAKE_DIR/splitn" 2>/dev/null || echo 8)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/splitn"
  printf '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p%s","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}' "$n" ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// reviewed is the board answering that the one task is in review, submitted
// by the worker in pane wM:p9.
const reviewed = `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`

// pane renders a pane list herdr would answer with, one entry per id/status
// pair given.
func paneList(entries ...string) string {
	var rows []string
	for i := 0; i+1 < len(entries); i += 2 {
		rows = append(rows, `{"pane_id":"`+entries[i]+`","name":"n","agent":"claude","agent_status":"`+entries[i+1]+
			`","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}`)
	}
	return `{"id":"x","result":{"type":"pane_list","panes":[` + strings.Join(rows, ",") + `]}}`
}

// newVerifyLoop is newLoop with the verification lane on and a herdr that can
// hand out more than one pane.
func newVerifyLoop(t *testing.T, enabled bool) (*Loop, *fake.Fake) {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "htask", htaskScript)
	f.Bin(t, "herdr", herdrTwoPanes)
	f.Write(t, "ready.json", readyOne)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", paneList())
	f.Write(t, "screen.txt", "⎿  Goal set: do the thing\n  ◎ /goal active\n")
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}`)

	doc := `{"default":"worker","profiles":{"worker":{"provider":"claude"},"verifier":{"provider":"claude","model":"sonnet","effort":"high"}}}`
	if enabled {
		doc = `{"default":"worker","profiles":{"worker":{"provider":"claude"},"verifier":{"provider":"claude","model":"sonnet","effort":"high"}},"verify":{"enabled":true,"profile":"verifier"}}`
	}
	cfg, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{
		Board:  &htask.Client{},
		Herdr:  &herdr.Client{},
		Config: cfg,
		Policy: decide.Policy{MaxWorkers: 4, ClaimTimeout: 5 * time.Minute, MaxPrompts: 2, Verify: cfg.Verify.Enabled},
		Spawn: &spawn.Pipeline{
			Herdr: &herdr.Client{}, Proxy: &proxy.Client{},
			StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: 5 * time.Second,
			Poll: time.Second, Sleep: func(time.Duration) {},
		},
		Store:    &store.Bindings{Path: filepath.Join(t.TempDir(), "hdis-bindings.json")},
		BasePane: "wM:p1",
		Now:      func() time.Time { return clock },
		Log:      log.New(io.Discard, "", 0),
	}
	return l, f
}

// submitted moves the board's one task into review and leaves the worker's
// pane alive, which is the state the verification lane acts on.
func submitted(t *testing.T, f *fake.Fake) {
	t.Helper()
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", reviewed)
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
}

func bindingFor(l *Loop, pane string) (decide.Binding, bool) {
	for _, b := range l.Bindings() {
		if b.Pane == pane {
			return b, true
		}
	}
	return decide.Binding{}, false
}

// Off is the default: a submission is announced and no second pane opens.
func TestWithTheLaneOffASubmissionSpawnsNoVerifier(t *testing.T) {
	l, f := newVerifyLoop(t, false)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes with the lane off: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}
}

// On, the submission earns a verifier on a pane of its own, launched from the
// verifier's profile, with the condition that forbids judging it.
func TestASubmissionEarnsAVerifierOnItsOwnPane(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	if got := calls(t, f, "pane split"); len(got) != 2 {
		t.Fatalf("split %d panes: %v", len(got), got)
	}
	v, ok := bindingFor(l, "wM:p10")
	if !ok {
		t.Fatalf("no verifier binding: %+v", l.Bindings())
	}
	if !v.IsVerifier() || v.TaskID != "01AAA" {
		t.Fatalf("verifier binding: %+v", v)
	}
	w, _ := bindingFor(l, "wM:p9")
	if !w.Verified {
		t.Fatalf("the worker's binding does not remember its verifier: %+v", w)
	}

	start := calls(t, f, "agent start")
	if len(start) != 2 {
		t.Fatalf("started %d agents: %v", len(start), start)
	}
	for _, want := range []string{"--pane wM:p10", "--model sonnet", "--effort high", spawn.GoalPrefix + spawn.VerifierGoal(7)} {
		if !strings.Contains(start[1], want) {
			t.Fatalf("want %q in the verifier's start: %q", want, start[1])
		}
	}
	// Delegating verification is not delegating judgment.
	for _, verb := range []string{"task approve", "task reject"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran %q: %v", verb, got)
		}
	}
}

// One submission, one verifier, however many ticks pass over it.
func TestASecondTickDoesNotSpawnASecondVerifier(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))
	for i := 0; i < 3; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := calls(t, f, "pane split"); len(got) != 2 {
		t.Fatalf("split %d panes for one submission: %v", len(got), got)
	}
}

// A verifier that went quiet past the grace is retired, and its pane closes.
func TestAFinishedVerifierIsRetiredAndItsPaneClosed(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "idle"))
	l.Now = func() time.Time { return clock.Add(time.Hour) }
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}

	if _, ok := bindingFor(l, "wM:p10"); ok {
		t.Fatalf("the verifier binding outlived the verifier: %+v", l.Bindings())
	}
	closed := calls(t, f, "pane close")
	if len(closed) != 1 || !strings.Contains(closed[0], "wM:p10") {
		t.Fatalf("closed panes: %v", closed)
	}
	// The worker's own binding is untouched by its verifier ending.
	if _, ok := bindingFor(l, "wM:p9"); !ok {
		t.Fatalf("the worker's binding went with the verifier: %+v", l.Bindings())
	}
}

// A rejection puts the task back to doing. The verifier is retired with the
// submission it was reading, and a resubmission earns a new one.
func TestARejectionRetiresTheVerifierAndTheNextSubmissionEarnsANewOne(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	// Rejected: back to doing, with the worker still holding it.
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", paneList("wM:p9", "working", "wM:p10", "working"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if _, ok := bindingFor(l, "wM:p10"); ok {
		t.Fatalf("the verifier outlived the submission it was reading: %+v", l.Bindings())
	}
	w, _ := bindingFor(l, "wM:p9")
	if w.Verified || w.Notified {
		t.Fatalf("the worker's binding still remembers the settled submission: %+v", w)
	}

	// Submitted again: a new verifier, and review announced again.
	submitted(t, f)
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("fourth tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 3 {
		t.Fatalf("split %d panes over two submissions: %v", len(got), got)
	}
	if got := calls(t, f, "notification show"); len(got) != 2 {
		t.Fatalf("announced review %d times over two submissions: %v", len(got), got)
	}
}

// A restart re-adopts a verifier as a verifier. Adopted as a worker it would
// be nudged to claim a task it must never claim.
func TestARestartReadoptsAVerifierAsAVerifier(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	v, ok := bindingFor(next, "wM:p10")
	if !ok || !v.IsVerifier() {
		t.Fatalf("the verifier was not re-adopted as one: %+v", next.Bindings())
	}
	w, ok := bindingFor(next, "wM:p9")
	if !ok || w.IsVerifier() || !w.Verified {
		t.Fatalf("the worker was not re-adopted as one: %+v", next.Bindings())
	}
	_ = f
}

// Status tells the operator which panes are doing the work and which are
// checking it.
func TestStatusNamesVerifiersApartFromWorkers(t *testing.T) {
	l, f := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	kinds := map[string]string{}
	for _, w := range st.Workers {
		kinds[w.Pane] = w.Kind
	}
	if kinds["wM:p9"] != decide.KindWorker || kinds["wM:p10"] != decide.KindVerifier {
		t.Fatalf("status kinds: %+v", kinds)
	}
}
