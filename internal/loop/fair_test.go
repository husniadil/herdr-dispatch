package loop

import (
	"context"
	"testing"
)

// The fairness rule shares the ready list out by project, so the snapshot has
// to carry each ready row's project. Without it every board reads as the same
// one and the rule has nothing to be fair between.
func TestTheSnapshotCarriesTheProjectOfEveryReadyRow(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[
{"id":"01AAA","seq":7,"project":"/src/alpha","title":"alpha work","status":"todo"},
{"id":"01BBB","seq":8,"project":"/src/beta","title":"beta work","status":"todo"}],"count":2}`)

	snap, err := l.snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Ready) != 2 {
		t.Fatalf("ready: %v", snap.Ready)
	}
	want := map[string]string{"01AAA": "/src/alpha", "01BBB": "/src/beta"}
	for id, project := range want {
		got, known := snap.Tasks[id]
		if !known {
			t.Fatalf("ready task %s is missing from the snapshot's rows", id)
		}
		if got.Project != project {
			t.Fatalf("task %s: want project %q, got %q", id, project, got.Project)
		}
	}
}

// A pane that has submitted and is waiting for a human still holds its slot.
// Status has to say so, or an operator who sees nothing moving has only the
// bindings file to learn it from.
func TestStatusMarksAWorkerHoldingItsSlotWhileAwaitingReview(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"},{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","revision":1}]}}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review"},"ready":false,"dependents":[]}`)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if !st.Workers[0].AwaitingReview {
		t.Fatalf("the worker on a row in review is not marked as awaiting review: %+v", st.Workers[0])
	}
	if got := l.AwaitingReview(); got != 1 {
		t.Fatalf("want 1 slot held awaiting review, got %d", got)
	}
}

// A worker still doing the task holds its slot for work, and must not be
// counted among the ones waiting on a human.
func TestAWorkingWorkerIsNotCountedAsAwaitingReview(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"},{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","revision":1}]}}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","pane":"wM:p9"},"ready":false,"dependents":[]}`)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].AwaitingReview {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if got := l.AwaitingReview(); got != 0 {
		t.Fatalf("want 0 slots held awaiting review, got %d", got)
	}
}
