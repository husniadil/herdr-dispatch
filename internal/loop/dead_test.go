package loop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// deadAgentHerdr is the fake herdr of a pane that is still alive and whose
// agent is gone: `agent prompt` refuses with the code the real server answers
// when it holds no agent for the target, and everything else answers as
// usual. It is the shape measured live — a pane Herdr still lists, and no
// agent behind it — and the one the loop had no answer for.
const deadAgentHerdr = `case "$1 $2" in
"agent prompt")
  echo '{"error":{"code":"agent_not_found","message":"no agent in pane wM:p9"},"id":"cli:agent:prompt"}' >&2
  exit 1 ;;
esac
` + herdrScript

// deadWorker spawns a worker, then puts the board and Herdr in the state this
// bug was measured in: the row is claimed and in doing, the pane is alive and
// idle, and the agent behind it is gone. Every tick after this one asks for a
// nudge the pane can no longer take.
func deadWorker(t *testing.T) (*Loop, *testenv.Fake) {
	t.Helper()
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "get.json", doingRow(""))
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	idlePane(t, f)
	f.Bin(t, "herdr", deadAgentHerdr)
	return l, f
}

// tickAt runs one tick with the clock moved far enough past the last prompt
// for the stalled rule to ask for another one.
func tickAt(t *testing.T, l *Loop, after time.Duration) {
	t.Helper()
	l.Now = func() time.Time { return clock.Add(after) }
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick at %s: %v", after, err)
	}
}

// The whole of the fix, in one case. A worker whose agent died leaves a pane
// Herdr still lists, so nothing unbinds it and every tick re-prompts a pane
// that answers agent_not_found forever. On the third refusal in a row the
// worker is declared dead: the pane is retired through the same teardown a
// cancelled task uses, the binding is dropped, and the trail carries the
// reason.
//
// Nothing is said to the board. htask releases a row to its holder or to the
// operator and the holder is the dead worker's own agent principal, so a
// release from this daemon is refused every time; retiring the pane is what
// hands the task back, through htask's own pane-gone sweep.
func TestThreeAgentNotFoundsInARowDeclareTheWorkerDeadAndRetireItsPane(t *testing.T) {
	l, f := deadWorker(t)
	for i, after := range []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute} {
		tickAt(t, l, after)
		if i < 2 && len(l.Bindings()) != 1 {
			t.Fatalf("the worker was dropped after %d refusal(s): %+v", i+1, l.Bindings())
		}
	}

	if got := len(l.Bindings()); got != 0 {
		t.Fatalf("the dead worker still holds %d binding(s): %+v", got, l.Bindings())
	}
	// The board is not asked to release a row it holds for somebody else.
	if got := calls(t, f, "release"); len(got) != 0 {
		t.Fatalf("the daemon tried to release a row held by the dead worker: %v", got)
	}
	// Retiring closes the pane, or the tab when this dispatcher opened one
	// and the pane is the last in it. Either is the teardown; what matters
	// here is that it happened once and through the existing one.
	closed := append(calls(t, f, "pane close"), calls(t, f, "tab close")...)
	if len(closed) != 1 {
		t.Fatalf("the dead worker's pane was retired %d times: %v", len(closed), closed)
	}
	events, err := l.Events(store.EventFilter{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var died *store.Event
	for i, ev := range events {
		if ev.Name == "dispatch.worker.died" {
			died = &events[i]
		}
	}
	if died == nil {
		t.Fatalf("nothing on the trail says the worker died: %+v", events)
	}
	if died.EntityID != "01AAA" || died.Detail["pane"] != "wM:p9" {
		t.Fatalf("the died event names %q in pane %v", died.EntityID, died.Detail["pane"])
	}
	if died.Detail["prompts"] != DeadWorkerStreak || died.Detail["deaths"] != 1 {
		t.Fatalf("the died event carries %+v", died.Detail)
	}
}

// Two refusals are not three. A prompt that herdr refuses once is not
// evidence that the agent is gone — the streak is what makes it evidence —
// and a worker dropped on the first refusal is a live worker's task handed
// away.
func TestTwoAgentNotFoundsLeaveTheWorkerAlone(t *testing.T) {
	l, f := deadWorker(t)
	tickAt(t, l, 10*time.Minute)
	tickAt(t, l, 20*time.Minute)

	if got := len(l.Bindings()); got != 1 {
		t.Fatalf("two refusals dropped the binding: %+v", l.Bindings())
	}
	for _, verb := range []string{"release", "pane close", "tab close"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("two refusals reached %q: %v", verb, got)
		}
	}
	if got := l.DeathsOf("01AAA"); got != 0 {
		t.Fatalf("two refusals counted %d death(s)", got)
	}
}

