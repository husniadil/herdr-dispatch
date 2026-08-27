package loop

import (
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// The §8.1 event names this dispatcher emits, `dispatch.<entity>.<kind>`.
//
// The set is what this binary alone knows. A task claimed, submitted or
// approved is a BOARD state change and stays on htask's trail: copying it
// here would be a second ledger of facts this plugin does not own, which the
// boundary with htask forbids. What is left is the binding, the reservation
// and the pane — the mapping that exists nowhere else until a worker claims.
const (
	// KindReserved is a task an on-demand dispatch took for the next tick.
	KindReserved = "reserved"
	// KindReservationDropped is a reservation given back: the board stopped
	// offering the task, or its spawn failed past MaxSpawnAttempts.
	KindReservationDropped = "reservation_dropped"
	// KindSpawned is a worker pane brought up for a task, with its goal
	// delivered.
	KindSpawned = "spawned"
	// KindAdopted is a live worker a restart took back.
	KindAdopted = "adopted"
	// KindPrompted is a nudge or a self-review shot delivered to a worker.
	KindPrompted = "prompted"
	// KindPromptRefused is a prompt this binary would not deliver, which is
	// the §5.9 render-time clamp refusing a condition that does not fit.
	KindPromptRefused = "prompt_refused"
	// KindReviewAnnounced is the operator being told a submission is
	// waiting, which is where this dispatcher stops.
	KindReviewAnnounced = "review_announced"
	// KindRetired is a worker pane this dispatcher closed.
	KindRetired = "retired"
	// KindGone is a worker pane that disappeared, whose binding is dropped.
	KindGone = "gone"
	// KindDeferred is the policy gate parking a call (§9.3).
	KindDeferred = "deferred"
	// KindResolved is the operator deciding a parked call, either way.
	KindResolved = "resolved"
	// KindFailed is a parked call the operator let through whose verb then
	// errored, which is the other outcome of the same decision.
	KindFailed = "failed"
)

// EventName is the §8.1 name for one entity and kind.
func EventName(entity, kind string) string { return "dispatch." + entity + "." + kind }

// OnBehalfOfOperator is the detail key operatorVerb writes. It is spelled the
// way both siblings spell it, because an operator reading three trails matches
// one word, and it is a shipped --json field the moment it appears in an
// event, so it is added and never repurposed (§6.2).
const OnBehalfOfOperator = "on_behalf_of_operator"

// operatorVerb marks an event whose authority is the operator's when a
// principal other than the operator performed it (§3.7). Nothing checks that
// the agent confirmed with the user first — a verb demanding proof of
// confirmation would be the refusal §3.7 removed wearing a different coat — so
// the trail is the whole accountability, and it is only honest if it says both
// who acted and that they acted for the operator.
//
// The actor recorded alongside it stays the calling principal, never `human`.
func operatorVerb(by string, detail map[string]any) map[string]any {
	if by == "human" {
		return detail
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail[OnBehalfOfOperator] = true
	return detail
}

// Events is the trail a reader asked for (§8.2).
func (l *Loop) Events(f store.EventFilter) ([]store.Event, error) {
	l.mu.Lock()
	trail := append([]store.Event{}, l.events...)
	l.mu.Unlock()
	return store.Select(trail, f)
}

// emit records one state change and hands it to the §8.3 hook.
//
// The caller must NOT hold mu. The event is appended and saved under the
// lock, so it goes out in the same write as the change it records, and the
// hook runs with the lock released: a hook is a process this daemon starts,
// and starting one under the lock would hold the tick behind it.
func (l *Loop) emit(entity, kind, entityID, project string, detail map[string]any) {
	l.mu.Lock()
	ev := l.emitLocked(entity, kind, entityID, project, detail)
	l.mu.Unlock()
	l.fire(ev)
}

// emitLocked appends one event and saves, with mu already held. It is what a
// mutation that is already under the lock uses, so the event and the change
// reach the document together; the caller fires the hook after unlocking.
//
// The actor is the board principal this daemon writes with, which carries its
// own pane: this plugin attributes nothing on the board (§3.4), and every
// event but one is its own daemon acting. The one is the resolution of a
// parked action, which §3.7 files under the principal that decided it;
// emitLockedAs is what writes that one.
func (l *Loop) emitLocked(entity, kind, entityID, project string, detail map[string]any) store.Event {
	return l.emitLockedAs(l.principal(), entity, kind, entityID, project, detail)
}

// emitLockedAs is emitLocked with the actor named rather than derived from
// this daemon. It exists for §3.7's trail duty and for nothing else: an
// operator verb a principal other than the operator performed records the
// CALLING principal, never this daemon and never `human`.
func (l *Loop) emitLockedAs(actor, entity, kind, entityID, project string, detail map[string]any) store.Event {
	ev := store.Event{
		ID:       store.NewEventID(l.now()),
		Name:     EventName(entity, kind),
		Entity:   entity,
		EntityID: entityID,
		Project:  project,
		AtMS:     l.now().UnixMilli(),
		Actor:    actor,
		Kind:     kind,
		Detail:   detail,
	}
	l.events = store.Rotate(append(l.events, ev))
	l.saveLocked()
	return ev
}

// fire hands one event to the §8.3 hook. A daemon with no hook wired has
// nothing to do here, and a hook that fails is the hook's problem: §8.3 makes
// it explicit that it MUST NOT fail the write that caused it, and the write
// has already happened by the time this runs.
func (l *Loop) fire(ev store.Event) {
	if l.OnEvent == nil {
		return
	}
	l.OnEvent(ev)
}

// projectOf is the board project a task is filed on, as the last tick read
// it, and empty when nothing here knows. An event with no project is still
// an event; a guessed one is a wrong fact on an audit trail.
func (l *Loop) projectOf(taskID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rows[taskID].Project
}
