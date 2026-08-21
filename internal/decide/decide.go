// Package decide is the dispatcher's pure core: given a snapshot of facts —
// board rows, worker pane states, the dispatcher's own bindings, the clock —
// it returns the actions to take. It spawns nothing and reads nothing; the
// adapters at the edge collect the snapshot before a tick and execute the
// actions after it.
package decide

import "time"

// Kind names what an adapter must do with an Action.
type Kind string

const (
	// Spawn brings up a fresh worker pane for a ready task and delivers its
	// goal: pane split, agent start, prompt, and a new binding.
	Spawn Kind = "spawn"
	// Prompt (re)delivers a goal to the already-bound pane and increments the
	// binding's prompt count.
	Prompt Kind = "prompt"
	// Notify tells the operator the task reached review and marks the binding
	// notified. The dispatcher's involvement with the task ends here until
	// the review gate moves it.
	Notify Kind = "notify"
	// Retire closes the bound pane and drops the binding.
	Retire Kind = "retire"
	// Unbind drops the binding without touching any pane, because the pane is
	// already gone. The board's own sweep returns the task to ready.
	Unbind Kind = "unbind"
	// GiveUp drops the binding and reports that the goal was delivered
	// MaxPrompts times and never claimed. The pane is retired with it.
	GiveUp Kind = "giveup"
)

// Reasons carried on Prompt and Retire actions, for the operator's log.
const (
	ReasonUnclaimed = "goal delivered but never claimed"
	ReasonStalled   = "worker went idle without submitting"
	ReasonTakenOver = "task was claimed by another pane"
	ReasonTerminal  = "task is terminal"
)

// Task is the slice of a board row the core decides on. ClaimedBy is the
// claiming pane id, or empty while unclaimed.
type Task struct {
	ID        string
	Status    string
	ClaimedBy string
}

// Binding is the dispatcher's one piece of own state: which pane it prompted
// for which task, when, how often, and whether review was already announced.
// Before the worker claims, this mapping exists nowhere else.
type Binding struct {
	TaskID     string
	Pane       string
	PromptedAt time.Time
	Prompts    int
	Notified   bool
}

// Snapshot is everything a tick may know. Agents maps pane id to Herdr's
// agent_status; a bound pane missing from the map is gone.
type Snapshot struct {
	Ready    []string // ready task ids, in dispatch order
	Tasks    map[string]Task
	Agents   map[string]string
	Bindings []Binding
	Now      time.Time
}

// Policy is the knobs a decision depends on.
type Policy struct {
	MaxWorkers   int
	ClaimTimeout time.Duration
	MaxPrompts   int
}

// Action is one thing for an adapter to do, in the order returned.
type Action struct {
	Kind   Kind
	TaskID string
	Pane   string
	Reason string
}

// Decide walks the bindings first — endings free worker slots — and then
// fills remaining capacity from Ready, in order.
func Decide(s Snapshot, p Policy) []Action {
	var out []Action
	live := 0

	for _, b := range s.Bindings {
		t, known := s.Tasks[b.TaskID]
		status, paneAlive := s.Agents[b.Pane]

		// Endings first: they do not hold a slot.
		if known && terminal(t.Status) {
			out = append(out, Action{Kind: Retire, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonTerminal})
			continue
		}
		if !paneAlive {
			out = append(out, Action{Kind: Unbind, TaskID: b.TaskID, Pane: b.Pane})
			continue
		}
		if known && t.ClaimedBy != "" && t.ClaimedBy != b.Pane {
			out = append(out, Action{Kind: Retire, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonTakenOver})
			continue
		}

		live++

		if known && t.Status == "review" {
			if !b.Notified {
				out = append(out, Action{Kind: Notify, TaskID: b.TaskID, Pane: b.Pane})
			}
			continue
		}
		if known && t.Status == "todo" {
			if s.Now.Sub(b.PromptedAt) < p.ClaimTimeout {
				continue
			}
			if b.Prompts >= p.MaxPrompts {
				out = append(out, Action{Kind: GiveUp, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonUnclaimed})
				live--
				continue
			}
			out = append(out, Action{Kind: Prompt, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonUnclaimed})
			continue
		}
		if known && t.Status == "doing" && status == "idle" {
			out = append(out, Action{Kind: Prompt, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonStalled})
		}
	}

	for _, id := range s.Ready {
		if live >= p.MaxWorkers {
			break
		}
		out = append(out, Action{Kind: Spawn, TaskID: id})
		live++
	}
	return out
}

func terminal(status string) bool {
	return status == "done" || status == "cancelled"
}
