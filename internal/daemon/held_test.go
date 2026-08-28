package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// doctor is where an operator asks why nothing is moving. A slot spent on a
// pane that has submitted and is waiting for a human has to be in that answer.
func TestDoctorCountsTheSlotsHeldByPanesAwaitingReview(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	if err := d.Loop.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","revision":1}]}}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review"}}`)
	if err := d.Loop.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.Workers != 1 || rep.AwaitingReview != 1 {
		t.Fatalf("doctor report: %d workers, %d awaiting review", rep.Workers, rep.AwaitingReview)
	}
}

// A task nothing will dispatch again, because the workers put on it keep
// dying, is exactly the state the operator has to be told about: everything
// else about the fleet looks healthy while that task sits still forever.
// doctor is where they ask, and the count travels with the task through the
// restart that re-read it.
func TestDoctorNamesTheTasksWhoseWorkersKeepDying(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	if err := d.Loop.Store.Save(store.State{Deaths: []store.Death{
		{TaskID: "01AAA", Project: "/src/p", Count: 2},
		{TaskID: "01BBB", Project: "/src/p", Count: 1},
	}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := d.Loop.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	raw, err := call(t, d, protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var rep DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	// Only the one at the cap: a task with one death behind it is still
	// dispatched, so naming it here would send the operator after a task
	// nothing is holding back.
	if len(rep.WorkersDied) != 1 {
		t.Fatalf("doctor names %d held-back task(s): %+v", len(rep.WorkersDied), rep.WorkersDied)
	}
	if rep.WorkersDied[0].TaskID != "01AAA" || rep.WorkersDied[0].Deaths != 2 {
		t.Fatalf("doctor names %+v", rep.WorkersDied[0])
	}
}
