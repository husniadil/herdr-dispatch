package loop

import (
	"context"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
)

// The case this exists for: a daemon Herdr's plugin manager started at boot.
// It has no HERDR_PANE_ID and the config deliberately names no pane, so
// without this it refuses every dispatch for as long as it runs.
func TestAPaneLessDaemonAdoptsALivePaneAsItsBase(t *testing.T) {
	l, f := newLoop(t)
	l.BasePane = ""
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1K:p1","workspace_id":"w1K","tab_id":"w1K:t1","agent_status":"idle"}]}}`)

	if got, want := l.EnsureBase(context.Background()), "w1K:p1"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q", got, want)
	}
	if got, want := l.Base(), "w1K:p1"; got != want {
		t.Fatalf("the adopted base did not stick: Base() = %q, want %q", got, want)
	}
}

// A worker pane is not a base. Splitting the next worker off one would put it
// in a tab this dispatcher opened for someone else's task, and retiring that
// task would take the base with it.
func TestAdoptingABaseSkipsThisDispatchersOwnTabs(t *testing.T) {
	l, f := newLoop(t)
	l.BasePane = ""
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[{"tab_id":"w1K:t9","workspace_id":"w1K","label":"hdis task 7","pane_count":1}]}}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
		`{"pane_id":"w1K:p9","workspace_id":"w1K","tab_id":"w1K:t9","agent_status":"working"},`+
		`{"pane_id":"w1K:p2","workspace_id":"w1K","tab_id":"w1K:t1","agent_status":"idle"}]}}`)

	if got, want := l.EnsureBase(context.Background()), "w1K:p2"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q", got, want)
	}
}

// The lowest pane id, for the reason spawn.inProject picks one: the same
// screen resolves to the same base on every attempt, so a daemon that
// adopts late does not wander between windows.
func TestAdoptingABaseTakesTheLowestPaneIDSoItIsStable(t *testing.T) {
	panes := []herdrclient.Agent{
		{PaneID: "w1K:p7", TabID: "w1K:t1"},
		{PaneID: "w1K:p2", TabID: "w1K:t1"},
		{PaneID: "w1K:p4", TabID: "w1K:t1"},
	}
	if got, want := pickBase(panes, nil, nil), "w1K:p2"; got != want {
		t.Fatalf("pickBase() = %q, want %q", got, want)
	}
}

// A pane this daemon is already driving is never the base either, whatever
// its tab is labelled: the binding is the evidence the tab label cannot give.
func TestAdoptingABaseSkipsAPaneAlreadyBoundToATask(t *testing.T) {
	panes := []herdrclient.Agent{
		{PaneID: "w1K:p2", TabID: "w1K:t1"},
		{PaneID: "w1K:p5", TabID: "w1K:t1"},
	}
	if got, want := pickBase(panes, nil, map[string]bool{"w1K:p2": true}), "w1K:p5"; got != want {
		t.Fatalf("pickBase() = %q, want %q", got, want)
	}
}

// Herdr with nothing to adopt leaves the daemon exactly as it was: still
// pane-less, still saying so, and free to ask again on the next tick.
func TestABaseIsNotAdoptedWhenHerdrHasNoPaneToOffer(t *testing.T) {
	l, f := newLoop(t)
	l.BasePane = ""
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)

	if got := l.EnsureBase(context.Background()); got != "" {
		t.Fatalf("EnsureBase() = %q, want empty", got)
	}
}

// A daemon that inherited a live pane keeps it. Herdr is asked whether the
// pane is still there — that is the one question — and never asked which pane
// would be better: the operator's own answer is not re-decided every tick.
func TestADaemonWhoseBasePaneIsStillLiveKeepsIt(t *testing.T) {
	l, f := newLoop(t)

	if got, want := l.EnsureBase(context.Background()), "wM:p1"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q", got, want)
	}
	if got := calls(t, f, "tab list"); len(got) != 0 {
		t.Fatalf("a daemon with a live base went looking for another one: %v", got)
	}
}

// The failure this exists for, measured on 2026-08-29: a worker in bypass mode
// ran `herdr workspace close w3`, taking this daemon's own base pane with it,
// and hdis went on reporting `base_pane w3:p1` while `herdr pane list` had no
// w3 at all. Nothing refused — the base is only read for the workspace a
// worker's tab opens in — so every spawn after that landed wherever Herdr
// chose, and the recorded base and the pane placement used were two different
// answers.
func TestABasePaneHerdrNoLongerHoldsIsReplaced(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
		`{"pane_id":"w9:p4","workspace_id":"w9","tab_id":"w9:t1","agent_status":"idle"}]}}`)

	if got, want := l.EnsureBase(context.Background()), "w9:p4"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q: the base that is gone was kept", got, want)
	}
	if got, want := l.Base(), "w9:p4"; got != want {
		t.Fatalf("Base() = %q, want %q: the replacement did not stick", got, want)
	}
}

// And a base with nothing to replace it is dropped rather than kept: a pane id
// Herdr does not hold is not an address, and dispatch refusing by name is a
// better answer than a placement nobody can see.
func TestABasePaneHerdrNoLongerHoldsIsDroppedWhenNothingCanReplaceIt(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)

	if got := l.EnsureBase(context.Background()); got != "" {
		t.Fatalf("EnsureBase() = %q, want empty", got)
	}
	if got := l.Base(); got != "" {
		t.Fatalf("Base() = %q, want empty: a pane herdr does not hold was kept as the base", got)
	}
}

// A Herdr that cannot be reached is not a pane that is gone. Dropping a live
// base on an unreadable pane list would refuse every dispatch until Herdr
// answered again, which is a failure this daemon invents for itself.
func TestAnUnreadablePaneListLeavesTheBaseExactlyAsItWas(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "herdr", `echo "herdr is down" >&2; exit 1`)

	if got, want := l.EnsureBase(context.Background()), "wM:p1"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q", got, want)
	}
	if got, want := l.Base(), "wM:p1"; got != want {
		t.Fatalf("Base() = %q, want %q", got, want)
	}
	_ = f
}

// The refusal is the last word, not the first: a dispatch on a pane-less
// daemon adopts a base and goes through.
func TestDispatchAdoptsABaseRatherThanRefusingWhenHerdrHasOne(t *testing.T) {
	l, f := newLoop(t)
	l.BasePane = ""
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"w1K:p1","workspace_id":"w1K","tab_id":"w1K:t1","agent_status":"idle"}]}}`)

	res, err := l.Dispatch(context.Background(), "7", "")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.TaskID != "01AAA" {
		t.Fatalf("reservation: %+v", res)
	}
}

// And when there is nothing to adopt the refusal is unchanged, with the same
// name a caller could read before.
func TestDispatchStillRefusesWhenThereIsNoPaneToAdopt(t *testing.T) {
	l, f := newLoop(t)
	l.BasePane = ""
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)

	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
}
