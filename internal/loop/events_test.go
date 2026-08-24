package loop

import (
	"context"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/store"
)

// names is every event name in the trail, oldest first.
func names(t *testing.T, l *Loop) []string {
	t.Helper()
	evs, err := l.Events(store.EventFilter{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	out := []string{}
	for _, ev := range evs {
		out = append(out, ev.Name)
	}
	return out
}

// one is the first event of that name, and fails when there is none.
func one(t *testing.T, l *Loop, name string) store.Event {
	t.Helper()
	evs, err := l.Events(store.EventFilter{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, ev := range evs {
		if ev.Name == name {
			return ev
		}
	}
	t.Fatalf("no %s in the trail: %v", name, names(t, l))
	return store.Event{}
}

// A dispatch is a state change of this dispatcher's own — the reservation
// exists nowhere but here — so it is on the trail with the task it took.
func TestReservingATaskIsOnTheTrail(t *testing.T) {
	l, _ := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	ev := one(t, l, "dispatch.task.reserved")
	if ev.Entity != store.EntityTask || ev.EntityID != "01AAA" || ev.Kind != "reserved" {
		t.Fatalf("event: %+v", ev)
	}
	if ev.Project != "/src/p" || ev.Actor == "" || ev.AtMS != clock.UnixMilli() {
		t.Fatalf("event: %+v", ev)
	}
	if ev.Detail["seq"] != float64(7) && ev.Detail["seq"] != 7 {
		t.Fatalf("the event does not carry the number an operator reads: %+v", ev.Detail)
	}
}

// The one state change nothing else records: a pane came up for a task, and
// which pane it was.
func TestAWorkerComingUpIsOnTheTrail(t *testing.T) {
	l, _ := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	ev := one(t, l, "dispatch.worker.spawned")
	if ev.Entity != store.EntityWorker || ev.EntityID != "01AAA" {
		t.Fatalf("event: %+v", ev)
	}
	if ev.Detail["pane"] == nil || ev.Detail["pane"] == "" {
		t.Fatalf("the event names no pane: %+v", ev.Detail)
	}
	if ev.Detail["branch"] == nil {
		t.Fatalf("the event names no branch: %+v", ev.Detail)
	}
}

// The trail outlives the process, in the same document as what it is a trail
// of: the document is written whole, so an event cannot land without its
// change or a change without its event.
func TestTheTrailIsWrittenWithTheChangeItRecords(t *testing.T) {
	l, _ := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	state, err := l.Store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(state.Events) != 1 || state.Events[0].Name != "dispatch.task.reserved" {
		t.Fatalf("the store holds %+v", state.Events)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("the reservation and its event were not written together: %+v", state)
	}
}

// A restart reads the trail back rather than starting a new one: an operator
// asking what happened last night is asking across the restart.
func TestARestartKeepsTheTrail(t *testing.T) {
	l, _ := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	next, _ := newLoop(t)
	next.Store = l.Store
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := names(t, next); len(got) == 0 || got[0] != "dispatch.task.reserved" {
		t.Fatalf("the trail after a restart is %v", got)
	}
}

// §8.3's hook is fired for every event, and it is handed the event that went
// on the trail rather than a second rendering of it.
func TestEveryEventReachesTheHook(t *testing.T) {
	l, _ := newLoop(t)
	var seen []store.Event
	l.OnEvent = func(ev store.Event) { seen = append(seen, ev) }
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("the hook heard nothing")
	}
	trail, err := l.Events(store.EventFilter{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(seen) != len(trail) {
		t.Fatalf("the hook heard %d events and the trail holds %d", len(seen), len(trail))
	}
	for i := range trail {
		if seen[i].ID != trail[i].ID {
			t.Fatalf("the hook heard %s where the trail holds %s", seen[i].ID, trail[i].ID)
		}
	}
}

// A reservation given up on is the state change an operator most needs and
// the log is the only place it was said.
func TestAReservationGivenUpOnIsOnTheTrail(t *testing.T) {
	l, _ := newLoop(t)
	// A project mapped to a profile the config does not name is what spawn
	// refuses on every time, which is the failure the attempt bound exists
	// for.
	l.Config.Projects = map[string]string{"/src/p": "nope"}
	for i := 0; i < MaxSpawnAttempts+1; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	ev := one(t, l, "dispatch.task.reservation_dropped")
	if ev.EntityID != "01AAA" || ev.Entity != store.EntityTask {
		t.Fatalf("event: %+v", ev)
	}
}
