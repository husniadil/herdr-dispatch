// Package loop is the dispatcher's tick: collect a snapshot of the facts,
// hand it to the pure decision core, and execute what comes back.
//
// The bindings are the only state the dispatcher owns, and they are written
// to the plugin's own store on every change so a restart does not forget a
// worker it prompted. Everything else it might want to remember is a board
// fact or a Herdr fact, and both are read fresh every tick.
package loop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// Loop holds the adapters, the policy, and the bindings.
type Loop struct {
	Board  *htask.Client
	Herdr  *herdr.Client
	Spawn  *spawn.Pipeline
	Config config.Config
	Policy decide.Policy
	// Worktrees hands each verifier the checkout it works in. The
	// verification lane needs it: a verifier with nowhere of its own to work
	// is not spawned at all.
	Worktrees *worktree.Manager
	// Store is where the bindings outlive the process. A nil Store keeps
	// them in memory only, which is a test that does not care and never a
	// running daemon.
	Store *store.Bindings

	// BasePane is the pane worker panes are split off, normally the
	// dispatcher's own.
	BasePane string
	// Now is time.Now unless a test replaces it.
	Now func() time.Time
	// Log is where the operator hears about anything that went wrong.
	Log *log.Logger

	// mu guards everything below it. The tick runs on the daemon's own
	// goroutine while dispatch and status answer on a door's, so the
	// bindings are read and written from more than one at a time. Nothing
	// slow is done under this lock: a spawn runs to minutes, and it runs
	// with the lock released.
	mu       sync.Mutex
	bindings []decide.Binding
	// pending is the task ids an on-demand dispatch reserved and no tick has
	// spawned yet. A reservation is what keeps the watching loop and the
	// dispatch verb from both taking the same task.
	pending []string
	// readopted is how many persisted bindings the last Adopt kept, for
	// doctor to report.
	readopted int
	// rows is the last tick's board rows, by task id: the project a worker
	// runs in, the number an operator reads, the title status prints. It is
	// a cache of board facts and never a source of them.
	rows map[string]htask.Task
}

// Tick runs one round. A board that cannot be read fails the tick loudly and
// spawns nothing; a single action that fails is logged and the rest go on.
func (l *Loop) Tick(ctx context.Context) error {
	snap, err := l.snapshot(ctx)
	if err != nil {
		return err
	}
	l.apply(ctx, decide.Decide(snap, l.Policy))
	return nil
}

// Adopt reads the persisted bindings and takes back the ones reality still
// agrees with. It is run once, before the first tick.
//
// Verification is against the two systems that own the facts: the pane must
// still be one Herdr lists, and the task must still be one this pane is
// driving. A binding that fails either is dropped with a line in the log and
// nothing is done to its pane — retiring a worker on the strength of a
// binding a restart could not verify is the split this is here to prevent.
//
// Herdr being unreachable is different from a pane being gone, and adopting
// on that guess would hand a live worker's task to a second pane. Nothing is
// adopted, the failure is loud, and the store is left for the next start.
func (l *Loop) Adopt(ctx context.Context) (int, error) {
	if l.Store == nil {
		return 0, nil
	}
	held, err := l.Store.Load()
	if err != nil {
		// A store that cannot be read is a dispatcher that has forgotten,
		// which is where it was before any of this. It is not a reason to
		// refuse to start.
		l.logf("the bindings could not be read, starting with none: %v", err)
		return 0, nil
	}
	if len(held) == 0 {
		return 0, nil
	}

	panes, err := l.Herdr.Panes(ctx)
	if err != nil {
		return 0, fmt.Errorf("herdr cannot say which panes are alive, so %d persisted binding(s) stay unadopted: %w", len(held), err)
	}

	kept := make([]decide.Binding, 0, len(held))
	rows := make(map[string]htask.Task, len(held))
	for _, b := range held {
		if _, alive := panes[b.Pane]; !alive {
			l.logf("task %s: pane %s is gone, dropping the binding a restart found", b.TaskID, b.Pane)
			continue
		}
		row, err := l.Board.Get(ctx, b.TaskID)
		if err != nil {
			var refusal *htask.Refusal
			if errors.As(err, &refusal) && refusal.Code == string(codes.NotFound) {
				l.logf("task %s: the board has no such task, dropping the binding a restart found", b.TaskID)
				continue
			}
			// The board could not answer, which is not an answer that the
			// task moved on. Hold it, exactly as a tick holds it.
			l.logf("task %s: cannot be read, holding the binding a restart found: %v", b.TaskID, err)
			kept = append(kept, b)
			continue
		}
		if decide.Terminal(row.Status) {
			l.logf("task %s is %s, dropping the binding a restart found on pane %s", b.TaskID, row.Status, b.Pane)
			continue
		}
		if b.IsVerifier() {
			// A verifier holds no claim, so the claim is not what says its
			// binding is still real. What it was brought up to read is: the
			// submission it was checking has to still be in review.
			if row.Status != "review" {
				l.logf("task %s is %s, dropping the verifier binding a restart found on pane %s", b.TaskID, row.Status, b.Pane)
				continue
			}
		} else if claimed := row.Pane(); claimed != "" && claimed != b.Pane {
			l.logf("task %s is held by %s, not by pane %s; dropping the binding a restart found", b.TaskID, claimed, b.Pane)
			continue
		}
		rows[row.ID] = row
		kept = append(kept, b)
	}

	l.mu.Lock()
	l.bindings = kept
	l.rows = rows
	l.readopted = len(kept)
	l.saveLocked()
	l.mu.Unlock()
	l.logf("re-adopted %d of %d persisted binding(s) from %s", len(kept), len(held), l.Store.Path)
	return len(kept), nil
}

