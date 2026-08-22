package loop

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"
)

// restarted is the same dispatcher started again: a fresh Loop with nothing
// in memory, pointed at the store the one before it wrote.
func restarted(t *testing.T, l *Loop) *Loop {
	t.Helper()
	next := &Loop{
		Board:    l.Board,
		Herdr:    l.Herdr,
		Spawn:    l.Spawn,
		Config:   l.Config,
		Policy:   l.Policy,
		Store:    l.Store,
		BasePane: l.BasePane,
		Now:      l.Now,
		Log:      log.New(io.Discard, "", 0),
	}
	return next
}

// The one mapping that exists nowhere else until the worker claims outlives
// the process that made it.
func TestAPromptedButUnclaimedWorkerIsStillTrackedAfterARestart(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-adopted %d bindings, want 1", n)
	}
	b := next.Bindings()
	if len(b) != 1 || b[0].TaskID != "01AAA" || b[0].Pane != "wM:p9" || b[0].Prompts != 1 {
		t.Fatalf("bindings: %+v", b)
	}
	// The claim timeout keeps running against the time the goal was really
	// delivered, not against the restart.
	if !b[0].PromptedAt.Equal(clock) {
		t.Fatalf("prompted_at came back as %s, want %s", b[0].PromptedAt, clock)
	}
	if next.Readopted() != 1 {
		t.Fatalf("readopted: %d", next.Readopted())
	}
}

// The orphan split: before the bindings were durable, a restart forgot the
// pane and the next tick dispatched the same task into a second one.
func TestARestartDoesNotDispatchATaskWhoseWorkerPaneIsStillAlive(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	// The task is still todo and still ready — a worker has not claimed yet
	// — which is exactly the shape that produced the second pane.
	if err := next.Tick(context.Background()); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes for one task: %v", len(got), got)
	}
	if got := next.Bindings(); len(got) != 1 || got[0].Pane != "wM:p9" {
		t.Fatalf("bindings: %+v", got)
	}
}

// A pane herdr no longer lists is a worker that is gone. The binding is
// dropped with a word to the operator, never acted on blindly.
func TestARestartDropsABindingWhosePaneIsGone(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)

	next := restarted(t, l)
	var said strings.Builder
	next.Log = log.New(&said, "", 0)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 || len(next.Bindings()) != 0 {
		t.Fatalf("re-adopted %d: %+v", n, next.Bindings())
	}
	if !strings.Contains(said.String(), "wM:p9") {
		t.Fatalf("the operator was not told which pane went: %q", said.String())
	}
	// And the drop is durable: a second restart does not find it again.
	again := restarted(t, l)
	if _, err := again.Adopt(context.Background()); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if got := again.Bindings(); len(got) != 0 {
		t.Fatalf("a dropped binding came back: %+v", got)
	}
}

