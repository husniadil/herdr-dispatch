package loop

import (
	"context"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// RestoredWorkerConfirm is how long Adopt watches a pane it took back before
// deciding there is no worker in it.
//
// The failure it exists for, measured on a fleet box on 2026-08-29: the box
// was restarted with a worker mid-task. Herdr restored the pane by relaunching
// `claude --resume <session>` in the worktree, WITHOUT the `proxenos env` the
// pane's shell had been given, so the restored client came up unauthenticated
// and sat at its prompt reading "Not logged in · /goal active". hdis re-adopted
// the binding — "re-adopted 1 of 1 persisted binding(s)" — counted one live
// worker against MaxWorkers, and never asked anything else about it. The task
// stayed `doing`, claimed by that pane, until the board's 900s lease sweep, and
// even then only once a person had closed the pane by hand.
//
// The screen is not what tells those two apart: the restored pane still had the
// goal marker on it. Herdr's own status is. A worker with a /goal armed is
// driven back to work after every turn, so a pane this daemon believes it is
// driving that stays IDLE for this long has nothing armed in it.
//
// The window is long enough that a live worker between two turns is never
// mistaken for a dead one, and it is paid once per restart rather than once per
// tick.
const RestoredWorkerConfirm = 30 * time.Second

// RestoredWorkerPoll is the gap between two of those reads.
const RestoredWorkerPoll = 5 * time.Second

// restoredWorkerIsLive reports whether a pane Adopt took back still holds a
// worker, by asking Herdr what it is doing until the confirmation window runs
// out.
//
// Every answer but a clear one keeps the pane. A status Herdr could not give, a
// status it calls unknown or blocked, a single sample that is not idle — each
// is a worker that may be at work, and closing a pane on any of them is a live
// worker's task taken away mid-flight. Only idle on every sample across the
// whole window decides.
func (l *Loop) restoredWorkerIsLive(ctx context.Context, pane string) bool {
	for i, n := 0, samples(RestoredWorkerConfirm, RestoredWorkerPoll); i < n; i++ {
		if i > 0 {
			l.sleep(RestoredWorkerPoll)
		}
		a, err := l.Herdr.AgentGet(ctx, pane)
		if err != nil {
			return true
		}
		if a.Status != herdrclient.StatusIdle {
			return true
		}
		if ctx.Err() != nil {
			return true
		}
	}
	return false
}

// samples turns a window into a number of reads, always at least one.
func samples(window, poll time.Duration) int {
	if poll <= 0 || window <= poll {
		return 1
	}
	return int(window / poll)
}

// retireRestored ends a pane Adopt took back that holds no worker, and says
// why on this daemon's own trail.
//
// The pane is retired through the spawn pipeline, which is the one teardown
// there is: it closes the tab this daemon opened, removes the settings file
// the spawn wrote and drops the binding with its checkout.
//
// What it does NOT do is hand the task back on the board, and that is not an
// omission. The claimant is the pane's own `agent:<pane>` principal, and htask
// refuses both ways in of getting past that — measured against htask 0.9.1
// (contract 0.10.1) on 2026-08-29:
//
//	htask release <id> --as plugin:hdis@w1:p1
//	  FORBIDDEN: you are plugin:hdis@w1:p1 and the lease on this task is held
//	  by agent:w25:p1: only the holder may release it.
//	htask sweep --pane w25:p1 --as plugin:hdis@w1:p1
//	  FORBIDDEN: plugin:hdis@w1:p1 holds the leases of pane w25:p1; that pane
//	  or the operator releases them.
//
// So closing the pane IS the hand-back this daemon can make: it is what a
// `sweep --pane` reaction or the lease timer acts on, exactly as a dead
// worker's teardown in dead.go already relies on. The REASON is written here,
// where this daemon is the only thing that knows it — the board's own sweep can
// say a lease lapsed and nothing more.
func (l *Loop) retireRestored(ctx context.Context, pane string, row htask.Task) {
	l.logf("task %s: pane %s came back from a restart and herdr called it idle for %s, "+
		"so there is no worker in it — the pane is retired, and the task goes back when the board's "+
		"own sweep or its lease reaches it (this daemon cannot release a claim held by agent:%s)",
		row.ID, pane, RestoredWorkerConfirm, pane)
	l.retirePane(ctx, pane)
	l.emit(store.EntityWorker, KindRetired, row.ID, row.Project, map[string]any{
		"pane":   pane,
		"reason": ReasonRestoredWithNoWorker,
		"action": "adopt",
	})
}

// ReasonRestoredWithNoWorker is what the trail calls a pane a restart handed
// back with nothing working in it.
const ReasonRestoredWithNoWorker = "the pane was restored without a worker in it"

// sleep is time.Sleep unless a test replaced it.
func (l *Loop) sleep(d time.Duration) {
	if l.Sleep != nil {
		l.Sleep(d)
		return
	}
	time.Sleep(d)
}
