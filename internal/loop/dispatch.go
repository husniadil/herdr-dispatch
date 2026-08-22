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
	// Kind is which lane the pane was brought up for: a worker does the
	// task, a verifier checks what a worker submitted.
	Kind string `json:"kind"`
	// AgentStatus is herdr's own word for the worker, or empty when herdr
	// no longer lists the pane at all.
	AgentStatus string `json:"agent_status"`
	// PaneAlive is whether herdr still lists the pane. A binding whose pane
	// is gone is dropped by the next tick.
	PaneAlive  bool      `json:"pane_alive"`
	PromptedAt time.Time `json:"prompted_at"`
	Prompts    int       `json:"prompts"`
	Notified   bool      `json:"notified"`
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
func (l *Loop) Dispatch(ctx context.Context, ref string) (Reservation, error) {
	if l.BasePane == "" {
		return Reservation{}, codes.Errorf(codes.NoBasePane,
			`this daemon has no pane to split a worker off: start it inside a Herdr pane, pass -pane, or set "pane" in the config`)
	}

	ready, err := l.Board.Ready(ctx)
	if err != nil {
		return Reservation{}, err
	}
	row, ok := match(ready, ref)
	if !ok {
		return Reservation{}, l.whyNotReady(ctx, ref)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bindings {
		if b.TaskID == row.ID {
			return Reservation{}, codes.Errorf(codes.AlreadyDispatched,
				"task %d already has a worker in pane %s", row.Seq, b.Pane)
		}
	}
	for _, held := range l.pending {
		if held.TaskID == row.ID {
			return Reservation{}, codes.Errorf(codes.AlreadyDispatched,
				"task %d is already reserved for the next tick", row.Seq)
		}
	}
	if live := len(l.bindings) + len(l.pending); live >= l.Policy.MaxWorkers {
		return Reservation{}, codes.Errorf(codes.AtCapacity,
			"%d workers are live or reserved and max-workers is %d", live, l.Policy.MaxWorkers)
	}
	l.pending = append(l.pending, store.Reservation{
		TaskID: row.ID,
		// The owner is what a restart reads to tell this daemon's own stale
		// reservation from a live peer's.
		Owner: l.principal(),
		At:    l.now(),
	})
	l.saveLocked()

	return Reservation{TaskID: row.ID, Seq: row.Seq, Title: row.Title, Project: row.Project}, nil
}

// whyNotReady tells a task the board is holding back from a task the board
// does not have. It costs one more call, on the failing path only, and it is
// the difference between a caller fixing a typo and a caller waiting for a
// task that will never come.
func (l *Loop) whyNotReady(ctx context.Context, ref string) error {
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
		return codes.Errorf(codes.NotReady, "task %d is %s, held by %s", row.Seq, row.Status, claimed)
	}
	return codes.Errorf(codes.NotReady, "task %d is %s and the board is not offering it", row.Seq, row.Status)
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
	defer l.mu.Unlock()
	st := Status{
		BasePane:   l.BasePane,
		MaxWorkers: l.Policy.MaxWorkers,
		Workers:    []Worker{},
		Pending:    pendingIDs(l.pending),
	}
	for _, b := range l.bindings {
		row := l.rows[b.TaskID]
		status, alive := panes[b.Pane]
		st.Workers = append(st.Workers, Worker{
			TaskID:      b.TaskID,
			Seq:         row.Seq,
			Title:       row.Title,
			Project:     row.Project,
			Pane:        b.Pane,
			Kind:        kindOf(b),
			AgentStatus: status,
			PaneAlive:   alive,
			PromptedAt:  b.PromptedAt,
			Prompts:     b.Prompts,
			Notified:    b.Notified,
		})
	}
	return st, nil
}

// kindOf names a binding's lane for a caller. A binding written before the
// lane existed carries no kind and is a worker.
func kindOf(b decide.Binding) string {
	if b.IsVerifier() {
		return decide.KindVerifier
	}
	return decide.KindWorker
}

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
func match(rows []htask.Task, ref string) (htask.Task, bool) {
	for _, row := range rows {
		if row.ID == ref || strconv.Itoa(row.Seq) == ref {
			return row, true
		}
	}
	return htask.Task{}, false
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
