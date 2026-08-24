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
	l, _ := newLoop(t)
	l.BasePane = ""

	if got := l.EnsureBase(context.Background()); got != "" {
		t.Fatalf("EnsureBase() = %q, want empty", got)
	}
}

// A daemon that inherited a pane keeps it. The operator's own answer is not
// re-decided every tick, and Herdr is not asked at all.
func TestADaemonThatAlreadyHasABaseAdoptsNothing(t *testing.T) {
	l, f := newLoop(t)

	if got, want := l.EnsureBase(context.Background()), "wM:p1"; got != want {
		t.Fatalf("EnsureBase() = %q, want %q", got, want)
	}
	if got := calls(t, f, "pane list"); len(got) != 0 {
		t.Fatalf("a daemon with a base asked herdr anyway: %v", got)
	}
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
	l, _ := newLoop(t)
	l.BasePane = ""

	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
}
