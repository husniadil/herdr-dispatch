package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

func verb(t *testing.T, name string) verbs.Verb {
	t.Helper()
	v, ok := verbs.ByName(name)
	if !ok {
		t.Fatalf("no verb named %q", name)
	}
	return v
}

func TestAPositionalBecomesTheDeclaredArgument(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "wM:p4")

	req, asJSON, err := Request(verb(t, "dispatch"), []string{"7"})
	if err != nil {
		t.Fatalf("dispatch 7: %v", err)
	}
	if asJSON {
		t.Error("--json was not asked for")
	}
	if req.Verb != "dispatch" || req.Args["task"] != "7" {
		t.Fatalf("request: %+v", req)
	}
	if req.Pane != "wM:p4" || req.Door != Door {
		t.Fatalf("request: %+v", req)
	}
}

func TestTheFlagIsReadWhereverItSits(t *testing.T) {
	req, asJSON, err := Request(verb(t, "dispatch"), []string{"--json", "7"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !asJSON || req.Args["task"] != "7" {
		t.Fatalf("json=%t request=%+v", asJSON, req)
	}
}

func TestAMissingRequiredPositionalIsRefused(t *testing.T) {
	_, _, err := Request(verb(t, "dispatch"), nil)
	if got, want := codes.Of(err), codes.Invalid; got != want {
		t.Fatalf("dispatch with nothing = %v (%q), want %q", err, got, want)
	}
}

func TestAnArgumentTheVerbDoesNotTakeIsRefused(t *testing.T) {
	_, _, err := Request(verb(t, "status"), []string{"7"})
	if got, want := codes.Of(err), codes.Invalid; got != want {
		t.Fatalf("status 7 = %v (%q), want %q", err, got, want)
	}
}

// --json is the daemon's own bytes, unchanged: it is the same document the
// MCP door hands its caller.
func TestJSONPrintsTheDaemonsOwnBytes(t *testing.T) {
	raw := json.RawMessage(`{"base_pane":"wM:p1","max_workers":2,"workers":[],"pending":[]}`)
	var out strings.Builder
	if err := Write("status", raw, true, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != string(raw) {
		t.Fatalf("printed %q, want %q", got, string(raw))
	}
}

func TestStatusPrintsOneLinePerWorkerAndPerReservation(t *testing.T) {
	st := loop.Status{
		BasePane: "wM:p1", MaxWorkers: 2,
		Workers: []loop.Worker{{
			TaskID: "01AAA", Seq: 7, Title: "do the thing", Pane: "wM:p9",
			AgentStatus: "working", PaneAlive: true,
			PromptedAt: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC), Prompts: 1,
		}},
		Pending: []string{"01BBB"},
	}
	raw, err := json.Marshal(st)
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
	for _, want := range []string{"#7", "wM:p9", "working", "do the thing"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("want %q in %q", want, lines[0])
		}
	}
	if !strings.Contains(lines[1], "01BBB") || !strings.Contains(lines[1], "reserved") {
		t.Errorf("the reservation line reads %q", lines[1])
	}
}

// A pane herdr no longer lists is said plainly rather than shown as an empty
// column an operator has to interpret.
func TestAWorkerWhosePaneIsGoneSaysSo(t *testing.T) {
	raw, err := json.Marshal(loop.Status{Workers: []loop.Worker{{Seq: 7, Pane: "wM:p9", PaneAlive: false}}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Write("status", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "pane gone") {
		t.Fatalf("printed %q", out.String())
	}
}

func TestDoctorSaysWhyADispatchWouldRefuse(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/hdis.sock","base_pane":"","max_workers":2,"interval":"15s","board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "dispatch refuses") {
		t.Fatalf("doctor printed %q", out.String())
	}
}

func TestUsageListsEveryVerbInTheTable(t *testing.T) {
	usage := Usage()
	for _, v := range verbs.All {
		if !strings.Contains(usage, "hdis "+strings.Join(v.CLI, " ")) {
			t.Errorf("usage does not list %q", v.Name)
		}
	}
	for _, want := range []string{"hdis daemon", "hdis mcp", "hdis version", "<task>"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
}

// The no-daemon outcome is the operator's, so the help says it rather than
// leaving them to meet the code once and guess.
func TestStopsHelpStatesTheNoDaemonOutcome(t *testing.T) {
	v, ok := verbs.ByCLI([]string{"stop"})
	if !ok {
		t.Fatal("no stop subcommand")
	}
	if !strings.Contains(v.Long, string(codes.NotRunning)) {
		t.Errorf("stop's help does not name %s: %q", codes.NotRunning, v.Long)
	}
	if !strings.Contains(Usage(), "hdis stop") {
		t.Errorf("usage does not list stop:\n%s", Usage())
	}
}

// Stop prints what it did, in a line an operator reads.
func TestStopPrintsWhatItStopped(t *testing.T) {
	var out bytes.Buffer
	if err := Write("stop", json.RawMessage(`{"stopping":true,"socket":"/tmp/hdis.sock","pid":41}`), false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/tmp/hdis.sock") {
		t.Errorf("stop printed %q", out.String())
	}
}
