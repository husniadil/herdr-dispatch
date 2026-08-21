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

// The lane is off unless the policy turns it on. A task reaching review is
// announced and nothing else.
func TestTheVerificationLaneIsOffByDefault(t *testing.T) {
	got := Decide(verifySnapshot(), Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2})
	if has(got, SpawnVerifier) {
		t.Fatalf("a verifier was spawned with the lane off: %v", actionKinds(got))
	}
	if !has(got, Notify) {
		t.Fatalf("review was not announced: %v", actionKinds(got))
	}
}

// With the lane on, the same review earns a verifier alongside the
// announcement, on a pane of its own.
func TestAReviewedTaskEarnsAVerifierWhenTheLaneIsOn(t *testing.T) {
	got := Decide(verifySnapshot(), Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	var spawn Action
	for _, a := range got {
		if a.Kind == SpawnVerifier {
			spawn = a
		}
	}
	if spawn.Kind == "" {
		t.Fatalf("no verifier was spawned: %v", actionKinds(got))
	}
	if spawn.TaskID != "t1" {
		t.Fatalf("verifier is for %q", spawn.TaskID)
	}
	if spawn.Pane != "" {
		t.Fatalf("a verifier takes a fresh pane, not %q", spawn.Pane)
	}
}

// One submission earns one verifier. The flag on the worker's binding is what
// says the submission has already been handed to one.
func TestOneSubmissionEarnsOneVerifier(t *testing.T) {
	s := verifySnapshot()
	s.Bindings[0].Verified = true
	s.Bindings[0].Notified = true
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if has(got, SpawnVerifier) {
		t.Fatalf("a second verifier was spawned for one submission: %v", actionKinds(got))
	}
}

// A verifier already live on the task is the same answer, even if the flag
// never landed: the pane is the thing there may only be one of.
func TestALiveVerifierIsNotJoinedByASecond(t *testing.T) {
	s := verifySnapshot()
	s.Bindings = append(s.Bindings, Binding{TaskID: "t1", Pane: "wM:p10", Kind: KindVerifier, PromptedAt: verifyNow})
	s.Agents["wM:p10"] = "working"
	got := Decide(s, Policy{MaxWorkers: 4, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if has(got, SpawnVerifier) {
		t.Fatalf("a second verifier was spawned beside a live one: %v", actionKinds(got))
	}
}

// A rejected task comes back out of review, and the announcement and the
// verification are both due again if it returns.
func TestARejectionRearmsTheAnnouncementAndTheVerification(t *testing.T) {
	s := verifySnapshot()
	s.Tasks["t1"] = Task{ID: "t1", Status: "doing", ClaimedBy: "wM:p9"}
	s.Bindings[0].Notified = true
	s.Bindings[0].Verified = true
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if !has(got, Rearm) {
		t.Fatalf("the binding was not rearmed: %v", actionKinds(got))
	}
}

// A verifier is never prompted to claim and never announced: it holds no
// claim on the board and has nothing to announce.
func TestAVerifierIsNeverPromptedOrAnnounced(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{"wM:p11": "working"},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p11", Kind: KindVerifier, PromptedAt: verifyNow.Add(-time.Hour)}},
		Now:      verifyNow,
	}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if has(got, Prompt) || has(got, Notify) || has(got, GiveUp) {
		t.Fatalf("a verifier was driven like a worker: %v", actionKinds(got))
	}
}

// A verifier that has gone idle past the grace is done: it is retired the way
// a worker is, and its pane goes with it.
func TestAFinishedVerifierIsRetired(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{"wM:p11": "idle"},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p11", Kind: KindVerifier, PromptedAt: verifyNow.Add(-time.Hour)}},
		Now:      verifyNow,
	}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if len(got) != 1 || got[0].Kind != Retire || got[0].Pane != "wM:p11" {
		t.Fatalf("a finished verifier was not retired: %+v", got)
	}
}

// Idle inside the grace is a verifier still coming up, not one that is done.
func TestAVerifierIsNotRetiredWhileItIsStillComingUp(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{"wM:p11": "idle"},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p11", Kind: KindVerifier, PromptedAt: verifyNow}},
		Now:      verifyNow,
	}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if len(got) != 0 {
		t.Fatalf("a verifier still coming up was acted on: %+v", got)
	}
}

// The submission the verifier was checking is settled, so there is nothing
// left to verify whatever the verifier is still doing.
func TestAVerifierIsRetiredWhenTheSubmissionLeavesReview(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "doing", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{"wM:p11": "working"},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p11", Kind: KindVerifier, PromptedAt: verifyNow.Add(-time.Hour)}},
		Now:      verifyNow,
	}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if len(got) != 1 || got[0].Kind != Retire {
		t.Fatalf("a verifier outliving its submission was not retired: %+v", got)
	}
}

// A verifier whose pane died is dropped like any binding, and nothing is done
// to the pane that is already gone.
func TestAVerifierWhosePaneDiedIsDropped(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"t1": {ID: "t1", Status: "review", ClaimedBy: "wM:p9"}},
		Agents:   map[string]string{},
		Bindings: []Binding{{TaskID: "t1", Pane: "wM:p11", Kind: KindVerifier, PromptedAt: verifyNow}},
		Now:      verifyNow,
	}
	got := Decide(s, Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true})
	if len(got) != 1 || got[0].Kind != Unbind || got[0].Pane != "wM:p11" {
		t.Fatalf("a dead verifier pane was not unbound: %+v", got)
	}
}

// An empty kind is a worker: bindings written before the lane existed read
// back as what they were.
func TestABindingWithNoKindIsAWorker(t *testing.T) {
	var b Binding
	if b.IsVerifier() {
		t.Fatal("a binding with no kind read as a verifier")
	}
	if (Binding{Kind: KindVerifier}).IsVerifier() != true {
		t.Fatal("a verifier binding did not read as one")
	}
}
