package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/loop"
)

// The one phrase both surfaces use for a slot that is spent on a pane doing
// nothing but waiting for a human. An operator who sees nothing moving reads
// it in one command instead of the bindings file.
const heldPhrase = "holding a slot while awaiting review"

func TestStatusSaysAWorkerIsHoldingItsSlotWhileAwaitingReview(t *testing.T) {
	raw, err := json.Marshal(loop.Status{
		BasePane: "wM:p1", MaxWorkers: 2,
		Workers: []loop.Worker{
			{Seq: 24, Pane: "wM:p4V", AgentStatus: "idle", PaneAlive: true, Notified: true, AwaitingReview: true, Title: "submitted work"},
			{Seq: 25, Pane: "wM:pG", AgentStatus: "working", PaneAlive: true, Title: "live work"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Write("status", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("printed %d lines: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], heldPhrase) {
		t.Errorf("want %q in %q", heldPhrase, lines[0])
	}
	if strings.Contains(lines[1], heldPhrase) {
		t.Errorf("a working worker was said to be awaiting review: %q", lines[1])
	}
}

func TestDoctorSaysHowManySlotsAreHeldByPanesAwaitingReview(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/hdis.sock","base_pane":"wM:p1","max_workers":4,"workers":4,"awaiting_review":2,"pending":0,"interval":"15s","board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), heldPhrase) {
		t.Fatalf("doctor printed %q", out.String())
	}
	if !strings.Contains(out.String(), "2 "+heldPhrase) {
		t.Fatalf("doctor did not say how many: %q", out.String())
	}
}

func TestDoctorSaysNothingAboutHeldSlotsWhenNoneAre(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/hdis.sock","base_pane":"wM:p1","max_workers":4,"workers":2,"awaiting_review":0,"pending":0,"interval":"15s","board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.Contains(out.String(), heldPhrase) {
		t.Fatalf("doctor printed %q", out.String())
	}
}
