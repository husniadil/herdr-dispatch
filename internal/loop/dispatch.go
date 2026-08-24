package loop

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// Reservation is what an accepted dispatch answers with. It is a promise that
// the next tick will bring a worker up for this task, and never a report that
// one is already running: a spawn runs to minutes and this call does not wait
// for it.
type Reservation struct {
	TaskID  string `json:"task"`
	Seq     int    `json:"seq"`
	Title   string `json:"title"`
	Project string `json:"project"`
}

// Worker is one binding, with what herdr says about the pane it names.
type Worker struct {
	TaskID  string `json:"task"`
	Seq     int    `json:"seq"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Pane    string `json:"pane"`
	// Kind is which lane the pane was brought up for. There is one, worker,
	// and this says so rather than leaving a caller to infer it.
	Kind string `json:"kind"`
	// Profile is the profile the worker was launched with, which its task's
	// priority may have routed to. It is what the binding recorded at spawn
	// rather than what the config says now, so a document rewritten since
	// does not change what this reports about a pane already running.
	Profile string `json:"profile,omitempty"`
	// AgentStatus is herdr's own word for the worker, or empty when herdr
	// no longer lists the pane at all.
	AgentStatus string `json:"agent_status"`
	// PaneAlive is whether herdr still lists the pane. A binding whose pane
	// is gone is dropped by the next tick.
	// Branch is where a worker's commits are, so an operator reading status
	// can find the work.
	Branch string `json:"branch,omitempty"`
	// Behind is whether the project's HEAD has moved past that branch since
	// the worker was spawned, which is what makes `git merge --ff-only`
	// refuse it. It is read at status time, one git call per branch, so the
	// per-pane tick loop pays nothing for it.
	Behind bool `json:"behind"`
	// Tab is the tab the worker was placed in. A tab carries a label, which
	// a pane does not, so this is what an operator follows to find the work
	// on their own screen.
	Tab        string    `json:"tab,omitempty"`
	PaneAlive  bool      `json:"pane_alive"`
	PromptedAt time.Time `json:"prompted_at"`
	Prompts    int       `json:"prompts"`
	Notified   bool      `json:"notified"`
	// AwaitingReview is a worker whose task is sitting in review: it has
	// submitted, it is spending nothing, and it is still holding its slot
	// on purpose, because a rejection puts the row back to doing and this
	// pane is where the conversation is.
	AwaitingReview bool `json:"awaiting_review"`
}

// Status is everything the dispatcher is driving, as it believes it now.
type Status struct {
	BasePane   string   `json:"base_pane"`
	MaxWorkers int      `json:"max_workers"`
	Workers    []Worker `json:"workers"`
	// Pending is the task ids reserved by a dispatch and not yet spawned.
	Pending []string `json:"pending"`
}

// Dispatch reserves one ready task for the next tick and returns at once.
//
// It does not spawn. The measured budget of a spawn runs past three minutes —
// the pane's shell, the agent's startup, a trust dialog that may never come,
// and the wait for the goal to show on screen — and no caller on a door can
// be held that long. The reservation is what stops anything else taking the
// task in the meantime; the tick does the work and records the binding.
// project is the §4.1 canonical board to look on, already resolved by the
// door; empty means every board, which is this daemon's default.
func (l *Loop) Dispatch(ctx context.Context, ref, project string) (Reservation, error) {
	// A daemon that inherited no pane asks Herdr for one here rather than
	// refusing outright: the plugin-started daemon has none to inherit,
	// and the pane it will use may not have existed when it started.
	if l.EnsureBase(ctx) == "" {
		return Reservation{}, codes.Refusef(codes.NoBasePane,
			`this daemon has no pane to split a worker off: start it inside a Herdr pane, pass --pane, or set "pane" in the config`)
	}

	ready, err := l.Board.Ready(ctx)
	if err != nil {
		return Reservation{}, err
	}
	// A scoped call is answered from that board's rows alone. Reaching past
	// the scope the caller wrote is how an operator standing in one
	// repository dispatches another one's task, and a number means different
	// tasks on different boards.
	ready = onBoard(ready, project)
	row, ok := match(ready, ref)
	if !ok {
		return Reservation{}, l.whyNotReady(ctx, ref, project)
	}

	// Asked before the reservation is taken, and deliberately before the
	// lock: it shells out to the proxy, and nothing slow runs under mu.
	if why := l.quotaRefusal(ctx, row); why != "" {
		return Reservation{}, codes.Refusef(codes.AtQuota,
			"task %d launches through the proxy and %s", row.Seq, why)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bindings {
		if b.TaskID == row.ID {
			return Reservation{}, codes.Refusef(codes.AlreadyDispatched,
				"task %d already has a worker in pane %s", row.Seq, b.Pane)
		}
	}
	for _, held := range l.pending {
		if held.TaskID == row.ID {
			return Reservation{}, codes.Refusef(codes.AlreadyDispatched,
				"task %d is already reserved for the next tick", row.Seq)
		}
	}
	if live := len(l.bindings) + len(l.pending); live >= l.Policy.MaxWorkers {
		return Reservation{}, codes.Refusef(codes.AtCapacity,
			"%d workers are live or reserved and max-workers is %d", live, l.Policy.MaxWorkers)
	}
	l.pending = append(l.pending, store.Reservation{
		TaskID: row.ID,
		// The owner is what a restart reads to tell this daemon's own stale
		// reservation from a live peer's.
		Owner: l.principal(),
		At:    l.now(),
	})
	ev := l.emitLocked(store.EntityTask, KindReserved, row.ID, row.Project, map[string]any{
		"seq": row.Seq, "title": row.Title,
	})
	// The lock is held for the rest of this call by the deferred unlock, so
	// the hook runs after it, when the caller has its answer.
	defer l.fire(ev)

	return Reservation{TaskID: row.ID, Seq: row.Seq, Title: row.Title, Project: row.Project}, nil
}

// whyNotReady tells a task the board is holding back from a task the board
// does not have. It costs one more call, on the failing path only, and it is
// the difference between a caller fixing a typo and a caller waiting for a
// task that will never come.
func (l *Loop) whyNotReady(ctx context.Context, ref, project string) error {
	if seq, isNumber := strconv.Atoi(ref); isNumber == nil {
		if project != "" {
			// With a board named, a number DOES resolve, so the refusal can
			// say which board it looked on rather than sending the operator
			// to find an id.
			return codes.Refusef(codes.NotReady,
				"task %d is not among the tasks %s is offering", seq, project)
		}
		// The same wall the restart rule hit, on the other call site: a
		// number is unique only inside a project, and a door call names no
		// project the way a pane's checkout does. So the offer list is as
		// far as a number resolves, and it did not carry this one. Asking
		// the board for a bare number across projects is what it refuses by
		// design, and the answer would be a USAGE error rather than a word
		// about the task.
		return codes.Refusef(codes.NotReady,
			"task %d is not among the tasks the board is offering; name it by id to be told what it is instead", seq)
	}
	row, err := l.Board.Get(ctx, ref)
	if err != nil {
		// NOT_FOUND is a board that answered and had no such task. A door
		// that could not answer at all is a different failure, and saying
		// NOT_FOUND for it tells the caller a task does not exist while it
		// may well be sitting on the board.
		var refusal *htask.Refusal
		if errors.As(err, &refusal) && refusal.Code == string(codes.NotFound) {
			return codes.Errorf(codes.NotFound, "no board has task %s: %s", ref, refusal.Message)
		}
		return codes.Errorf(codes.Unavailable, "cannot read task %s from the board: %v", ref, err)
	}
	if claimed := row.ClaimedBy; claimed != "" {
		return codes.Refusef(codes.NotReady, "task %d is %s, held by %s", row.Seq, row.Status, claimed)
	}
	return codes.Refusef(codes.NotReady, "task %d is %s and the board is not offering it", row.Seq, row.Status)
}

// Status reports the bindings and what herdr says about their panes. The
// board facts on each row are as of the last tick; the pane facts are read
// now, because a worker's status is the thing that changes between ticks.
func (l *Loop) Status(ctx context.Context) (Status, error) {
	panes, err := l.Herdr.Panes(ctx)
	if err != nil {
		return Status{}, err
	}

	l.mu.Lock()
	st := Status{
		BasePane:   l.Base(),
		MaxWorkers: l.Policy.MaxWorkers,
		Workers:    []Worker{},
		Pending:    pendingIDs(l.pending),
	}
	// Held alongside the rows so the git reads below can happen after the
	// lock is dropped, in the same order as the workers they answer for.
	bindings := append([]decide.Binding(nil), l.bindings...)
	projects := make([]string, 0, len(bindings))
	for _, b := range bindings {
		row := l.rows[b.TaskID]
		status, alive := panes[b.Pane]
		st.Workers = append(st.Workers, Worker{
			TaskID:      b.TaskID,
			Seq:         row.Seq,
			Title:       row.Title,
			Project:     row.Project,
			Pane:        b.Pane,
			Kind:        kindOf(b),
			Profile:     b.Profile,
			AgentStatus: status,
			Branch:      b.Branch,
			Tab:         b.Tab,
			PaneAlive:   alive,
			PromptedAt:  b.PromptedAt,
			Prompts:     b.Prompts,
			Notified:    b.Notified,

			AwaitingReview: awaitingReview(b, row.Status),
		})
		projects = append(projects, row.Project)
	}
	l.mu.Unlock()

	// Deliberately outside the lock: each of these shells out to git, and a
	// tick waiting on the mutex behind a process spawn per binding is the
	// bug this ordering avoids.
	for i := range st.Workers {
		st.Workers[i].Behind = l.behind(ctx, projects[i], bindings[i])
	}
	return st, nil
}

// behind asks git whether a worker's branch can still be fast-forwarded into
// the project. A git that cannot answer leaves the fact unsaid rather than
// asserting either way, because status is a report and a guess here is a
// wrong one.
func (l *Loop) behind(ctx context.Context, project string, b decide.Binding) bool {
	if b.Branch == "" || project == "" || l.Worktrees == nil {
		return false
	}
	behind, err := l.Worktrees.Behind(ctx, project, b.Branch)
	if err != nil {
		l.logf("task %s: cannot tell whether %s is behind: %v", b.TaskID, b.Branch, err)
		return false
	}
	return behind
}

// awaitingReview reports a worker holding its slot while a human decides.
func awaitingReview(_ decide.Binding, status string) bool {
	return status == "review"
}

// AwaitingReview is how many worker slots are spent on panes that have
// submitted and are waiting for a human. It is what doctor reports so an
// operator who sees nothing moving learns why in one command.
func (l *Loop) AwaitingReview() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	held := 0
	for _, b := range l.bindings {
		if awaitingReview(b, l.rows[b.TaskID].Status) {
			held++
		}
	}
	return held
}

// kindOf names a binding's lane for a caller. There is one, and a binding
// written before the kind existed reads as it.
func kindOf(decide.Binding) string { return decide.KindWorker }

// Pending reports the task ids reserved by a dispatch and not yet spawned.
func (l *Loop) Pending() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return pendingIDs(l.pending)
}

// pendingIDs is the task ids of a set of reservations, for a caller that
// reads the ids and not the daemon that took them.
func pendingIDs(held []store.Reservation) []string {
	out := make([]string, 0, len(held))
	for _, r := range held {
		out = append(out, r.TaskID)
	}
	return out
}

// match finds the row a caller's reference names: the board's id, or the
// project number an operator reads.
// onBoard is the rows of one project, or every row when none was named.
func onBoard(rows []htask.Task, project string) []htask.Task {
	if project == "" {
		return rows
	}
	out := []htask.Task{}
	for _, row := range rows {
		if row.Project == project {
			out = append(out, row)
		}
	}
	return out
}

func match(rows []htask.Task, ref string) (htask.Task, bool) {
	for _, row := range rows {
		if row.ID == ref || strconv.Itoa(row.Seq) == ref {
			return row, true
		}
	}
	return htask.Task{}, false
}

// MaxSpawnAttempts bounds how often one reservation may fail to become a
// worker before it is given up on.
//
// The failures it exists for are the ones that repeat: a profile the config
// does not name, a checkout git refuses to make. Nothing about the next tick
// makes either of them likelier to work, so an unbounded retry is a worker
// slot spent forever on a task that will never start — and with the slot goes
// every other task the fleet had room for.
const MaxSpawnAttempts = 3

// attempt takes the slot for a spawn about to run and counts the try.
//
// It is what a tick calls before every spawn, and it is idempotent in the
// only sense that matters: a task reserved by a dispatch keeps that
// reservation and its owner, and only the count moves. It answers false once
// the count is spent, having dropped the reservation — the task goes back to
// the board's own list, where an operator can see it is not moving.
func (l *Loop) attempt(taskID string) bool {
	l.mu.Lock()
	for i := range l.pending {
		if l.pending[i].TaskID != taskID {
			continue
		}
		if l.pending[i].Attempts >= MaxSpawnAttempts {
			l.pending = append(l.pending[:i:i], l.pending[i+1:]...)
			project := l.rows[taskID].Project
			ev := l.emitLocked(store.EntityTask, KindReservationDropped, taskID, project, map[string]any{
				"why": "its spawn failed", "attempts": MaxSpawnAttempts,
			})
			l.mu.Unlock()
			l.fire(ev)
			l.logf("task %s: %d spawn attempts failed, so its reservation is dropped and the task is left on the board",
				taskID, MaxSpawnAttempts)
			return false
		}
		l.pending[i].Attempts++
		l.saveLocked()
		l.mu.Unlock()
		return true
	}
	l.pending = append(l.pending, store.Reservation{
		TaskID: taskID, Owner: l.principal(), At: l.now(), Attempts: 1,
	})
	l.saveLocked()
	l.mu.Unlock()
	return true
}

// unreserve drops a reservation, whether it was spent on a spawn or taken
// back by the board.
func (l *Loop) unreserve(taskID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.pending[:0]
	for _, held := range l.pending {
		if held.TaskID != taskID {
			kept = append(kept, held)
		}
	}
	l.pending = kept
	l.saveLocked()
}