// Readopted is how many persisted bindings the last Adopt kept.
func (l *Loop) Readopted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readopted
}

// BindingsPath is where the bindings are kept, for doctor to name.
func (l *Loop) BindingsPath() string {
	if l.Store == nil {
		return ""
	}
	return l.Store.Path
}

// saveLocked writes the bindings out. The caller holds mu, so the document
// on disk can never be a set no process ever held. A store that cannot be
// written is reported and the daemon carries on: the bindings in memory are
// still right, and refusing to dispatch because a file is unwritable helps
// nobody.
func (l *Loop) saveLocked() {
	if l.Store == nil {
		return
	}
	if err := l.Store.Save(l.bindings); err != nil {
		l.logf("the bindings could not be written: %v", err)
	}
}

// Bindings reports what the dispatcher currently believes it is driving.
func (l *Loop) Bindings() []decide.Binding {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]decide.Binding(nil), l.bindings...)
}

func (l *Loop) snapshot(ctx context.Context) (decide.Snapshot, error) {
	l.mu.Lock()
	bindings := append([]decide.Binding(nil), l.bindings...)
	pending := append([]string(nil), l.pending...)
	l.mu.Unlock()

	snap := decide.Snapshot{
		Tasks: make(map[string]decide.Task),
		Now:   l.now(),
	}
	rows := make(map[string]htask.Task)

	bound := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		bound[b.TaskID] = true
		row, err := l.Board.Get(ctx, b.TaskID)
		if err != nil {
			// The core holds a binding it knows nothing about, which is the
			// safe reading of a row that cannot be read.
			l.logf("task %s: cannot be read, holding its binding: %v", b.TaskID, err)
			continue
		}
		rows[row.ID] = row
		snap.Tasks[row.ID] = decide.Task{ID: row.ID, Status: row.Status, ClaimedBy: row.Pane()}
	}

	ready, err := l.Board.Ready(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	offered := make(map[string]htask.Task, len(ready))
	for _, row := range ready {
		offered[row.ID] = row
	}

	// Reservations go first: an on-demand dispatch is a caller asking for
	// this task now, ahead of whatever order the board lists.
	reserved := make(map[string]bool, len(pending))
	for _, id := range pending {
		row, ok := offered[id]
		if bound[id] || !ok {
			// A worker is already on it, or the board has taken it back.
			// Either way the reservation has nothing left to buy.
			if !bound[id] {
				l.logf("task %s: was reserved for dispatch, but the board no longer offers it; dropping the reservation", id)
			}
			l.unreserve(id)
			continue
		}
		reserved[row.ID] = true
		rows[row.ID] = row
		snap.Ready = append(snap.Ready, row.ID)
	}

	for _, row := range ready {
		// A task stays ready until its worker claims, so a task already
		// bound is a task already dispatched, not a task to dispatch again.
		if bound[row.ID] || reserved[row.ID] {
			continue
		}
		rows[row.ID] = row
		snap.Ready = append(snap.Ready, row.ID)
	}

	panes, err := l.Herdr.Panes(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	snap.Agents = panes
	snap.Bindings = bindings

	l.mu.Lock()
	l.rows = rows
	l.mu.Unlock()
	return snap, nil
}

func (l *Loop) apply(ctx context.Context, actions []decide.Action) {
	for _, a := range actions {
		var err error
		switch a.Kind {
		case decide.Spawn:
			err = l.spawn(ctx, a)
		case decide.SpawnVerifier:
			err = l.spawnVerifier(ctx, a)
		case decide.Rearm:
			// The submission the announcement and the verification belonged
			// to has left review. If it comes back, both are due again.
			l.rearm(a.TaskID)
		case decide.Prompt:
			err = l.prompt(ctx, a)
		case decide.Notify:
			err = l.notify(ctx, a)
		case decide.Retire:
			err = l.retire(ctx, a)
		case decide.Unbind:
			// The pane is gone. Dropping the binding and the settings file
			// its spawn wrote is all there is to do: releasing the lease is
			// the board's own sweep.
			l.logf("task %s: pane %s is gone, dropping its binding", a.TaskID, a.Pane)
			l.Spawn.Discard(a.Pane)
			l.drop(ctx, a.Pane)
		case decide.GiveUp:
			l.logf("task %s: %s after %d prompts, retiring pane %s",
				a.TaskID, a.Reason, l.promptsFor(a.Pane), a.Pane)
			err = l.retire(ctx, a)
		default:
			err = fmt.Errorf("unknown action %q", a.Kind)
		}
		if err != nil {
			l.logf("%s task %s: %v", a.Kind, a.TaskID, err)
		}
	}
}

func (l *Loop) spawn(ctx context.Context, a decide.Action) error {
	row, ok := l.row(a.TaskID)
	if !ok {
		return fmt.Errorf("no board row to spawn from")
	}
	profile, err := l.Config.ProfileFor(row.Project)
	if err != nil {
		return err
	}
	// The condition is composed here, not rendered from the board: it travels
	// on a line herdr TYPES into the pane, and the board's own goal document
	// is far too long to type intact. The worker reads the criteria itself.
	pane, err := l.Spawn.Run(ctx, spawn.Request{
		Name:     workerName(row.Seq),
		BasePane: l.BasePane,
		Cwd:      row.Project,
		Profile:  profile,
		Goal:     spawn.PointerGoal(row.Seq),
	})
	if pane == "" {
		return err
	}
	// A pane came back, so a worker is alive in it — including when the
	// spawn failed loud because it could not be read. The binding is the
	// only record of which pane was prompted for which task until the worker
	// claims; drop it and review is never announced for a task that may
	// already be under way. The error still reaches the operator's log.
	l.mu.Lock()
	l.bindings = append(l.bindings, decide.Binding{
		TaskID:     row.ID,
		Pane:       pane,
		Kind:       decide.KindWorker,
		PromptedAt: l.now(),
		Prompts:    1,
	})
	l.saveLocked()
	l.mu.Unlock()
	// The reservation is spent the moment the binding exists.
	l.unreserve(row.ID)
	return err
}

// spawnVerifier brings up a VERIFIER for a task one of this dispatcher's own
// workers submitted: a fresh pane, the same spawn path, its own binding with
// the verifier's kind on it.
//
// The verifier's own condition is what keeps this inside the boundary. It
// rereads, reruns and reports; it never approves and never rejects, and this
// binary still runs no review verb of its own. Delegating the reading is not
// delegating the judgment.
func (l *Loop) spawnVerifier(ctx context.Context, a decide.Action) error {
	row, ok := l.row(a.TaskID)
	if !ok {
		return fmt.Errorf("no board row to verify")
	}
	profile, err := l.Config.VerifyProfile()
	if err != nil {
		return err
	}
	// The checkout comes first, and no checkout means no verifier. A
	// verifier reads and mutates the tree it is put in, so the project's own
	// directory — which its worker still holds and the operator reviews in —
	// is the one place it must never run: the lane's first live run restored
	// that tree from HEAD, destroyed the operator's uncommitted work, and
	// then reported a gate result measured on it. Not verifying is the
	// better failure, and the next tick may try again.
	if l.Worktrees == nil {
		return fmt.Errorf("no worktree manager, so nothing verifies task %s in a tree of its own", row.Project)
	}
	tree, err := l.Worktrees.Create(ctx, row.Project, row.Seq)
	if err != nil {
		return err
	}
	pane, err := l.Spawn.Run(ctx, spawn.Request{
		Name:     verifierName(row.Seq),
		BasePane: l.BasePane,
		Cwd:      tree,
		Profile:  profile,
		Goal:     spawn.VerifierGoal(row.Seq),
	})
	if pane == "" {
		// Nothing came up, so the submission has not had its verifier and
		// the next tick may try again. The checkout it would have worked in
		// goes with it.
		if rmErr := l.Worktrees.Remove(ctx, tree); rmErr != nil {
			l.logf("task %s: %v", row.ID, rmErr)
		}
		return err
	}
	l.mu.Lock()
	l.bindings = append(l.bindings, decide.Binding{
		TaskID:     row.ID,
		Pane:       pane,
		Kind:       decide.KindVerifier,
		Worktree:   tree,
		PromptedAt: l.now(),
		Prompts:    1,
	})
	// The worker's binding is where "this submission has had its verifier"
	// is remembered, and Rearm is what clears it.
	for i := range l.bindings {
		if l.bindings[i].TaskID == row.ID && !l.bindings[i].IsVerifier() {
			l.bindings[i].Verified = true
		}
	}
	l.saveLocked()
	l.mu.Unlock()
	return err
}

// rearm forgets the announcement and the verification on a task's worker
// binding, because the submission they belonged to is no longer in review.
func (l *Loop) rearm(taskID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == taskID && !l.bindings[i].IsVerifier() {
			l.bindings[i].Notified = false
			l.bindings[i].Verified = false
		}
	}
	l.saveLocked()
}

