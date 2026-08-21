package loop

import (
	"context"
	"io"
	"log"
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
)

var clock = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

const htaskScript = `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") cat "$HDIS_FAKE_DIR/get.json" ;;
"task goal") cat "$HDIS_FAKE_DIR/goal.txt" ;;
*) echo '{}' ;;
esac`

const herdrScript = `case "$1 $2" in
"pane split") echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}' ;;
"pane read") cat "$HDIS_FAKE_DIR/read.json" ;;
"agent list") cat "$HDIS_FAKE_DIR/agents.json" ;;
"agent get") echo '{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}' ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

const readyOne = `{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}],"count":1}`

func newLoop(t *testing.T) (*Loop, *fake.Fake) {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "htask", htaskScript)
	f.Bin(t, "herdr", herdrScript)
	f.Write(t, "ready.json", readyOne)
	f.Write(t, "get.json", `{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}`)
	f.Write(t, "goal.txt", "do the thing · Done when: it is done")
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[]}}`)
	f.Write(t, "read.json", `{"id":"x","result":{"type":"pane_read","read":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","source":"detection","format":"text","revision":1,"truncated":false,"text":"⎿  Goal set: do the thing\n  ◎ /goal active"}}}`)

	cfg, err := config.Parse([]byte(`{"default":"worker","profiles":{"worker":{"provider":"claude"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{
		Board:  &htask.Client{},
		Herdr:  &herdr.Client{},
		Config: cfg,
		Policy: decide.Policy{MaxWorkers: 2, ClaimTimeout: 5 * time.Minute, MaxPrompts: 2},
		Spawn: &spawn.Pipeline{
			Herdr: &herdr.Client{}, Proxy: &proxy.Client{},
			StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: time.Second,
			Poll: time.Second, Sleep: func(time.Duration) {},
		},
		BasePane: "wM:p1",
		Now:      func() time.Time { return clock },
		Log:      log.New(io.Discard, "", 0),
	}
	return l, f
}

func calls(t *testing.T, f *fake.Fake, prefix string) []string {
	t.Helper()
	var out []string
	for _, c := range f.Calls(t) {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// One tick, end to end: a ready task on the board becomes a worker pane with
// the task's goal already armed, and the dispatcher records the one mapping
// that exists nowhere else until the worker claims.
func TestATickTakesAReadyTaskToADeliveredGoal(t *testing.T) {
	l, f := newLoop(t)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	b := l.bindings[0]
	if b.TaskID != "01AAA" || b.Pane != "wM:p9" || b.Prompts != 1 || !b.PromptedAt.Equal(clock) {
		t.Fatalf("binding: %+v", b)
	}

	if got := calls(t, f, "task list --ready --all-projects"); len(got) != 1 {
		t.Fatalf("ready was asked for %d times", len(got))
	}
	if got := calls(t, f, "task goal 01AAA --one-line"); len(got) != 1 {
		t.Fatalf("the goal was asked for %d times: %v", len(got), f.Calls(t))
	}
	start := calls(t, f, "agent start")
	if len(start) != 1 {
		t.Fatalf("agent start ran %d times", len(start))
	}
	for _, want := range []string{"hdis-7", "--kind claude", "--pane wM:p9", "/goal do the thing · Done when: it is done"} {
		if !strings.Contains(start[0], want) {
			t.Fatalf("want %q in %q", want, start[0])
		}
	}
	// The worker claims for itself; the dispatcher never claims for it.
	if got := calls(t, f, "task claim"); len(got) != 0 {
		t.Fatalf("the dispatcher claimed on the worker's behalf: %v", got)
	}
}

// The task stays ready until the worker claims it, so the second tick must
// recognise its own binding rather than dispatching the task twice.
func TestASecondTickDoesNotDispatchATaskItAlreadyPrompted(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes for one task", len(got))
	}
	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
}

// The dispatcher stops at review: it announces the task once and touches no
// review verb, ever.
func TestReviewIsAnnouncedOnceAndNeverActedOn(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"}`)
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	for i := 0; i < 2; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("announced review %d times: %v", len(got), got)
	}
	for _, verb := range []string{"task approve", "task reject", "note add"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran a review verb: %v", got)
		}
	}
	if !l.bindings[0].Notified {
		t.Fatal("the binding does not remember that review was announced")
	}
}

// A board that cannot be reached is a loud failure and an idle tick, never a
// guess at state and never an empty queue.
func TestAnUnreachableBoardFailsTheTickWithoutSpawning(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "htask", `echo "htask: the daemon is not answering" >&2; exit 1`)

	err := l.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the daemon is not answering") {
		t.Fatalf("want the board's own message, got %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("spawned with no board to read: %v", got)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
}

// A worker that never claims is re-prompted once the claim times out, and
// the prompt is a nudge rather than the goal: a slash command that long
// cannot arrive this way.
func TestAnUnclaimedWorkerIsNudgedAfterTheClaimTimeout(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)
	l.Now = func() time.Time { return clock.Add(10 * time.Minute) }

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	prompts := calls(t, f, "agent prompt")
	if len(prompts) != 1 {
		t.Fatalf("prompted %d times: %v", len(prompts), prompts)
	}
	if strings.Contains(prompts[0], "/goal") {
		t.Fatalf("the nudge carries a goal: %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "7") {
		t.Fatalf("the nudge does not name the task: %q", prompts[0])
	}
	if l.bindings[0].Prompts != 2 || !l.bindings[0].PromptedAt.Equal(clock.Add(10*time.Minute)) {
		t.Fatalf("binding: %+v", l.bindings[0])
	}
}

// A pane that is gone drops its binding and nothing else. Releasing the
// lease is the board's own sweep, and a second writer racing it is the bug.
func TestAGonePaneOnlyDropsItsBinding(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	for _, verb := range []string{"task release", "sweep", "pane close"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("a gone pane triggered %q: %v", verb, got)
		}
	}
}
