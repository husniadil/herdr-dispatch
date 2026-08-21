package loop

import (
	"context"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/decide"
)

// The whole point of the on-demand verb: it reserves the task and comes back,
// because bringing a worker up runs to minutes and no caller can wait that
// long. Nothing is split, started or typed until the tick that follows.
func TestDispatchReservesTheTaskAndReturnsBeforeAnythingIsSpawned(t *testing.T) {
	l, f := newLoop(t)

	res, err := l.Dispatch(context.Background(), "7")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.TaskID != "01AAA" || res.Seq != 7 || res.Project != "/src/p" {
		t.Fatalf("reservation: %+v", res)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("dispatch spawned before returning: %v", got)
	}
	if got := calls(t, f, "agent start"); len(got) != 0 {
		t.Fatalf("dispatch started an agent before returning: %v", got)
	}
	if got := l.Pending(); len(got) != 1 || got[0] != "01AAA" {
		t.Fatalf("pending: %v", got)
	}

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("the tick split %d panes for the reserved task", len(got))
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("the reservation outlived its spawn: %v", got)
	}
}

// The reservation and the board's own ready row are the same task. Feeding
// both into one tick must still produce one worker.
func TestADispatchedTaskIsNotAlsoSpawnedByTheWatchingLoop(t *testing.T) {
	l, f := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "01AAA"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes for one task: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}
}

func TestDispatchRefusesATaskTheBoardWillNotHandOut(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p4"},"ready":false,"dependents":[]}`)

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.Of(err), codes.NotReady; got != want {
		t.Fatalf("dispatch of a claimed task = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesATaskTheBoardDoesNotHave(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Bin(t, "htask", `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") echo 'NOT_FOUND: no task 999' >&2; exit 1 ;;
*) echo '{}' ;;
esac`)

	_, err := l.Dispatch(context.Background(), "999")
	if got, want := codes.Of(err), codes.NotFound; got != want {
		t.Fatalf("dispatch of a missing task = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesWhenTheFleetIsAtMaxWorkers(t *testing.T) {
	l, _ := newLoop(t)
	l.Policy.MaxWorkers = 1
	l.bindings = []decide.Binding{{TaskID: "01ZZZ", Pane: "wM:p8", PromptedAt: clock, Prompts: 1}}

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.Of(err), codes.AtCapacity; got != want {
		t.Fatalf("dispatch at capacity = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesWithoutABasePane(t *testing.T) {
	l, _ := newLoop(t)
	l.BasePane = ""

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.Of(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch with no base pane = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesATaskItIsAlreadyDriving(t *testing.T) {
	l, _ := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7"); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.Of(err), codes.AlreadyDispatched; got != want {
		t.Fatalf("second dispatch = %v (%q), want %q", err, got, want)
	}
}

// A reservation the board has taken back is dropped rather than spawned: the
// worker that claimed it in the meantime is the one doing the work.
func TestAReservationTheBoardTookBackIsDroppedAtTheNextTick(t *testing.T) {
	l, f := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p4"},"ready":false,"dependents":[]}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("a task the board took back was still spawned: %v", got)
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("pending: %v", got)
	}
}

func TestStatusReportsTheBindingsAndWhatHerdrSaysAboutTheirPanes(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.BasePane != "wM:p1" || st.MaxWorkers != 2 {
		t.Fatalf("status: %+v", st)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	w := st.Workers[0]
	if w.TaskID != "01AAA" || w.Seq != 7 || w.Title != "do the thing" || w.Project != "/src/p" {
		t.Fatalf("worker row: %+v", w)
	}
	if w.Pane != "wM:p9" || w.AgentStatus != "working" || !w.PaneAlive {
		t.Fatalf("worker pane: %+v", w)
	}
	if w.Prompts != 1 || !w.PromptedAt.Equal(clock) || w.Notified {
		t.Fatalf("worker delivery: %+v", w)
	}
}

func TestStatusSaysAPaneIsGoneWhenHerdrNoLongerListsIt(t *testing.T) {
	l, _ := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].PaneAlive {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if st.Workers[0].AgentStatus != "" {
		t.Fatalf("a pane herdr does not list has no agent_status: %+v", st.Workers[0])
	}
}

// Dispatch runs on a door's goroutine while the tick runs on the daemon's.
// The race detector is the assertion.
func TestDispatchAndStatusAreSafeAlongsideATick(t *testing.T) {
	l, _ := newLoop(t)
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			l.Status(ctx)
			l.Dispatch(ctx, "7")
			l.Bindings()
			l.Pending()
		}
	}()
	for i := 0; i < 3; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Errorf("tick: %v", err)
		}
	}
	<-done
}