// prompt is the backstop, and it carries a nudge rather than the goal: the
// goal is already armed in the worker, and a slash command that long cannot
// arrive through a prompt anyway.
func (l *Loop) prompt(ctx context.Context, a decide.Action) error {
	if err := l.Herdr.AgentPrompt(ctx, a.Pane, l.nudge(a)); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Prompts++
			l.bindings[i].PromptedAt = l.now()
		}
	}
	l.saveLocked()
	return nil
}

func (l *Loop) nudge(a decide.Action) string {
	name := l.taskName(a.TaskID)
	if a.Reason == decide.ReasonStalled {
		return fmt.Sprintf("You went idle without submitting %s. Carry on, or release it with a note saying what is left.", name)
	}
	return fmt.Sprintf("Your goal is still unclaimed on the board. Run `htask task claim %s` and start on it.", l.taskNumber(a.TaskID))
}

// notify hands the task to the operator and stops there: verification and
// approval belong to the board's review gate, not to this binary.
func (l *Loop) notify(ctx context.Context, a decide.Action) error {
	name := l.taskName(a.TaskID)
	err := l.Herdr.Notify(ctx,
		fmt.Sprintf("%s is in review", name),
		fmt.Sprintf("Worker %s submitted it. Review it on the board.", workerName(l.seqFor(a.TaskID))))
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Notified = true
		}
	}
	l.saveLocked()
	return nil
}

