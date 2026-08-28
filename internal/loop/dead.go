package loop

import (
	"context"
	"errors"

	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// AgentNotFound is Herdr's refusal when it holds no agent for a target it
// still holds a pane for. It is the one word that separates the failure this
// file exists for from every other prompt that did not go through: the pane
// is there, so nothing unbinds it, and the agent behind it is not.
const AgentNotFound = "agent_not_found"

// DeadWorkerStreak is how many prompts in a row must come back
// AgentNotFound before the worker is declared dead.
//
// It is a streak rather than a single refusal because one refusal is not
// evidence. A pane mid-restart, an agent Herdr has not registered yet, a
// prompt that raced a client reconnecting — each answers once and recovers,
// and a worker dropped on the first of them is a live worker's task handed
// away while it is still working. Three consecutive refusals, each a tick
// apart, are the shape that was measured: it never recovered, and every tick
// after it re-prompted a pane that could not take a prompt.
const DeadWorkerStreak = 3

// MaxWorkerDeaths is how many workers may die on one task before the task is
// held back.
//
// The second death is what makes it the task rather than the worker. One
// agent dying is an agent dying; two agents dying on the same task, each in
// its own fresh pane and its own fresh checkout, is a fact about the work,
// and nothing about the next tick makes the third likelier to stay up. The
// task keeps its place on the board and stops being handed panes, which puts
// the decision where it belongs — with the operator, who reads it in doctor.
const MaxWorkerDeaths = 2

// boardActs are the board-trail kinds that clear a task's death count: an
// act by somebody other than this daemon that says a human has looked at the
// row. A release with a note leaves the next worker something to read; an
// amendment changes what the work is. Both are somebody deciding, and the
// count is a record of workers dying rather than a verdict to be defended.
//
// `swept` is deliberately not one of them. That is htask's own pane-gone
// sweep returning the task when this daemon retires the dead worker's pane
// — this daemon's own teardown coming back at it under another name — and
// counting it as somebody deciding would clear every death on the tick that
// counted it.
var boardActs = map[string]bool{"released": true, "amended": true}

// agentMissing counts one prompt Herdr refused because it has no agent in the
// pane, and declares the worker dead on the DeadWorkerStreak'th in a row.
//
// Only that refusal is counted. Every other failure of a prompt — a Herdr
// that could not be reached, a budget clamp, a pane that is gone — is a
// different fact with its own handling, and a pane Herdr no longer lists is
// unbound by the core before a prompt is ever asked for.
func (l *Loop) agentMissing(ctx context.Context, a decide.Action, err error) {
	var refusal *herdrclient.Error
	if !errors.As(err, &refusal) || refusal.Code != AgentNotFound {
		return
	}
	l.mu.Lock()
	if l.missing == nil {
		l.missing = make(map[string]int)
	}
	l.missing[a.Pane]++
	streak := l.missing[a.Pane]
	l.mu.Unlock()

	if streak < DeadWorkerStreak {
		l.logf("task %s: pane %s is alive and herdr has no agent in it (%d of %d in a row)",
			a.TaskID, a.Pane, streak, DeadWorkerStreak)
		return
	}
	l.workerDied(ctx, a, streak)
}

// promptLanded ends a pane's streak, because a prompt that went through is
// the agent answering. The streak is CONSECUTIVE refusals, so anything
// between them starts the count again.
func (l *Loop) promptLanded(pane string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.missing, pane)
}

// workerDied ends a worker whose agent is gone and whose pane is not.
//
// The teardown is the one a cancelled task already uses — retire closes the
// pane through the spawn pipeline and drops the binding with its checkout —
// because there is one teardown and this is a second way of reaching it, not
// a second one.
//
// Retiring the pane is ALSO what hands the task back, and nothing here says
// anything to the board. htask releases a row to its holder or to the
// operator, and the holder of a dead worker's task is that worker's own
// `agent:<pane>`: a plugin principal asking for it is refused every time, so
// a release from here would be a call that always fails and a note nobody
// ever reads. What actually returns the task is htask's own pane-gone sweep
// (§11.5): the pane closes, the sweep releases the claim as `swept` with its
// own note, and the lease timer is behind that. The REASON the worker ended
// is on this daemon's trail instead, where the fact belongs — the board's
// sweep can only say a pane exited, and only this daemon knows the agent
// inside it stopped answering while the pane stayed up.
func (l *Loop) workerDied(ctx context.Context, a decide.Action, streak int) {
	project := l.projectOf(a.TaskID)
	l.logf("task %s: herdr has had no agent in pane %s for %d prompts in a row while the pane stayed alive; the worker's agent died, so the pane is retired and the board's own sweep takes the task back",
		a.TaskID, a.Pane, streak)

	if err := l.retire(ctx, a); err != nil {
		l.logf("task %s: the dead worker's pane %s could not be retired: %v", a.TaskID, a.Pane, err)
	}

	l.mu.Lock()
	delete(l.missing, a.Pane)
	count := l.countDeathLocked(a.TaskID, project)
	ev := l.emitLocked(store.EntityWorker, KindDied, a.TaskID, project, map[string]any{
		"pane": a.Pane, "prompts": streak, "deaths": count,
	})
	l.mu.Unlock()
	l.fire(ev)
}

