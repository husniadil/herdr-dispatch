package decide

import (
	"testing"
	"time"
)

var verifyNow = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// verifySnapshot is one worker binding whose task has just reached review.
func verifySnapshot() Snapshot {
	return Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{"wM:p9": "working"},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p9", Kind: KindWorker, PromptedAt: verifyNow.Add(-time.Hour)}},
		Now:      verifyNow,
	}
}

func actionKinds(actions []Action) []Kind {
	out := make([]Kind, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Kind)
	}
	return out
}

func has(actions []Action, k Kind) bool {
	for _, a := range actions {
		if a.Kind == k {
			return true
		}
	}
	return false
}

// selfReviews are the self-review shots in a tick's actions.
func selfReviews(actions []Action) []Action {
	var out []Action
	for _, a := range actions {
		if a.Kind == Prompt && a.Reason == ReasonSelfReview {
			out = append(out, a)
		}
	}
	return out
}

// The lane is off unless the policy turns it on. A task reaching review is
// announced and nothing else.
func TestTheVerificationLaneIsOffByDefault(t *testing.T) {
	got := Decide(verifySnapshot(), Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2})
	if len(selfReviews(got)) != 0 {
		t.Fatalf("a self-review shot was sent with the lane off: %+v", got)
	}
	if !has(got, Notify) {
		t.Fatalf("review was not announced: %v", actionKinds(got))
	}
}

// With the lane on, the same review earns a self-review shot in the pane that
// did the work. No second pane is brought up for it: the whole point of the
// lane is the context the worker's own pane already holds.
func TestAReviewedTaskEarnsASelfReviewShotInTheWorkersOwnPane(t *testing.T) {
	got := Decide(verifySnapshot(), Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	shots := selfReviews(got)
	if len(shots) != 1 {
		t.Fatalf("want one self-review shot, got %+v", got)
	}
	if shots[0].TaskID != "t1" {
		t.Fatalf("the shot is for %q", shots[0].TaskID)
	}
	if shots[0].Pane != "wM:p9" {
		t.Fatalf("the shot went to %q, not the worker's own pane", shots[0].Pane)
	}
	if has(got, Spawn) {
		t.Fatalf("a second pane was brought up for the review: %v", actionKinds(got))
	}
}

// One submission earns one shot. The flag on the worker's binding is what says
// the submission has already had it.
func TestOneSubmissionEarnsOneSelfReviewShot(t *testing.T) {
	s := verifySnapshot()
	s.Bindings[0].Verified = true
	s.Bindings[0].Notified = true
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if len(selfReviews(got)) != 0 {
		t.Fatalf("a second shot was sent for one submission: %+v", got)
	}
}

// A NEW submission earns another. Rearm is what clears the flag when the task
// leaves review, and a task back in review with a cleared flag is shot again.
func TestANewSubmissionEarnsAnotherSelfReviewShot(t *testing.T) {
	s := verifySnapshot()
	s.Tasks["t1"] = Task{ID: "t1", Status: "doing", ClaimedBy: "wM:p9"}
	s.Bindings[0].Notified = true
	s.Bindings[0].Verified = true
	p := Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true}
	got := Decide(s, p)
	if !has(got, Rearm) {
		t.Fatalf("the binding was not rearmed: %v", actionKinds(got))
	}

	// The adapter's Rearm clears both flags; the next submission is a fresh
	// review with a cleared binding.
	s = verifySnapshot()
	got = Decide(s, p)
	if len(selfReviews(got)) != 1 {
		t.Fatalf("the new submission earned no shot: %+v", got)
	}
}

// The shot is not a nudge: a worker sitting in review is not stalled, and the
// only prompt a reviewed task earns is the self-review one.
func TestAWorkerInReviewIsNotNudged(t *testing.T) {
	s := verifySnapshot()
	s.Agents["wM:p9"] = "idle"
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	for _, a := range got {
		if a.Kind == Prompt && a.Reason != ReasonSelfReview {
			t.Fatalf("a worker in review was nudged: %+v", a)
		}
	}
}

// The shot costs no worker slot, because it takes no pane. A board with work
// waiting still fills the slot the verifier lane used to take.
func TestASelfReviewShotHoldsNoExtraWorkerSlot(t *testing.T) {
	s := verifySnapshot()
	s.Ready = []string{"t2"}
	s.Tasks["t2"] = Task{ID: "t2", Status: "todo"}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if !has(got, Spawn) {
		t.Fatalf("the free slot was held by the review: %v", actionKinds(got))
	}
}

// §11.4: a plugin MUST NOT treat a successful `agent prompt` as delivery.
// Herdr accepting the text says it was accepted and nothing more — the agent
// TUI can collapse a paste, and a pane can exit between the call and the
// agent's next turn.
//
// Marking the shot spent on the strength of that call is what made the lane
// fail silently: Rearm only clears the mark when the task LEAVES review, so a
// submission whose shot was accepted and seen by nobody had its one check
// burned with the board still green and nothing anywhere saying so.
//
// So idle is what re-opens it. A worker that got the condition is working and
// is never asked twice; one that never saw it is asked again.
func TestASelfReviewShotHerdrAcceptedIsNotTreatedAsDelivered(t *testing.T) {
	now := time.Unix(2000, 0)
	p := Policy{MaxWorkers: 4, MaxPrompts: 3, ClaimTimeout: 30 * time.Second, Verify: true}
	base := Binding{TaskID: "t1", Pane: "wM:p9", Verified: true, Prompts: 1,
		PromptedAt: now.Add(-time.Minute)}
	snap := func(b Binding, status string) Snapshot {
		return Snapshot{
			Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
			Agents:   map[string]string{"wM:p9": status},
			Bindings: []Binding{b},
			Now:      now,
		}
	}
	shots := func(acts []Action) int {
		n := 0
		for _, a := range acts {
			if a.Kind == Prompt && a.Reason == ReasonSelfReview {
				n++
			}
		}
		return n
	}

	if got := shots(Decide(snap(base, "idle"), p)); got != 1 {
		t.Errorf("a worker herdr still calls idle got %d further shots, want 1: the accepted "+
			"prompt reached nothing and the submission's only check is gone", got)
	}
	if got := shots(Decide(snap(base, "working"), p)); got != 0 {
		t.Errorf("a worker that is working got %d further shots, want 0: it has the condition", got)
	}
	spent := base
	spent.Prompts = p.MaxPrompts
	if got := shots(Decide(snap(spent, "idle"), p)); got != 0 {
		t.Errorf("a pane idle for its own reasons got %d shots past MaxPrompts, want 0", got)
	}
	fresh := base
	fresh.PromptedAt = now
	if got := shots(Decide(snap(fresh, "idle"), p)); got != 0 {
		t.Errorf("a shot sent this instant was repeated %d times, want 0: a worker mid-turn "+
			"must not meet a second copy", got)
	}
	first := base
	first.Verified = false
	if got := shots(Decide(snap(first, "working"), p)); got != 1 {
		t.Errorf("a submission with no shot sent yet got %d, want 1", got)
	}
}