// retire closes the worker's pane through the spawn pipeline, which takes the
// settings file the spawn wrote with it, and drops the binding. It never
// releases the task's lease: pane-gone sweeps and the lease timer are the
// board's own, and a second writer racing them is the bug, not a safety net.
func (l *Loop) retire(ctx context.Context, a decide.Action) error {
	err := l.Spawn.Retire(ctx, a.Pane)
	l.drop(ctx, a.Pane)
	return err
}

// drop forgets one binding, by the pane it names, and takes the worktree the
// binding owned with it. The pane is what a binding is unique by: a task in
// review can hold a worker's binding and its verifier's at once, and dropping
// by task would take both.
//
// The binding is the only record of where a verifier's checkout is, so
// removing it here is the one place that record is spent. It is written to
// the store, so a restart inherits the removal rather than leaking it.
func (l *Loop) drop(ctx context.Context, pane string) {
	l.mu.Lock()
	var tree string
	kept := l.bindings[:0]
	for _, b := range l.bindings {
		if b.Pane != pane {
			kept = append(kept, b)
			continue
		}
		tree = b.Worktree
	}
	l.bindings = kept
	l.saveLocked()
	l.mu.Unlock()

	if tree != "" && l.Worktrees != nil {
		if err := l.Worktrees.Remove(ctx, tree); err != nil {
			l.logf("pane %s: %v", pane, err)
		}
	}
}

func (l *Loop) promptsFor(pane string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bindings {
		if b.Pane == pane {
			return b.Prompts
		}
	}
	return 0
}

// row reads the last tick's board row for a task.
func (l *Loop) row(taskID string) (htask.Task, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.rows[taskID]
	return row, ok
}

func (l *Loop) seqFor(taskID string) int {
	row, _ := l.row(taskID)
	return row.Seq
}

// taskName is what an operator would call the task: its number and title
// when the row is at hand, its id when it is not.
func (l *Loop) taskName(taskID string) string {
	row, ok := l.row(taskID)
	if !ok {
		return "task " + taskID
	}
	return fmt.Sprintf("task #%d %q", row.Seq, row.Title)
}

func (l *Loop) taskNumber(taskID string) string {
	if row, ok := l.row(taskID); ok {
		return fmt.Sprintf("%d", row.Seq)
	}
	return taskID
}

// workerName is the agent name herdr registers. Herdr requires it to match
// [a-z][a-z0-9_-]{0,31} and to be unique among live agents; one worker per
// task number is both.
func workerName(seq int) string { return fmt.Sprintf("hdis-%d", seq) }

// verifierName is the agent name a verifier registers under, apart from the
// worker's: herdr requires the name to be unique among live agents, and both
// panes are live at once.
func verifierName(seq int) string { return fmt.Sprintf("hdis-v-%d", seq) }

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) logf(format string, args ...any) {
	if l.Log != nil {
		l.Log.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
