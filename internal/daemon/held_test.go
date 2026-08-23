package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/protocol"
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