// A task the board has finished with, or handed to someone else, is not this
// dispatcher's to drive any more.
func TestARestartDropsABindingWhoseTaskMovedOn(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		// A finished task takes its pane with it; a task another pane holds
		// leaves the pane alone, because whose worker it is now is the
		// board's answer and not a restart's.
		retires bool
	}{
		{name: "done", row: `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`, retires: true},
		{name: "claimed by another pane", row: `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:pZ"},"ready":false,"dependents":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newLoop(t)
			if err := l.Tick(context.Background()); err != nil {
				t.Fatalf("tick: %v", err)
			}
			f.Write(t, "panes.json", panesWith("working"))
			f.Write(t, "get.json", tc.row)

			next := restarted(t, l)
			var said strings.Builder
			next.Log = log.New(&said, "", 0)
			n, err := next.Adopt(context.Background())
			if err != nil {
				t.Fatalf("adopt: %v", err)
			}
			if n != 0 || len(next.Bindings()) != 0 {
				t.Fatalf("re-adopted %d: %+v", n, next.Bindings())
			}
			if !strings.Contains(said.String(), "01AAA") {
				t.Fatalf("the operator was not told which task went: %q", said.String())
			}
			want := 0
			if tc.retires {
				want = 1
			}
			if got := calls(t, f, "pane close"); len(got) != want {
				t.Fatalf("closed %d panes, want %d: %v", len(got), want, got)
			}
		})
	}
}

// A board that cannot answer for one task is not evidence that the task is
// gone. The binding is held, exactly as a tick holds it.
func TestARestartHoldsABindingWhoseRowCannotBeRead(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))
	f.Bin(t, "htask", `case "$1 $2" in
"task get") echo "htask: the daemon is not answering" >&2; exit 1 ;;
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
*) echo '{}' ;;
esac`)

	next := restarted(t, l)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-adopted %d bindings, want the unreadable one held", n)
	}
}

// Herdr unreachable means the pane half of the check cannot be made at all.
// Adopting on a guess is how a live worker gets a second pane, so nothing is
// adopted, the failure is loud, and the store is left for the next start.
func TestARestartWithNoHerdrAdoptsNothingAndKeepsTheStore(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Bin(t, "herdr", `echo "herdr: no server" >&2; exit 1`)

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err == nil {
		t.Fatal("a restart with no herdr adopted quietly")
	}
	if got := next.Bindings(); len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
	held, err := l.Store.Load()
	if err != nil || len(held.Bindings) != 1 {
		t.Fatalf("the store was not left intact: %+v (%v)", held, err)
	}
}

// The claim timeout is measured from the moment the goal was delivered, and
// a restart in between neither restarts nor skips it.
func TestTheClaimTimeoutKeepsRunningAcrossARestart(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	later := clock.Add(6 * time.Minute)
	next.Now = func() time.Time { return later }
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := next.Tick(context.Background()); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	prompts := calls(t, f, "agent prompt")
	if len(prompts) != 1 {
		t.Fatalf("the claim timeout did not carry across the restart: %v", prompts)
	}
	if got := next.Bindings(); len(got) != 1 || got[0].Prompts != 2 {
		t.Fatalf("bindings: %+v", got)
	}
}

// A store nobody has written is a first start, not a failure.
func TestAFirstStartAdoptsNothingAndSaysSo(t *testing.T) {
	l, _ := newLoop(t)
	n, err := l.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 || l.Readopted() != 0 {
		t.Fatalf("re-adopted %d", n)
	}
}

// The fourth shape of the restart window. Task 28 closed a reservation with
// no binding, a live pane with no binding, and a worktree no binding names;
// this is the one left: a binding whose task finished while the daemon was
// down. Dropping it is right, but the pane it named is one this daemon
// opened, and nothing else will ever close it.
func TestARestartRetiresThePaneOfABindingWhoseTaskFinished(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("idle"))
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`)

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := next.Bindings(); len(got) != 0 {
		t.Fatalf("the binding survived a finished task: %+v", got)
	}
	// Unbound is not enough: the pane has to be gone.
	got := calls(t, f, "pane close")
	if len(got) != 1 || !strings.Contains(got[0], "wM:p9") {
		t.Fatalf("the pane of a finished task was left open: %v", got)
	}
}

// The two edges the retire must not cross: a pane this daemon never opened,
// and a pane whose task is still unfinished.
func TestARestartRetiresNoPaneItDidNotStrand(t *testing.T) {
	t.Run("a pane no binding names is never touched", func(t *testing.T) {
		l, f := newLoop(t)
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		// A second pane herdr lists that this daemon never opened, alongside
		// the one it did, whose task has finished.
		f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
			`{"pane_id":"wM:p9","agent":"claude","agent_status":"idle","focused":false,"revision":1},`+
			`{"pane_id":"wM:pX","agent":"claude","agent_status":"working","focused":false,"revision":1}]}}`)
		f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`)

		next := restarted(t, l)
		if _, err := next.Adopt(context.Background()); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		for _, c := range calls(t, f, "pane close") {
			if strings.Contains(c, "wM:pX") {
				t.Fatalf("a restart closed a pane it never opened: %v", c)
			}
		}
	})

	t.Run("an unfinished task keeps its pane", func(t *testing.T) {
		l, f := newLoop(t)
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		f.Write(t, "panes.json", panesWith("working"))
		f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)

		next := restarted(t, l)
		n, err := next.Adopt(context.Background())
		if err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if n != 1 {
			t.Fatalf("re-adopted %d bindings, want 1", n)
		}
		if got := calls(t, f, "pane close"); len(got) != 0 {
			t.Fatalf("a restart closed a live worker's pane: %v", got)
		}
	})
}