// The streak is CONSECUTIVE. A prompt that lands says the agent is there, so
// whatever went wrong before it was not a dead agent, and the count starts
// again from nothing.
func TestAPromptThatLandsClearsTheAgentNotFoundStreak(t *testing.T) {
	l, f := deadWorker(t)
	tickAt(t, l, 10*time.Minute)
	tickAt(t, l, 20*time.Minute)
	// The agent is back, and the third prompt reaches it.
	f.Bin(t, "herdr", herdrScript)
	tickAt(t, l, 30*time.Minute)
	// Gone again, and this is the first of a new streak rather than the
	// third of the old one.
	f.Bin(t, "herdr", deadAgentHerdr)
	tickAt(t, l, 40*time.Minute)
	tickAt(t, l, 50*time.Minute)

	if got := len(l.Bindings()); got != 1 {
		t.Fatalf("the worker was declared dead across a prompt that landed: %+v", l.Bindings())
	}
	if got := calls(t, f, "release"); len(got) != 0 {
		t.Fatalf("the task was handed back: %v", got)
	}
}

// A task whose workers keep dying is not a task to keep spending workers on:
// at the cap the watching loop stops offering it a pane of its own, and the
// operator is the one who decides what happens next.
func TestATaskThatHasKilledTwoWorkersIsNotDispatchedAgain(t *testing.T) {
	l, f := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: MaxWorkerDeaths}}

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := len(l.Bindings()); got != 0 {
		t.Fatalf("a task that has killed %d workers was given another: %+v", MaxWorkerDeaths, l.Bindings())
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("a pane was split for it: %v", got)
	}
}

// The same task, asked for by name. A dispatch that refused without saying
// why would send the caller to doctor for a fact the refusal already has.
func TestDispatchOfATaskThatHasKilledTwoWorkersRefusesAndNamesTheCount(t *testing.T) {
	l, _ := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: MaxWorkerDeaths}}

	_, err := l.Dispatch(context.Background(), "01AAA", "")
	if err == nil {
		t.Fatal("the dispatch was accepted")
	}
	if got := codes.ReasonOf(err); got != codes.WorkersDied {
		t.Fatalf("refused as %q: %v", got, err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("the refusal does not name the count: %v", err)
	}
}

// The count is a record of workers dying and not a verdict on the task, so
// anyone else acting on the row clears it. A release with a note is one of
// the two acts that say a human has looked.
func TestAReleaseWithANoteByAnyoneElseClearsTheDeathCount(t *testing.T) {
	l, f := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: MaxWorkerDeaths, SinceMS: 1000}}
	f.Write(t, "events.json", `{"events":[{"id":"01EV1","entity":"task","entity_id":"01AAA","project":"/src/p","at":2000,"actor":"human","kind":"released"}],"count":1}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := l.DeathsOf("01AAA"); got != 0 {
		t.Fatalf("the count is still %d after somebody else released the task", got)
	}
}

// The other act: an amendment on the board. Same rule, and it is read off the
// board's own trail rather than guessed from the row.
func TestAnAmendmentOnTheBoardClearsTheDeathCount(t *testing.T) {
	l, f := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: MaxWorkerDeaths, SinceMS: 1000}}
	f.Write(t, "events.json", `{"events":[{"id":"01EV1","entity":"task","entity_id":"01AAA","project":"/src/p","at":2000,"actor":"agent:wM:p3","kind":"amended"}],"count":1}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := l.DeathsOf("01AAA"); got != 0 {
		t.Fatalf("the count is still %d after the row was amended", got)
	}
}

// This daemon's own release is what it does when it counts the death, so
// reading it back as somebody clearing the count would make the count
// unreachable: every death would clear itself on the next tick.
func TestThisDaemonsOwnReleaseDoesNotClearTheDeathCount(t *testing.T) {
	l, f := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: 1, SinceMS: 1000}}
	f.Write(t, "events.json", `{"events":[{"id":"01EV1","entity":"task","entity_id":"01AAA","project":"/src/p","at":2000,"actor":"plugin:hdis@wM:p1","kind":"released"}],"count":1}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := l.DeathsOf("01AAA"); got != 1 {
		t.Fatalf("this daemon's own release cleared the count: %d", got)
	}
}

// A worker on a task that has already lost one is still driven, and status is
// where an operator sees that this is the second attempt.
func TestStatusSaysHowOftenATasksWorkerHasAlreadyDied(t *testing.T) {
	l, _ := newLoop(t)
	l.deaths = []store.Death{{TaskID: "01AAA", Project: "/src/p", Count: 1}}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("status carries %d workers", len(st.Workers))
	}
	if st.Workers[0].Deaths != 1 {
		t.Fatalf("status says %d deaths on the task, want 1", st.Workers[0].Deaths)
	}
}
