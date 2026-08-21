package decide

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

func pol() Policy {
	return Policy{MaxWorkers: 2, ClaimTimeout: 5 * time.Minute, MaxPrompts: 2}
}

func kinds(as []Action) []Kind {
	ks := make([]Kind, len(as))
	for i, a := range as {
		ks[i] = a.Kind
	}
	return ks
}

func one(t *testing.T, as []Action, k Kind) Action {
	t.Helper()
	var got []Action
	for _, a := range as {
		if a.Kind == k {
			got = append(got, a)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one %s, got %d in %v", k, len(got), kinds(as))
	}
	return got[0]
}

func none(t *testing.T, as []Action, k Kind) {
	t.Helper()
	for _, a := range as {
		if a.Kind == k {
			t.Fatalf("want no %s, got %+v", k, a)
		}
	}
}

// A ready task with worker capacity free is dispatched to a new worker, and
// ready order is dispatch order.
func TestAReadyTaskIsSpawnedInOrder(t *testing.T) {
	s := Snapshot{Ready: []string{"7", "9"}, Now: t0}
	as := Decide(s, pol())
	if len(as) != 2 || as[0] != (Action{Kind: Spawn, TaskID: "7"}) || as[1] != (Action{Kind: Spawn, TaskID: "9"}) {
		t.Fatalf("got %+v", as)
	}
}

// Capacity counts live bindings: with MaxWorkers 2 and two bindings, a third
// ready task waits.
func TestCapacityHoldsTheThirdTask(t *testing.T) {
	s := Snapshot{
		Ready:  []string{"3"},
		Tasks:  map[string]Task{"1": {ID: "1", Status: "doing", ClaimedBy: "wA:p1"}, "2": {ID: "2", Status: "doing", ClaimedBy: "wA:p2"}},
		Agents: map[string]string{"wA:p1": "working", "wA:p2": "working"},
		Bindings: []Binding{
			{TaskID: "1", Pane: "wA:p1", PromptedAt: t0},
			{TaskID: "2", Pane: "wA:p2", PromptedAt: t0},
		},
		Now: t0,
	}
	none(t, Decide(s, pol()), Spawn)
}

// A bound task still unclaimed after ClaimTimeout is prompted again; before
// the timeout it is left alone.
func TestAnUnclaimedGoalIsResentAfterTheTimeout(t *testing.T) {
	b := Binding{TaskID: "4", Pane: "wA:p3", PromptedAt: t0, Prompts: 1}
	s := Snapshot{
		Tasks:    map[string]Task{"4": {ID: "4", Status: "todo"}},
		Agents:   map[string]string{"wA:p3": "idle"},
		Bindings: []Binding{b},
		Now:      t0.Add(time.Minute),
	}
	none(t, Decide(s, pol()), Prompt)

	s.Now = t0.Add(6 * time.Minute)
	a := one(t, Decide(s, pol()), Prompt)
	if a.TaskID != "4" || a.Pane != "wA:p3" || a.Reason != ReasonUnclaimed {
		t.Fatalf("got %+v", a)
	}
}

// When the prompt budget is spent and the task is still unclaimed, the
// dispatcher gives up loudly instead of prompting forever.
func TestASpentPromptBudgetGivesUp(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"4": {ID: "4", Status: "todo"}},
		Agents:   map[string]string{"wA:p3": "idle"},
		Bindings: []Binding{{TaskID: "4", Pane: "wA:p3", PromptedAt: t0, Prompts: 2}},
		Now:      t0.Add(10 * time.Minute),
	}
	as := Decide(s, pol())
	a := one(t, as, GiveUp)
	if a.TaskID != "4" || a.Pane != "wA:p3" {
		t.Fatalf("got %+v", a)
	}
	none(t, as, Prompt)
}

// A bound task that reached review is notified exactly once; the Notified
// mark suppresses a second notification.
func TestReviewIsNotifiedOnce(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"5": {ID: "5", Status: "review", ClaimedBy: "wA:p4"}},
		Agents:   map[string]string{"wA:p4": "idle"},
		Bindings: []Binding{{TaskID: "5", Pane: "wA:p4", PromptedAt: t0, Prompts: 1}},
		Now:      t0.Add(time.Hour),
	}
	a := one(t, Decide(s, pol()), Notify)
	if a.TaskID != "5" {
		t.Fatalf("got %+v", a)
	}

	s.Bindings[0].Notified = true
	none(t, Decide(s, pol()), Notify)
}

