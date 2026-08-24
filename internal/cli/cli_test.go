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

// Go's flag package stops at the first non-flag word, so a flag written after
// the positional landed in the positionals and the call was refused for an
// argument the verb does not take. `hdis dispatch 41 --json` is what a caller
// writes, and it means the same thing as `hdis --json dispatch 41`.
func TestTheFlagIsReadAfterThePositionalToo(t *testing.T) {
	req, asJSON, err := Request(verb(t, "dispatch"), []string{"7", "--json"})
	if err != nil {
		t.Fatalf("dispatch 7 --json: %v", err)
	}
	if !asJSON || req.Args["task"] != "7" {
		t.Fatalf("json=%t request=%+v", asJSON, req)
	}
	if !WantsJSON([]string{"7", "--json"}) || WantsJSON([]string{"7"}) {
		t.Error("WantsJSON does not read the flag where the caller wrote it")
	}
	if WantsJSON([]string{"--json=false", "7"}) {
		t.Error("an explicit false still asked for a document")
	}
}

// §6.2: with --json a failure is one envelope on stdout, carrying the
// contract code and the message. It is the same document the MCP door builds.
func TestAFailureWithJSONIsTheContractEnvelope(t *testing.T) {
	var out strings.Builder
	err := WriteError(codes.Refusef(codes.NoBasePane, "no pane to split off"), &out)
	if err != nil {
		t.Fatalf("WriteError: %v", err)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &body); err != nil {
		t.Fatalf("the envelope is not one JSON document: %q (%v)", out.String(), err)
	}
	if body.Error.Code != string(codes.Unsupported) {
		t.Errorf("code = %q, want %q", body.Error.Code, codes.Unsupported)
	}
	if body.Error.Message != "NO_BASE_PANE: no pane to split off" {
		t.Errorf("message = %q", body.Error.Message)
	}
}

func TestAMissingRequiredPositionalIsRefused(t *testing.T) {
	_, _, err := Request(verb(t, "dispatch"), nil)
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
		t.Fatalf("dispatch with nothing = %v (%q), want %q", err, got, want)
	}
}

