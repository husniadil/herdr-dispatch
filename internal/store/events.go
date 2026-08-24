package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
)

// Event is one state change of this dispatcher's own, as §8.1 shapes it:
// `{id, at, actor, project, entity, kind, detail}`, plus the §8.1 name the
// three parts spell out, `dispatch.<entity>.<kind>`.
//
// What is HERE is what exists nowhere else. A board fact — a task claimed,
// submitted, approved — is htask's own trail and is not copied into this one:
// the events this plugin owns are the ones about a binding, a reservation and
// a pane, which nothing outside this daemon ever sees.
type Event struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Entity is what the event is about: a task, a worker, or an action the
	// policy gate parked.
	Entity string `json:"entity"`
	// EntityID is the board's task id for a task or a worker event, and the
	// parked action's id for a parked one.
	EntityID string `json:"entity_id"`
	Project  string `json:"project,omitempty"`
	// AtMS is Unix milliseconds (§5.3). It is `at` on the wire, which is
	// the name §8.1 gives it.
	AtMS  int64  `json:"at"`
	Actor string `json:"actor"`
	Kind  string `json:"kind"`
	// Detail is verb-specific and small on purpose: the pane a worker came
	// up in, the reason a prompt was sent, the number an operator reads. A
	// consumer that wants more asks the board.
	Detail map[string]any `json:"detail,omitempty"`
}

// The entities this plugin has events about.
const (
	// EntityTask is a board task as this dispatcher acts on it: reserved
	// for a worker, or given back.
	EntityTask = "task"
	// EntityWorker is a pane this dispatcher brought up and what became of
	// it.
	EntityWorker = "worker"
	// EntityParked is an action the policy gate deferred (§9.3).
	EntityParked = "parked"
)

// MaxEvents is how many events the trail keeps. The whole document is written
// on every change, so an unbounded trail makes every save slower for as long
// as the daemon runs; the newest are kept because a consumer resumes from
// where it left off and an operator asks what happened last night.
//
// A consumer that cannot afford to miss one follows the stream (§8.2) rather
// than polling a window: `events --follow` is handed every event as it is
// written, whatever the trail then does with it.
const MaxEvents = 1000

// NewEventID is a sortable id: the millisecond the event was written, then
// eight random hex digits so two in the same millisecond are still two. It is
// the same shape a parked action's id has, and for the same reason — this is
// a name for one row in one operator's own file.
func NewEventID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ev-%013d-00000000", now.UnixMilli())
	}
	return fmt.Sprintf("ev-%013d-%s", now.UnixMilli(), hex.EncodeToString(b[:]))
}

// Rotate is the trail bounded to MaxEvents, newest kept.
func Rotate(trail []Event) []Event {
	if len(trail) <= MaxEvents {
		return trail
	}
	return append([]Event{}, trail[len(trail)-MaxEvents:]...)
}

// EventFilter is the slice of the trail a reader asked for.
type EventFilter struct {
	// SinceID resumes strictly after that event.
	SinceID string
	// SinceMS resumes strictly after that millisecond.
	SinceMS int64
	// Limit stops after that many; zero is all of them.
	Limit int
}

// Select is the trail a reader asked for, oldest first.
//
// A --since id the trail does not carry is REFUSED rather than read as no
// filter at all. A consumer resuming from an id the rotation has passed would
// otherwise be handed the whole window again and take it for the tail of its
// own stream, which is the one failure a resumable trail exists to prevent.
func Select(trail []Event, f EventFilter) ([]Event, error) {
	from := 0
	if f.SinceID != "" {
		found := false
		for i, ev := range trail {
			if ev.ID == f.SinceID {
				from, found = i+1, true
				break
			}
		}
		if !found {
			return nil, codes.Refusef(codes.Invalid,
				"the trail has no event %s to resume from: it holds at most %d events and this one has rotated past, so read from the beginning or pass a millisecond",
				f.SinceID, MaxEvents)
		}
	}
	out := []Event{}
	for _, ev := range trail[from:] {
		if f.SinceMS > 0 && ev.AtMS <= f.SinceMS {
			continue
		}
		out = append(out, ev)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}
