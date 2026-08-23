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
	// SpawnVerifier brings up a VERIFIER worker for a task this dispatcher's
	// own worker submitted: a fresh pane, the same spawn path, its own
	// binding. It reports findings and never approves or rejects.
	SpawnVerifier Kind = "spawn_verifier"
	// Rearm clears the announcement and the verification on a worker's
	// binding, because its task came back out of review. If it is submitted
	// again, both are due again.
	Rearm Kind = "rearm"
)

// The kinds of worker a binding may name. A binding written before the
// verification lane existed carries no kind, and reads as a worker.
const (
	KindWorker   = "worker"
	KindVerifier = "verifier"
)

// Reasons carried on Prompt and Retire actions, for the operator's log.
const (
	ReasonUnclaimed = "goal delivered but never claimed"
	ReasonStalled   = "the board still has the task in doing and the pane is idle"
	ReasonRejected  = "the task is back in doing and the board's row carries review feedback"
	ReasonTakenOver = "task was claimed by another pane"
	ReasonTerminal  = "task is terminal"
	ReasonVerified  = "verifier went idle, its findings are sent or they are not coming"
	ReasonSettled   = "the submission it was checking has left review"
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
	// Kind is which lane the pane was brought up for: KindWorker does the
	// task, KindVerifier checks what a worker submitted. Empty is a worker,
	// so a binding written before the lane existed reads as what it was.
	Kind string
	// Worktree is the checkout the pane was given to work in, which is
	// removed when this binding is dropped. Both lanes get one: a verifier's
	// is thrown away with everything in it, and a worker's is thrown away
	// with its commits already safe on Branch.
	Worktree string
	// Tab is the tab this dispatcher opened the pane in. It is what a
	// restart needs in order to give the tab back: a tab holds up to the
	// pane cap of workers, so which tab a pane is in cannot be re-derived
	// from the pane alone once the process that opened it is gone.
	Tab string
	// Branch is the branch a WORKER's commits live on, named for the task.
	// It outlives the checkout, so it is what tells a verifier which commit
	// was submitted and tells the operator where the work is. A verifier
	// has none: it works detached and commits nothing.
	Branch string
	// Verified is set on a WORKER's binding when a verifier has been
	// brought up for the submission it is currently holding. It is what
	// makes one submission earn one verifier, and Rearm clears it when the
	// task leaves review.
	Verified bool
}

// IsVerifier reports whether the binding names a verifier pane.
func (b Binding) IsVerifier() bool { return b.Kind == KindVerifier }

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
}

// Policy is the knobs a decision depends on.
type Policy struct {
	MaxWorkers   int
	ClaimTimeout time.Duration
	MaxPrompts   int
	// Verify turns the verification lane on. Off, a task reaching review is
	// announced and nothing else, which is what this dispatcher did before
	// the lane existed.
	Verify bool
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

	// A task that already has a verifier pane up gets no second one, whatever
	// the worker's binding remembers.
	verified := make(map[string]bool, len(s.Bindings))
	for _, b := range s.Bindings {
		if b.IsVerifier() {
			verified[b.TaskID] = true
		}
	}

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

		if b.IsVerifier() {
			// A verifier holds no claim on the board, is never nudged, and
			// announces nothing. It ends when the submission it was reading
			// is settled, or when it has gone quiet with the reading done.
			if known && t.Status != "review" {
				out = append(out, Action{Kind: Retire, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonSettled})
				continue
			}
			if status == "idle" && s.Now.Sub(b.PromptedAt) >= p.ClaimTimeout {
				out = append(out, Action{Kind: Retire, TaskID: b.TaskID, Pane: b.Pane, Reason: ReasonVerified})
				continue
			}
			live++
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
			if p.Verify && !b.Verified && !verified[b.TaskID] {
				out = append(out, Action{Kind: SpawnVerifier, TaskID: b.TaskID})
				verified[b.TaskID] = true
				live++
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

// Terminal is a board status the dispatcher has nothing left to do about.
func Terminal(status string) bool {
	return status == "done" || status == "cancelled"
}