func TestAnArgumentTheVerbDoesNotTakeIsRefused(t *testing.T) {
	_, _, err := Request(verb(t, "status"), []string{"7"})
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
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
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"","max_workers":2,"interval":"15s","board":{"reachable":true}}`)
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
	if err := Write("stop", json.RawMessage(`{"stopping":true,"socket":"/tmp/dispatch.sock","pid":41}`), false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "/tmp/dispatch.sock") {
		t.Errorf("stop printed %q", out.String())
	}
}

// CRITERION 6. README says doctor reports the verification lane, and what the
// lane IS is now a self-review shot in the worker's own pane. The prose report
// is what an operator reads, so it has to say that there and not only in the
// JSON — an operator told "on" alone would still be looking for a second pane.
func TestDoctorSaysTheLaneIsASelfReviewShotInTheWorkersOwnPane(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"wM:p1","max_workers":2,"interval":"15s","verify":{"enabled":true},"board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, want := range []string{"verify", "on", "self-review", "own pane"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("want %q in doctor prose %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "profile") {
		t.Errorf("the lane still names a profile it no longer launches: %q", out.String())
	}
}

// A lane that is off is said, rather than left to be inferred from a line
// that is not printed.
func TestDoctorSaysTheVerificationLaneIsOffWhenItIsOff(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"wM:p1","max_workers":2,"interval":"15s","verify":{"enabled":false},"board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "verify") || !strings.Contains(out.String(), "off") {
		t.Fatalf("doctor printed %q", out.String())
	}
}

// An unreachable board returns early from the report. The lane is this
// dispatcher's own config and is known either way, so it is printed before
// the board line rather than lost behind it.
func TestDoctorNamesTheLaneEvenWhenTheBoardIsDown(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"wM:p1","max_workers":2,"interval":"15s","verify":{"enabled":true},"board":{"reachable":false,"error":"dial: no socket"}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "self-review") {
		t.Fatalf("doctor printed %q", out.String())
	}
}

// The work is no longer in the project directory, so the line an operator
// reads has to name the branch it is on.
func TestStatusNamesTheBranchTheWorkIsOn(t *testing.T) {
	raw, err := json.Marshal(loop.Status{Workers: []loop.Worker{{
		Seq: 7, Pane: "wM:p9", AgentStatus: "working", PaneAlive: true,
		Branch: "hdis/task-7", Title: "do the thing",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Write("status", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "hdis/task-7") {
		t.Fatalf("status does not name the branch: %q", out.String())
	}
}

// The number the operator just set is read back from the running daemon, in
// the prose an operator actually reads and not only in the JSON.
func TestDoctorNamesTheMaxPanesPerTabInProse(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"wM:p1","max_workers":4,"interval":"15s","min_pane_columns":40,"max_panes_per_tab":2,"board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, want := range []string{"max_panes_per_tab", "2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("want %q in doctor prose %q", want, out.String())
		}
	}
}

// The log the running daemon opened is read back off it in prose, the same
// way the bindings and the layout are: an operator looking for a spawn
// decision is told the file rather than guessing at the shell line.
func TestDoctorNamesTheLogInProse(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","base_pane":"wM:p1","log":"/s/dispatch.log","board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "/s/dispatch.log") {
		t.Fatalf("doctor does not name the log: %q", out.String())
	}
}

// A daemon that could not open a file says so where the path would be,
// rather than showing an empty field an operator reads as "no log".
func TestDoctorSaysWhenNoLogFileCouldBeOpened(t *testing.T) {
	raw := json.RawMessage(`{"version":"0.1.0","socket":"/s/dispatch.sock","board":{"reachable":true}}`)
	var out strings.Builder
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out.String(), "stdout only") {
		t.Fatalf("doctor does not say the log went nowhere: %q", out.String())
	}
}

// A named argument the verb declares is a flag on this door, so `hdis events
// --since <id> --limit 5` reaches the daemon as the arguments the registry
// declares rather than as words the parser refuses.
func TestANamedArgumentIsAFlagOnThisDoor(t *testing.T) {
	v, ok := verbs.ByCLI([]string{"events"})
	if !ok {
		t.Fatal("no events subcommand")
	}
	req, _, err := Request(v, []string{"--since", "ev-1", "--limit", "5"})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if req.Args["since"] != "ev-1" {
		t.Fatalf("since came through as %v", req.Args["since"])
	}
	if req.Args["limit"] != 5 {
		t.Fatalf("limit came through as %v (%T)", req.Args["limit"], req.Args["limit"])
	}
}

// --follow is a property of the connection and not an argument of the verb:
// it is on the request the daemon reads and never in the args it checks
// against the registry, which would refuse it.
func TestFollowIsOnTheRequestAndNotAnArgument(t *testing.T) {
	v, _ := verbs.ByCLI([]string{"events"})
	req, _, err := Request(v, []string{"--follow"})
	if err != nil {
		t.Fatalf("events --follow: %v", err)
	}
	if !req.Follow {
		t.Fatal("--follow did not reach the request")
	}
	if _, named := req.Args["follow"]; named {
		t.Fatalf("--follow was sent as an argument: %v", req.Args)
	}
	for _, a := range v.Args {
		if a.Name == "follow" {
			t.Fatal("follow is declared as a verb argument, so the MCP door publishes a stream it cannot serve")
		}
	}
}

// An event reads as one line an operator can scan, and as its own document
// for a machine caller.
func TestAnEventRendersAsALineAndAsItsOwnDocument(t *testing.T) {
	raw := json.RawMessage(`{"id":"ev-1","name":"dispatch.worker.spawned","entity":"worker","entity_id":"01AAA","at":1756000000000,"actor":"plugin:hdis","kind":"spawned","detail":{"pane":"wM:p9"}}`)
	var out bytes.Buffer
	if err := WriteEvent(raw, false, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	line := out.String()
	for _, want := range []string{"dispatch.worker.spawned", "01AAA", "pane=wM:p9"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line %q does not carry %q", line, want)
		}
	}
	out.Reset()
	if err := WriteEvent(raw, true, &out); err != nil {
		t.Fatalf("write: %v", err)
	}
	if strings.TrimSpace(out.String()) != string(raw) {
		t.Fatalf("--json rewrote the daemon's document as %s", out.String())
	}
}