// A terminal task retires its worker pane; the binding ends with it.
func TestATerminalTaskRetiresItsPane(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		s := Snapshot{
			Tasks:    map[string]Task{"6": {ID: "6", Status: status}},
			Agents:   map[string]string{"wA:p5": "idle"},
			Bindings: []Binding{{TaskID: "6", Pane: "wA:p5", PromptedAt: t0, Prompts: 1}},
			Now:      t0,
		}
		a := one(t, Decide(s, pol()), Retire)
		if a.Pane != "wA:p5" || a.TaskID != "6" {
			t.Fatalf("%s: got %+v", status, a)
		}
	}
}

// A pane that is gone is never retired (there is nothing to close) and never
// prompted; the binding is dropped and the board's own sweep brings the task
// back to ready.
func TestAGonePaneOnlyUnbinds(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"8": {ID: "8", Status: "doing", ClaimedBy: "wA:p6"}},
		Agents:   map[string]string{},
		Bindings: []Binding{{TaskID: "8", Pane: "wA:p6", PromptedAt: t0, Prompts: 1}},
		Now:      t0.Add(time.Hour),
	}
	as := Decide(s, pol())
	a := one(t, as, Unbind)
	if a.TaskID != "8" {
		t.Fatalf("got %+v", a)
	}
	none(t, as, Retire)
	none(t, as, Prompt)
	none(t, as, GiveUp)
}

// A claimed task whose worker sits idle stopped without submitting, or came
// back from a reject: the worker is prompted to continue, on the same pane.
func TestAnIdleWorkerOnADoingTaskIsReprompted(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"9": {ID: "9", Status: "doing", ClaimedBy: "wA:p7"}},
		Agents:   map[string]string{"wA:p7": "idle"},
		Bindings: []Binding{{TaskID: "9", Pane: "wA:p7", PromptedAt: t0, Prompts: 1}},
		Now:      t0.Add(time.Minute),
	}
	a := one(t, Decide(s, pol()), Prompt)
	if a.TaskID != "9" || a.Pane != "wA:p7" || a.Reason != ReasonStalled {
		t.Fatalf("got %+v", a)
	}
}

// A working worker on a claimed task needs nothing.
func TestAWorkingWorkerIsLeftAlone(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"9": {ID: "9", Status: "doing", ClaimedBy: "wA:p7"}},
		Agents:   map[string]string{"wA:p7": "working"},
		Bindings: []Binding{{TaskID: "9", Pane: "wA:p7", PromptedAt: t0, Prompts: 1}},
		Now:      t0.Add(time.Minute),
	}
	if as := Decide(s, pol()); len(as) != 0 {
		t.Fatalf("got %+v", as)
	}
}

// A task claimed by a pane other than the bound one was taken by someone
// else: the dispatcher retires its own idle worker and lets go.
func TestATaskClaimedElsewhereReleasesOurWorker(t *testing.T) {
	s := Snapshot{
		Tasks:    map[string]Task{"10": {ID: "10", Status: "doing", ClaimedBy: "wB:p1"}},
		Agents:   map[string]string{"wA:p8": "idle"},
		Bindings: []Binding{{TaskID: "10", Pane: "wA:p8", PromptedAt: t0, Prompts: 1}},
		Now:      t0.Add(time.Minute),
	}
	a := one(t, Decide(s, pol()), Retire)
	if a.Pane != "wA:p8" || a.Reason != ReasonTakenOver {
		t.Fatalf("got %+v", a)
	}
}

// Retiring frees capacity within the same tick: a terminal task's slot is
// available to a ready task in one Decide call.
func TestARetiredSlotIsReusedInTheSameTick(t *testing.T) {
	p := pol()
	p.MaxWorkers = 1
	s := Snapshot{
		Ready:    []string{"12"},
		Tasks:    map[string]Task{"11": {ID: "11", Status: "done"}},
		Agents:   map[string]string{"wA:p9": "idle"},
		Bindings: []Binding{{TaskID: "11", Pane: "wA:p9", PromptedAt: t0, Prompts: 1}},
		Now:      t0,
	}
	as := Decide(s, p)
	one(t, as, Retire)
	a := one(t, as, Spawn)
	if a.TaskID != "12" {
		t.Fatalf("got %+v", a)
	}
}