// countDeathLocked adds one to a task's death count and returns the total.
//
// The cursor it writes is read now, AFTER the retire: the reset reads the
// board's trail from it, and the `swept` the retire causes lands after this
// point and is not a reset kind either way.
func (l *Loop) countDeathLocked(taskID, project string) int {
	for i := range l.deaths {
		if l.deaths[i].TaskID == taskID {
			l.deaths[i].Count++
			l.deaths[i].SinceMS = l.now().UnixMilli()
			if project != "" {
				l.deaths[i].Project = project
			}
			l.saveLocked()
			return l.deaths[i].Count
		}
	}
	l.deaths = append(l.deaths, store.Death{
		TaskID: taskID, Project: project, Count: 1, SinceMS: l.now().UnixMilli(),
	})
	l.saveLocked()
	return 1
}

// DeathsOf is how many workers have died on one task.
func (l *Loop) DeathsOf(taskID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deathsLocked(taskID)
}

func (l *Loop) deathsLocked(taskID string) int {
	for _, d := range l.deaths {
		if d.TaskID == taskID {
			return d.Count
		}
	}
	return 0
}

// heldBack reports whether a task has killed enough workers to stop being
// handed one. It is asked of the ready list every tick and of dispatch by
// name, so both doors answer the same way.
func (l *Loop) heldBack(taskID string) bool {
	return l.DeathsOf(taskID) >= MaxWorkerDeaths
}

// DeadTask is one task doctor holds in front of the operator: nothing will
// dispatch it, and the number of workers that died on it is why.
type DeadTask struct {
	TaskID  string `json:"task"`
	Seq     int    `json:"seq,omitempty"`
	Project string `json:"project,omitempty"`
	Title   string `json:"title,omitempty"`
	Deaths  int    `json:"deaths"`
}

// WorkersDied is every task held back for killing its workers.
//
// It is what makes the hold visible. A task that is quietly skipped every
// tick is the failure this whole file exists to end: an operator watching a
// board that never moves, with every command answering that nothing is
// wrong.
func (l *Loop) WorkersDied() []DeadTask {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []DeadTask{}
	for _, d := range l.deaths {
		if d.Count < MaxWorkerDeaths {
			continue
		}
		row := l.rows[d.TaskID]
		project := d.Project
		if project == "" {
			project = row.Project
		}
		out = append(out, DeadTask{
			TaskID: d.TaskID, Seq: row.Seq, Project: project, Title: row.Title, Deaths: d.Count,
		})
	}
	return out
}

// clearDeaths drops the count on every task somebody other than this daemon
// has since acted on.
//
// It is read off the BOARD's own trail rather than off the row, and that is
// the whole of its correctness: a row carries when it last changed and not
// who changed it or how, so a count cleared from the row alone would be
// cleared by the next worker claiming it — and a task on its second worker
// would reach the cap only if that worker died before touching the board.
// The two kinds it reads are the two acts that mean a human decided
// something, and this daemon's own release is filtered out by its actor,
// because that release is what it does when it counts a death.
//
// Nothing is read when nothing is counted, so a fleet with no dead workers
// pays no board call for this.
func (l *Loop) clearDeaths(ctx context.Context) {
	l.mu.Lock()
	deaths := append([]store.Death(nil), l.deaths...)
	l.mu.Unlock()
	if len(deaths) == 0 {
		return
	}
	since := deaths[0].SinceMS
	for _, d := range deaths {
		if d.SinceMS < since {
			since = d.SinceMS
		}
	}
	events, err := l.Board.Events(ctx, since)
	if err != nil {
		// A trail that cannot be read is not a trail that says nobody
		// acted, so every count is held exactly as it was.
		l.logf("the board's trail cannot be read, so no death count is cleared on it: %v", err)
		return
	}

	mine := l.principal()
	watermark := make(map[string]int64, len(deaths))
	for _, d := range deaths {
		watermark[d.TaskID] = d.SinceMS
	}
	acted := make(map[string]string)
	for _, ev := range events {
		if ev.Entity != "task" || !boardActs[ev.Kind] || ev.Actor == mine {
			continue
		}
		at, counted := watermark[ev.EntityID]
		if !counted || ev.AtMS <= at {
			continue
		}
		acted[ev.EntityID] = ev.Actor
	}
	if len(acted) == 0 {
		return
	}

	l.mu.Lock()
	kept := l.deaths[:0]
	for _, d := range l.deaths {
		if actor, ok := acted[d.TaskID]; ok {
			l.logf("task %s: %s has acted on the row, so the %d worker death(s) counted against it are forgotten",
				d.TaskID, actor, d.Count)
			continue
		}
		kept = append(kept, d)
	}
	l.deaths = kept
	l.saveLocked()
	l.mu.Unlock()
}
