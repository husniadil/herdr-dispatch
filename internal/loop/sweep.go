package loop

import (
	"context"
	"errors"
	"strings"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/htask"
)

// sweepClaim hands back the claim a pane left behind, and writes what happened
// into the detail of the event the caller is about to file.
//
// This is §11.7 and nothing wider. A pane dies with the machine it ran on and
// cannot sweep itself; the operator is asleep; and until the board opened this
// door the work sat in `doing` under a principal that no longer existed until
// the lease lapsed — measured against htask 0.9.1 on 2026-08-29, both ways in
// were FORBIDDEN and the only thing this daemon could do was say so.
//
// The authority is HERDR's answer and never this daemon's belief. The board
// asks Herdr itself and refuses a pane it still lists, so the worst this call
// can do to a live worker is be refused. That is why it is sent rather than
// guarded here: a second opinion computed from this tick's own pane list would
// be a race, and the board's is the one that decides.
//
// The four answers are four different things to do:
//
//	released      the claim is back; the ids are named on the trail.
//	FORBIDDEN     the pane is alive after all. Never retried: asking again
//	              asks the same question of the same Herdr.
//	UNAVAILABLE   Herdr could not be asked. Owed, and the next tick asks.
//	TIMEOUT       the same, for a Herdr that did not answer in time.
//	UNSUPPORTED   this Herdr cannot list panes at all, so no amount of asking
//	              will change it: the board's lease is the fallback, said once.
//
// The call is idempotent, so a retry costs a call and nothing else.
func (l *Loop) sweepClaim(ctx context.Context, taskID, pane string, detail map[string]any) {
	l.sweep(ctx, taskID, pane, detail, false)
}

// sweep is sweepClaim with the retry flag a repeated attempt carries: it says
// "still owed" once, when the claim is first deferred, rather than every tick
// a board or a Herdr stays down.
func (l *Loop) sweep(ctx context.Context, taskID, pane string, detail map[string]any, retry bool) {
	released, err := l.Board.SweepPane(ctx, pane)
	if err == nil {
		detail["released"] = released
		if len(released) == 0 {
			l.logf("task %s: pane %s is gone and the board was holding nothing for it", taskID, pane)
			return
		}
		l.logf("task %s: pane %s is gone, and the board released %s back to the queue",
			taskID, pane, strings.Join(released, ", "))
		return
	}

	var refusal *htask.Refusal
	if !errors.As(err, &refusal) {
		// Not the board answering: it could not be reached at all, which is
		// the same "ask again" as a Herdr that could not be asked.
		detail["sweep_retry"] = true
		l.oweSweep(taskID, pane)
		if !retry {
			l.logf("task %s: the claim pane %s left could not be handed back and will be asked again: %v",
				taskID, pane, err)
		}
		return
	}
	detail["sweep_refused"] = refusal.Code
	switch codes.Code(refusal.Code) {
	case codes.Forbidden:
		// Herdr still lists the pane, so this daemon was wrong about it being
		// gone, and the claim is the pane's own to give back.
		l.logf("task %s: the board will not hand back pane %s's claim, because %s; nothing is retried",
			taskID, pane, refusal.Message)
	case codes.Unavailable, codes.Timeout:
		detail["sweep_retry"] = true
		l.oweSweep(taskID, pane)
		if !retry {
			l.logf("task %s: pane %s's claim is still owed — the board could not ask herdr (%s: %s) — "+
				"so the next tick asks again", taskID, pane, refusal.Code, refusal.Message)
		}
	case codes.Unsupported:
		// A Herdr too old to list panes will answer this way forever, and the
		// board's own lease is what returns the task instead.
		l.logf("task %s: pane %s's claim cannot be handed back on this herdr (%s: %s), so the task "+
			"comes back when the board's lease reaches it", taskID, pane, refusal.Code, refusal.Message)
	default:
		l.logf("task %s: the board refused to hand back pane %s's claim (%s: %s)",
			taskID, pane, refusal.Code, refusal.Message)
	}
}

// oweSweep notes a claim this daemon still owes the board, so the next tick
// asks again.
func (l *Loop) oweSweep(taskID, pane string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owedSweeps == nil {
		l.owedSweeps = map[string]string{}
	}
	l.owedSweeps[pane] = taskID
}

// sweepOwed asks again for every claim a previous tick could not hand back.
//
// A pane that answers — released, or refused in a way that will not change —
// is dropped from the note; one that still cannot be asked stays, and the tick
// after this one asks again. A retry that fails the same way says nothing,
// because a board or a Herdr that is down would otherwise write the same line
// every tick until it is back; a retry that succeeds or is refused for good
// says so, because that is news.
func (l *Loop) sweepOwed(ctx context.Context) {
	l.mu.Lock()
	owed := make(map[string]string, len(l.owedSweeps))
	for pane, taskID := range l.owedSweeps {
		owed[pane] = taskID
	}
	l.mu.Unlock()

	for pane, taskID := range owed {
		detail := map[string]any{}
		l.sweep(ctx, taskID, pane, detail, true)
		if detail["sweep_retry"] == true {
			continue
		}
		l.mu.Lock()
		delete(l.owedSweeps, pane)
		l.mu.Unlock()
	}
}
