// Package loop is the dispatcher's tick: collect a snapshot of the facts,
// hand it to the pure decision core, and execute what comes back.
//
// The bindings are the only state the dispatcher owns, and they live in
// memory. Everything else it might want to remember is a board fact or a
// Herdr fact, and both are read fresh every tick.
package loop

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
)

// Loop holds the adapters, the policy, and the bindings.
type Loop struct {
	Board  *htask.Client
	Herdr  *herdr.Client
	Spawn  *spawn.Pipeline
	Config config.Config
	Policy decide.Policy

	// BasePane is the pane worker panes are split off, normally the
	// dispatcher's own.
	BasePane string
	// Now is time.Now unless a test replaces it.
	Now func() time.Time
	// Log is where the operator hears about anything that went wrong.
	Log *log.Logger

	bindings []decide.Binding
	// rows is this tick's board rows, by task id: the project a worker runs
	// in and the number an operator reads. Rebuilt every tick, never kept.
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

// Bindings reports what the dispatcher currently believes it is driving.
func (l *Loop) Bindings() []decide.Binding {
	return append([]decide.Binding(nil), l.bindings...)
}

func (l *Loop) snapshot(ctx context.Context) (decide.Snapshot, error) {
	snap := decide.Snapshot{
		Tasks: make(map[string]decide.Task),
		Now:   l.now(),
	}
	l.rows = make(map[string]htask.Task)

	bound := make(map[string]bool, len(l.bindings))
	for _, b := range l.bindings {
		bound[b.TaskID] = true
		row, err := l.Board.Get(ctx, b.TaskID)
		if err != nil {
			// The core holds a binding it knows nothing about, which is the
			// safe reading of a row that cannot be read.
			l.logf("task %s: cannot be read, holding its binding: %v", b.TaskID, err)
			continue
		}
		l.rows[row.ID] = row
		snap.Tasks[row.ID] = decide.Task{ID: row.ID, Status: row.Status, ClaimedBy: row.Pane()}
	}

	ready, err := l.Board.Ready(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	for _, row := range ready {
		// A task stays ready until its worker claims, so a task already
		// bound is a task already dispatched, not a task to dispatch again.
		if bound[row.ID] {
			continue
		}
		l.rows[row.ID] = row
		snap.Ready = append(snap.Ready, row.ID)
	}

	agents, err := l.Herdr.Agents(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	snap.Agents = agents
	snap.Bindings = l.bindings
	return snap, nil
}

func (l *Loop) apply(ctx context.Context, actions []decide.Action) {
	for _, a := range actions {
		var err error
		switch a.Kind {
		case decide.Spawn:
			err = l.spawn(ctx, a)
		case decide.Prompt:
			err = l.prompt(ctx, a)
		case decide.Notify:
			err = l.notify(ctx, a)
		case decide.Retire:
			err = l.retire(ctx, a)
		case decide.Unbind:
			// The pane is gone. Dropping the binding is all there is to do:
			// releasing the lease is the board's own sweep.
			l.logf("task %s: pane %s is gone, dropping its binding", a.TaskID, a.Pane)
			l.drop(a.TaskID)
		case decide.GiveUp:
			l.logf("task %s: %s after %d prompts, retiring pane %s",
				a.TaskID, a.Reason, l.promptsFor(a.TaskID), a.Pane)
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
	row, ok := l.rows[a.TaskID]
	if !ok {
		return fmt.Errorf("no board row to spawn from")
	}
	profile, err := l.Config.ProfileFor(row.Project)
	if err != nil {
		return err
	}
	goal, err := l.Board.Goal(ctx, row.ID)
	if err != nil {
		return err
	}
	pane, err := l.Spawn.Run(ctx, spawn.Request{
		Name:     workerName(row.Seq),
		BasePane: l.BasePane,
		Cwd:      row.Project,
		Profile:  profile,
		Goal:     goal,
	})
	if err != nil {
		return err
	}
	l.bindings = append(l.bindings, decide.Binding{
		TaskID:     row.ID,
		Pane:       pane,
		PromptedAt: l.now(),
		Prompts:    1,
	})
	return nil
}

// prompt is the backstop, and it carries a nudge rather than the goal: the
// goal is already armed in the worker, and a slash command that long cannot
// arrive through a prompt anyway.
func (l *Loop) prompt(ctx context.Context, a decide.Action) error {
	if err := l.Herdr.AgentPrompt(ctx, a.Pane, l.nudge(a)); err != nil {
		return err
	}
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Prompts++
			l.bindings[i].PromptedAt = l.now()
		}
	}
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
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Notified = true
		}
	}
	return nil
}

// retire closes the worker's pane and drops the binding. It never releases
// the task's lease: pane-gone sweeps and the lease timer are the board's own,
// and a second writer racing them is the bug, not a safety net.
func (l *Loop) retire(ctx context.Context, a decide.Action) error {
	err := l.Herdr.PaneClose(ctx, a.Pane)
	l.drop(a.TaskID)
	return err
}

func (l *Loop) drop(taskID string) {
	kept := l.bindings[:0]
	for _, b := range l.bindings {
		if b.TaskID != taskID {
			kept = append(kept, b)
		}
	}
	l.bindings = kept
}

func (l *Loop) promptsFor(taskID string) int {
	for _, b := range l.bindings {
		if b.TaskID == taskID {
			return b.Prompts
		}
	}
	return 0
}

func (l *Loop) seqFor(taskID string) int { return l.rows[taskID].Seq }

// taskName is what an operator would call the task: its number and title
// when the row is at hand, its id when it is not.
func (l *Loop) taskName(taskID string) string {
	row, ok := l.rows[taskID]
	if !ok {
		return "task " + taskID
	}
	return fmt.Sprintf("task #%d %q", row.Seq, row.Title)
}

func (l *Loop) taskNumber(taskID string) string {
	if row, ok := l.rows[taskID]; ok {
		return fmt.Sprintf("%d", row.Seq)
	}
	return taskID
}

// workerName is the agent name herdr registers. Herdr requires it to match
// [a-z][a-z0-9_-]{0,31} and to be unique among live agents; one worker per
// task number is both.
func workerName(seq int) string { return fmt.Sprintf("hdis-%d", seq) }

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
