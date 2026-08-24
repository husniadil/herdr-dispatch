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
	// Rearm clears the announcement and the verification on a worker's
	// binding, because its task came back out of review. If it is submitted
	// again, both are due again.
	Rearm Kind = "rearm"
)

// KindWorker is the only lane a binding names. A binding written before the
// kind existed carries none, and reads as a worker.
const KindWorker = "worker"

// Reasons carried on Prompt and Retire actions, for the operator's log.
const (
	ReasonUnclaimed = "goal delivered but never claimed"
	ReasonStalled   = "the board still has the task in doing and the pane is idle"
	ReasonRejected  = "the task is back in doing and the board's row carries review feedback"
	ReasonTakenOver = "task was claimed by another pane"
	ReasonTerminal  = "task is terminal"
	// ReasonSelfReview carries the second condition into the pane that did
	// the work. It is not a nudge: the worker is not stalled, it has
	// submitted, and what it is asked for is the mechanical check its own
	// report cannot make for itself.
	ReasonSelfReview = "the task reached review and its submission has had no self-review shot"
)

// Task is the slice of a board row the core decides on. ClaimedBy is the
// claiming pane id, or empty while unclaimed.
type Task struct {
	ID        string
	Status    string
	ClaimedBy string
	// Project is the board the row is filed on. It is what the ready list
	// is shared out by, so one busy board cannot take every worker slot.
	Project string
	// Feedback is the review gate's words on the row, present only between a
	// rejection and the next submission. It is the one fact that separates a
	// worker waiting on a rejection from one that stopped without
	// submitting: both sit idle on a doing row.
	Feedback string
	// Codex is whether the worker this task would get launches through the
	// proxy. It is the config's word, resolved per project before the tick,
	// because the core knows nothing about profiles — and only a worker
	// that routes through the proxy is gated on the proxy's quota.
	Codex bool
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
	// Kind is the lane the pane was brought up for. There is one, KindWorker,
	// and an empty kind reads as it, so a binding written before the field
	// existed reads as what it was.
	Kind string
	// Worktree is the checkout the pane was given to work in, which is
	// removed when this binding is dropped. A worker's is thrown away with
	// its commits already safe on Branch.
	Worktree string
	// Tab is the tab this dispatcher opened the pane in. It is what a
	// restart needs in order to give the tab back: a tab holds up to the
	// pane cap of workers, so which tab a pane is in cannot be re-derived
	// from the pane alone once the process that opened it is gone.
	Tab string
	// Branch is the branch a worker's commits live on, named for the task.
	// It outlives the checkout, so it is what tells the operator where the
	// work is.
	Branch string
	// Verified says a self-review shot has been SENT for the submission the
	// binding is currently holding. Sent, not received: §11.4 forbids reading
	// a successful `agent prompt` as delivery, so this alone does not close
	// the shot — see selfReviewDue. Rearm clears it when the task leaves
	// review.
	Verified bool
}

// Snapshot is everything a tick may know. Agents maps pane id to Herdr's
// agent_status for every LIVE pane, whether or not an agent is attached to
// it yet; a bound pane missing from the map is gone. A worker pane herdr has
// not registered an agent in reads as "unknown", which is a worker still
// coming up rather than a pane to unbind.
type Snapshot struct {
	Ready    []string // ready task ids, in dispatch order
	Tasks    map[string]Task
	Agents   map[string]string
	Bindings []Binding
	Now      time.Time
	// Quota is the proxy launcher's word about the account a codex worker
	// would spend. A zero Quota is an unknown one, which gates nothing.
	Quota Quota
}

// Policy is the knobs a decision depends on.
type Policy struct {
	MaxWorkers   int
	ClaimTimeout time.Duration
	MaxPrompts   int
	// Verify turns the verification lane on: a submission earns one
	// self-review shot in the pane that produced it. Off, a task reaching
	// review is announced and nothing else.
	Verify bool
	// MaxUsedPercent is the share of its window the proxy's serving account
	// may already have spent before a codex spawn is refused. Zero is no
	// threshold, and the limit_reached flag still gates on its own.
	MaxUsedPercent int
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
		if known && Terminal(t.Status) {
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
			// The shot goes to the pane that did the work, so it costs no
			// slot and lands on a prefix still warm from the submission.
			if p.Verify && selfReviewDue(b, status, s.Now, p) {
				out = append(out, Action{Kind: Prompt, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonSelfReview})
			}
			continue
		}
		// Out of review with an announcement or a verification still on the
		// binding: the submission those belonged to is gone, and a new one
		// earns both again.
		if known && (b.Notified || b.Verified) {
			out = append(out, Action{Kind: Rearm, TaskID: b.TaskID, Pane: b.Pane})
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
			reason := ReasonStalled
			if t.Feedback != "" {
				reason = ReasonRejected
			}
			out = append(out, Action{Kind: Prompt, TaskID: b.TaskID, Pane: b.Pane, Reason: reason})
		}
	}

	for _, id := range shareOut(s.Ready, s.Tasks) {
		if live >= p.MaxWorkers {
			break
		}
		// The quota gate is the codex provider's alone, and it skips this
		// task rather than ending the loop: a claude task behind a gated
		// one spends nothing on the proxy and still gets its slot.
		if s.Tasks[id].Codex && QuotaRefusal(s.Quota, p) != "" {
			continue
		}
		out = append(out, Action{Kind: Spawn, TaskID: id})
		live++
	}
	return out
}

// shareOut deals the ready list round-robin by project, so a board offering
// more work than there are slots cannot take them all while another board
// waits. Projects keep the order they first appear in and their tasks keep
// the order the board offered them, so a machine serving one board gets the
// list back exactly as it came.
func shareOut(readyIDs []string, tasks map[string]Task) []string {
	var order []string
	byProject := make(map[string][]string)
	for _, id := range readyIDs {
		project := tasks[id].Project
		if _, seen := byProject[project]; !seen {
			order = append(order, project)
		}
		byProject[project] = append(byProject[project], id)
	}
	if len(order) < 2 {
		return readyIDs
	}
	out := make([]string, 0, len(readyIDs))
	for len(out) < len(readyIDs) {
		for _, project := range order {
			queue := byProject[project]
			if len(queue) == 0 {
				continue
			}
			out = append(out, queue[0])
			byProject[project] = queue[1:]
		}
	}
	return out
}

// selfReviewDue reports whether the submission in hand still owes its
// self-review shot.
//
// A shot never sent is due. A shot that WAS sent is due again only while herdr
// still calls the worker idle, and that clause is §11.4's: a plugin MUST NOT
// treat a successful `agent prompt` as delivery, and idle is the one thing on
// this pane that says the text reached nothing. A worker that got the
// condition is working, so it is never asked twice; a worker whose prompt
// herdr accepted and nothing ever saw is asked again instead of losing the
// shot in silence with the board still green.
//
// The wait and the bound are the ones the unclaimed nudge already uses, for
// the same reason: a pane idle for reasons of its own must not be prompted
// forever, and a worker mid-turn must not be interrupted by a second copy.
func selfReviewDue(b Binding, status string, now time.Time, p Policy) bool {
	if !b.Verified {
		return true
	}
	if status != "idle" || b.Prompts >= p.MaxPrompts {
		return false
	}
	return now.Sub(b.PromptedAt) >= p.ClaimTimeout
}

// Terminal is a board status the dispatcher has nothing left to do about.
func Terminal(status string) bool {
	return status == "done" || status == "cancelled"
}
