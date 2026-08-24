package store

import (
	"testing"
	"time"
)

// The whole of an event survives a round trip, and the trail is what the
// document was written with.
func TestAnEventSurvivesARoundTrip(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	want := Event{
		ID:       NewEventID(at),
		Name:     "dispatch.worker.spawned",
		Entity:   EntityWorker,
		EntityID: "01AAA",
		Project:  "/repo",
		AtMS:     at.UnixMilli(),
		Actor:    "plugin:hdis",
		Kind:     "spawned",
		Detail:   map[string]any{"pane": "wM:p9"},
	}
	if err := s.Save(State{Events: []Event{want}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(state.Events) != 1 {
		t.Fatalf("loaded %d events, want 1: %+v", len(state.Events), state.Events)
	}
	got := state.Events[0]
	if got.ID != want.ID || got.Name != want.Name || got.Entity != want.Entity ||
		got.EntityID != want.EntityID || got.Project != want.Project ||
		got.AtMS != want.AtMS || got.Actor != want.Actor || got.Kind != want.Kind {
		t.Fatalf("event came back as %+v, want %+v", got, want)
	}
	if got.Detail["pane"] != "wM:p9" {
		t.Fatalf("detail came back as %+v", got.Detail)
	}
}

// Two events in the same millisecond are still two.
func TestTwoEventsInOneMillisecondAreTwoIDs(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if a, b := NewEventID(at), NewEventID(at); a == b {
		t.Fatalf("both events got the id %s", a)
	}
}

// An event id sorts by the millisecond it was written in, which is what makes
// --since <id> a position in the trail rather than a search.
func TestEventIDsSortByTime(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first, second := NewEventID(at), NewEventID(at.Add(time.Millisecond))
	if !(first < second) {
		t.Fatalf("%s does not sort before %s", first, second)
	}
}

// The trail is bounded. A daemon that runs for months would otherwise rewrite
// a document that only grows, and the whole document is written on every
// change.
func TestTheTrailKeepsTheNewestAndDropsTheRest(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	trail := make([]Event, 0, MaxEvents+10)
	for i := 0; i < MaxEvents+10; i++ {
		trail = append(trail, Event{
			ID:   NewEventID(at.Add(time.Duration(i) * time.Millisecond)),
			Name: "dispatch.worker.spawned",
			AtMS: at.Add(time.Duration(i) * time.Millisecond).UnixMilli(),
		})
	}
	kept := Rotate(trail)
	if len(kept) != MaxEvents {
		t.Fatalf("kept %d events, want %d", len(kept), MaxEvents)
	}
	if kept[len(kept)-1].ID != trail[len(trail)-1].ID {
		t.Fatalf("the newest event was dropped: last kept is %s", kept[len(kept)-1].ID)
	}
	if kept[0].ID != trail[10].ID {
		t.Fatalf("the oldest kept is %s, want %s", kept[0].ID, trail[10].ID)
	}
}

// A trail shorter than the bound is left exactly as it is.
func TestAShortTrailIsNotRotated(t *testing.T) {
	trail := []Event{{ID: "ev-1"}, {ID: "ev-2"}}
	if got := Rotate(trail); len(got) != 2 || got[0].ID != "ev-1" {
		t.Fatalf("a short trail came back as %+v", got)
	}
}

// selected is Select for the cases that expect it to answer.
func selected(t *testing.T, trail []Event, f EventFilter) []Event {
	t.Helper()
	got, err := Select(trail, f)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	return got
}

func trail(t *testing.T) []Event {
	t.Helper()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	return []Event{
		{ID: "ev-0000000000001-aaaaaaaa", AtMS: at.UnixMilli(), Name: "dispatch.task.reserved", EntityID: "01AAA", Project: "/one"},
		{ID: "ev-0000000000002-bbbbbbbb", AtMS: at.Add(time.Millisecond).UnixMilli(), Name: "dispatch.worker.spawned", EntityID: "01AAA", Project: "/one"},
		{ID: "ev-0000000000003-cccccccc", AtMS: at.Add(2 * time.Millisecond).UnixMilli(), Name: "dispatch.worker.retired", EntityID: "01BBB", Project: "/two"},
	}
}

// With no filter the whole trail comes back, oldest first: a consumer that
// passes no --since is asking for everything there is.
func TestNoFilterIsTheWholeTrailOldestFirst(t *testing.T) {
	got := selected(t, trail(t), EventFilter{})
	if len(got) != 3 || got[0].ID != "ev-0000000000001-aaaaaaaa" {
		t.Fatalf("the whole trail came back as %+v", got)
	}
}

// --since <id> resumes strictly after that event, so a consumer that saved the
// last id it saw is not handed it again.
func TestSinceAnIDResumesAfterIt(t *testing.T) {
	got := selected(t, trail(t), EventFilter{SinceID: "ev-0000000000001-aaaaaaaa"})
	if len(got) != 2 || got[0].ID != "ev-0000000000002-bbbbbbbb" {
		t.Fatalf("resuming came back as %+v", got)
	}
}

// --since <ms> resumes strictly after that millisecond.
func TestSinceAMillisecondResumesAfterIt(t *testing.T) {
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	got := selected(t, trail(t), EventFilter{SinceMS: at.Add(time.Millisecond).UnixMilli()})
	if len(got) != 1 || got[0].ID != "ev-0000000000003-cccccccc" {
		t.Fatalf("resuming came back as %+v", got)
	}
}

// A limit takes the OLDEST of what is left rather than the newest, because a
// bounded read is a page of the stream and the next page resumes from its last
// id.
func TestALimitTakesThePageFromWhereTheReadStarts(t *testing.T) {
	got := selected(t, trail(t), EventFilter{Limit: 2})
	if len(got) != 2 || got[1].ID != "ev-0000000000002-bbbbbbbb" {
		t.Fatalf("a limited read came back as %+v", got)
	}
}

// An unknown --since id is refused rather than answered with the whole trail:
// a consumer resuming from an id the trail has rotated past would otherwise be
// handed every event again and believe it had caught up.
func TestAnUnknownSinceIDIsRefused(t *testing.T) {
	if _, err := Select(trail(t), EventFilter{SinceID: "ev-0000000009999-zzzzzzzz"}); err == nil {
		t.Fatal("resuming from an id the trail does not carry was accepted")
	}
}
