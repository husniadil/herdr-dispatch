package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// gateScript writes a gate command that answers with body, and points the
// daemon's config at it. The gate is a command, so a test that stubbed the
// decision instead would leave §9.2's whole failure surface untested.
func gateScript(t *testing.T, d *Daemon, body string) string {
	t.Helper()
	seen := filepath.Join(t.TempDir(), "seen.json")
	path := filepath.Join(t.TempDir(), "gate")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat > "+seen+"\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	d.Loop.Config.GateCommand = []string{path}
	return seen
}

// §9.2: unconfigured allows. Every other case here configures a gate, so this
// is what says the gate is off by default rather than merely quiet.
func TestAnUnconfiguredGateLetsEveryVerbThrough(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	if d.Policy().Configured() {
		t.Fatal("a daemon whose config names no gate reports one configured")
	}
	if _, err := call(t, d, protocol.Request{Verb: "status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
}

// §9.1 and §9.2: the world-changing verb passes the gate, the gate reads
// {subject, verb, target} on stdin, and a deny comes back as DENIED with the
// gate's own reason.
func TestAGateThatDeniesRefusesTheDispatchWithItsReason(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	seen := gateScript(t, d, `echo '{"decision":"deny","reason":"not during a freeze"}'`)

	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Pane: "wM:p3"})
	if got := codes.Of(err); got != codes.Denied {
		t.Fatalf("dispatch under a denying gate = %v (%s), want DENIED", err, got)
	}
	if !strings.Contains(err.Error(), "not during a freeze") {
		t.Errorf("the refusal drops the gate's reason: %v", err)
	}
	body, rerr := os.ReadFile(seen)
	if rerr != nil {
		t.Fatalf("the gate was never run: %v", rerr)
	}
	for _, want := range []string{`"subject":"agent:wM:p3"`, `"verb":"dispatch.dispatch"`, `"target":"7"`} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(string(body), want) {
				t.Errorf("the gate saw %s, missing %s", body, want)
			}
		})
	}
}

// §9.2's whole point, at the call site rather than in the gate package: a
// gate that cannot answer denies, and the verb does not run.
func TestAGateThatCannotAnswerDeniesTheDispatch(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	d.Loop.Config.GateCommand = []string{filepath.Join(t.TempDir(), "no-such-gate")}

	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	if got := codes.Of(err); got != codes.Denied {
		t.Fatalf("dispatch under an unreachable gate = %v (%s), want DENIED: §9.2 fails closed", err, got)
	}
	if len(d.Loop.Pending()) != 0 {
		t.Errorf("a denied dispatch reserved the task anyway: %v", d.Loop.Pending())
	}
}

// §9.1 names stop as world-changing in substance — it is the brake on every
// worker — so it passes the gate like dispatch, and a gate that denies it
// leaves the daemon serving.
func TestAGateThatDeniesStopLeavesTheDaemonServing(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	d.halt = make(chan struct{})
	gateScript(t, d, `echo '{"decision":"deny","reason":"the fleet is mid-release"}'`)

	if _, err := call(t, d, protocol.Request{Verb: "stop"}); codes.Of(err) != codes.Denied {
		t.Fatalf("stop under a denying gate = %v, want DENIED", err)
	}
	select {
	case <-d.halt:
		t.Fatal("a denied stop halted the daemon anyway")
	default:
	}
}

// §9.3: defer parks it. The call is recorded rather than performed, the
// caller is refused with DENIED carrying parked_id, and the row names the
// subject the gate stopped.
func TestADeferredDispatchIsParkedAndTheDeniedNamesTheRow(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)

	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Pane: "wM:p3"})
	if got := codes.Of(err); got != codes.Denied {
		t.Fatalf("a deferred dispatch = %v (%s), want DENIED", err, got)
	}
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatal("the DENIED names no parked_id, so the caller has nothing to point the operator at (§9.3)")
	}
	if len(d.Loop.Pending()) != 0 {
		t.Errorf("a parked dispatch reserved the task anyway: %v", d.Loop.Pending())
	}

	held := d.Loop.Parked()
	if len(held) != 1 {
		t.Fatalf("parked = %+v, want one row", held)
	}
	got := held[0]
	if got.ID != id || got.Verb != "dispatch.dispatch" || got.Target != "7" ||
		got.Subject != "agent:wM:p3" || got.State != store.ParkedWaiting || got.Reason != "ask the operator" {
		t.Errorf("parked row = %+v", got)
	}
	if got.Payload["task"] != "7" {
		t.Errorf("the row does not carry the call's own arguments: %+v", got.Payload)
	}
}

// The row outlives the process, because the operator decides at their own
// pace and a deferral that vanished on restart would be a call the caller was
// answered for and nobody can find.
func TestAParkedActionSurvivesAReload(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	if _, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}}); err == nil {
		t.Fatal("a deferred dispatch succeeded")
	}

	state, err := d.Loop.Store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(state.Parked) != 1 || state.Parked[0].Verb != "dispatch.dispatch" {
		t.Fatalf("the reloaded document holds %+v", state.Parked)
	}
}

// §9.3: resolving re-runs the verb, and it re-runs it WITHOUT the gate. A
// second ask would park the resolution too, and the action could never leave
// the queue however many times an operator decided it.
func TestResolvingRunsTheVerbWithoutAskingTheGateAgain(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}, Pane: "wM:p3"})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}

	raw, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Pane: "wM:p1"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var res ParkedResolution
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		t.Fatal(uerr)
	}
	if res.State != store.ParkedResolved {
		t.Errorf("resolution = %+v", res)
	}
	// The verb really ran: the task the gate stopped is now reserved.
	if got := d.Loop.Pending(); len(got) != 1 {
		t.Fatalf("the resolved dispatch reserved %v, want the one task", got)
	}
	// And the row is gone from what the operator is asked to look at, with
	// the resolver named on it.
	if held := d.Loop.Parked(); len(held) != 0 {
		t.Fatalf("a resolved action is still waiting: %+v", held)
	}
	state, _ := d.Loop.Store.Load()
	if len(state.Parked) != 1 || state.Parked[0].ResolvedBy != "agent:wM:p1" {
		t.Fatalf("the row does not name who resolved it: %+v", state.Parked)
	}
}

// §9.3 re-runs the call the caller made, and the board scope is part of that
// call: a number means different tasks on different boards, so a re-run that
// lost the scope could reserve the same seq on the wrong project's board.
func TestAParkedDispatchRerunsUnderTheBoardItWasCalledOn(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)

	// The board offers task 7 on /src/p; this call names another board, so
	// the scope alone decides whether the re-run finds a task at all.
	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Project: "/src/other"})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	held := d.Loop.Parked()
	if len(held) != 1 || held[0].Project != "/src/other" {
		t.Fatalf("the parked row does not carry the scope the caller named: %+v", held)
	}

	_, err = call(t, d, protocol.Request{Verb: "parked.resolve", Args: map[string]any{"id": id}})
	if err == nil {
		t.Fatal("the re-run reserved task 7 although the call named a board that is not offering it")
	}
	if !strings.Contains(err.Error(), "/src/other") {
		t.Errorf("the re-run was answered off another board: %v", err)
	}
	if got := d.Loop.Pending(); len(got) != 0 {
		t.Fatalf("the re-run reserved %v off a board the call never named", got)
	}
}

// §9.3: a refused action closes without the verb running.
func TestRefusingAParkedActionNeverRunsTheVerb(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	id := codes.ParkedOf(err)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id, "reject": true}}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got := d.Loop.Pending(); len(got) != 0 {
		t.Fatalf("a refused dispatch reserved %v", got)
	}
	state, _ := d.Loop.Store.Load()
	if state.Parked[0].State != store.ParkedRefused {
		t.Fatalf("row = %+v", state.Parked[0])
	}
}

// One winner. The move out of `parked` happens before the verb runs, so two
// resolves cannot both run it — the loser meets a row that has already been
// decided rather than a side effect that has already happened twice.
func TestAParkedActionResolvesOnlyOnce(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}})
	id := codes.ParkedOf(err)

	if _, err := call(t, d, protocol.Request{Verb: "parked.resolve", Args: map[string]any{"id": id}}); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	_, err = call(t, d, protocol.Request{Verb: "parked.resolve", Args: map[string]any{"id": id}})
	if got := codes.Of(err); got != codes.Conflict {
		t.Fatalf("the second resolve = %v (%s), want CONFLICT", err, got)
	}
}

func TestResolvingAnUnknownRowIsNotFound(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	_, err := call(t, d, protocol.Request{Verb: "parked.resolve", Args: map[string]any{"id": "pk-nope"}})
	if got := codes.Of(err); got != codes.NotFound {
		t.Fatalf("resolve of an unknown row = %v (%s), want NOT_FOUND", err, got)
	}
}

// §9.3: an action the operator let through whose verb then errored is not a
// resolved one. The row says so, and stays in what the operator is shown.
func TestAResolvedActionWhoseVerbFailedStaysInFrontOfTheOperator(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "nosuch"}})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	// The board has task 7 and nothing called "nosuch", so the re-run fails
	// on the board's own answer rather than on anything the gate did.
	if _, err := call(t, d, protocol.Request{Verb: "parked.resolve", Args: map[string]any{"id": id}}); err == nil {
		t.Fatal("a dispatch of a task the board does not have succeeded")
	}
	held := d.Loop.Parked()
	if len(held) != 1 || held[0].State != store.ParkedFailed || held[0].Error == "" {
		t.Fatalf("the failed row is %+v; an operator who decided and got nothing must see why", held)
	}
}

// parkOne defers a dispatch and hands back the parked row's id, which is all
// a test about the RESOLUTION needs of the deferral before it.
func parkOne(t *testing.T, d *Daemon) string {
	t.Helper()
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}, Pane: "wM:p3"})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	return id
}

// resolvedEvent is the one `dispatch.parked.resolved` on the trail, and
// deferredEvent the `dispatch.parked.deferred` before it.
func resolvedEvent(t *testing.T, d *Daemon) store.Event {
	t.Helper()
	return parkedEvent(t, d, loop.KindResolved)
}

func deferredEvent(t *testing.T, d *Daemon) store.Event {
	t.Helper()
	return parkedEvent(t, d, loop.KindDeferred)
}

func parkedEvent(t *testing.T, d *Daemon, kind string) store.Event {
	t.Helper()
	trail, err := d.Loop.Events(store.EventFilter{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, ev := range trail {
		if ev.Kind == kind {
			return ev
		}
	}
	t.Fatalf("no %s event on the trail of %d events", kind, len(trail))
	return store.Event{}
}

// §3.7: resolving a deferral is the operator's authority, and an operator verb
// performed by a principal other than the operator records the CALLING
// principal as the event's actor. Every other event here is this daemon
// acting; this one is not, because a trail that filed the decision under the
// daemon would name nobody who made it.
func TestTheResolvedEventIsFiledUnderTheCallerAndNotTheDaemon(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOne(t, d)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Pane: "wM:p1"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ev := resolvedEvent(t, d)
	if ev.Actor != "agent:wM:p1" {
		t.Fatalf("the resolution is filed under %q, want the caller agent:wM:p1", ev.Actor)
	}
	// The deferral that preceded it is this daemon acting and carries this
	// daemon's own principal, so a resolution wearing the same one is the
	// mutation this test exists to catch.
	if ev.Actor == deferredEvent(t, d).Actor {
		t.Fatalf("the resolution is filed under this daemon (%q)", ev.Actor)
	}
	if ev.Detail["resolved_by"] != "agent:wM:p1" {
		t.Errorf("resolved_by moved: %+v", ev.Detail)
	}
}

// §3.7's second half: the event is MARKED as an operator verb performed by
// someone other than the operator, under the key the sibling plugins write,
// because an operator reading three trails matches one word.
func TestAnAgentResolvingForTheOperatorMarksTheEvent(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOne(t, d)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Pane: "wM:p1"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ev := resolvedEvent(t, d); ev.Detail[loop.OnBehalfOfOperator] != true {
		t.Fatalf("detail = %+v, want %s: an agent performing the operator's verb labels itself",
			ev.Detail, loop.OnBehalfOfOperator)
	}
}

// And the mark means what it says: the operator resolving in person carries
// none. A mark on every resolution would say nothing about any of them.
func TestTheOperatorResolvingCarriesNoMark(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOne(t, d)

	// A paneless door started with §7.5's declaration is the operator, which
	// is the one shape of this call that is not an agent acting for them.
	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Operator: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ev := resolvedEvent(t, d)
	if ev.Actor != "human" {
		t.Fatalf("the operator's own resolution is filed under %q", ev.Actor)
	}
	if _, marked := ev.Detail[loop.OnBehalfOfOperator]; marked {
		t.Fatalf("detail = %+v, want no %s on the operator's own call", ev.Detail, loop.OnBehalfOfOperator)
	}
}

// parkOneThatFails defers a dispatch of a task the board does not have, so the
// re-run a resolution triggers fails on the board's own answer rather than on
// anything the gate did. It hands back the parked row's id.
func parkOneThatFails(t *testing.T, d *Daemon) string {
	t.Helper()
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	_, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "nosuch"}})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	return id
}

// failedEvent is the one `dispatch.parked.failed` on the trail.
func failedEvent(t *testing.T, d *Daemon) store.Event {
	t.Helper()
	return parkedEvent(t, d, loop.KindFailed)
}

// §3.7: the failure of an operator verb is that verb's own outcome, so it is
// filed under the principal that decided it, exactly as the resolution before
// it is. A trail that filed the two halves of one decision under two different
// actors would name this daemon for the half that went wrong.
func TestTheFailedEventIsFiledUnderTheCallerAndNotTheDaemon(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOneThatFails(t, d)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Pane: "wM:p1"}); err == nil {
		t.Fatal("a dispatch of a task the board does not have succeeded")
	}
	ev := failedEvent(t, d)
	if ev.Actor != "agent:wM:p1" {
		t.Fatalf("the failure is filed under %q, want the caller agent:wM:p1", ev.Actor)
	}
	if ev.Actor == deferredEvent(t, d).Actor {
		t.Fatalf("the failure is filed under this daemon (%q)", ev.Actor)
	}
	if ev.Detail["resolved_by"] != "agent:wM:p1" {
		t.Errorf("resolved_by moved: %+v", ev.Detail)
	}
}

// §3.7's second half on the same event: an agent performing the operator's
// verb labels itself whether the verb then ran or errored.
func TestAnAgentResolvingForTheOperatorMarksTheFailedEvent(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOneThatFails(t, d)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Pane: "wM:p1"}); err == nil {
		t.Fatal("a dispatch of a task the board does not have succeeded")
	}
	if ev := failedEvent(t, d); ev.Detail[loop.OnBehalfOfOperator] != true {
		t.Fatalf("detail = %+v, want %s: an agent performing the operator's verb labels itself",
			ev.Detail, loop.OnBehalfOfOperator)
	}
}

// And the mark means the same thing here: the operator resolving in person
// carries none, however the verb they let through turned out.
func TestTheOperatorResolvingCarriesNoMarkOnTheFailedEvent(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	id := parkOneThatFails(t, d)

	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id}, Operator: true}); err == nil {
		t.Fatal("a dispatch of a task the board does not have succeeded")
	}
	ev := failedEvent(t, d)
	if ev.Actor != "human" {
		t.Fatalf("the operator's own failure is filed under %q", ev.Actor)
	}
	if _, marked := ev.Detail[loop.OnBehalfOfOperator]; marked {
		t.Fatalf("detail = %+v, want no %s on the operator's own call", ev.Detail, loop.OnBehalfOfOperator)
	}
}

// §10.3 with §9.2: an unconfigured gate allows, which at the call site is
// indistinguishable from a configured one that allows. doctor is the only
// place an operator can tell them apart, so it says which.
func TestDoctorSaysWhetherAGateIsConfigured(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	rep, err := d.doctor(context.Background(), protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Gate.Configured {
		t.Error("doctor reports a gate configured on a daemon whose config names none")
	}
	if want := verbs.GatedVerbs(); len(rep.Gate.Verbs) != len(want) {
		t.Errorf("doctor names %v as the gated verbs, and the registry says %v", rep.Gate.Verbs, want)
	}

	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	if _, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}}); err == nil {
		t.Fatal("a deferred dispatch succeeded")
	}
	rep, _ = d.doctor(context.Background(), protocol.Request{Verb: "doctor"})
	if !rep.Gate.Configured || len(rep.Gate.Command) == 0 {
		t.Errorf("doctor does not name the configured gate: %+v", rep.Gate)
	}
	if rep.Gate.Parked != 1 {
		t.Errorf("doctor reports %d parked and one is waiting", rep.Gate.Parked)
	}
}

// §10.3 with §11.2: doctor prints the Herdr schema it saw. It is the one
// surface that can say "this Herdr does not offer tab.create" BEFORE a
// dispatch refuses, which is the whole reason a plugin feature-detects at all
// rather than finding out at the call.
func TestDoctorReportsTheHerdrSchemaItSaw(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	rep, err := d.doctor(context.Background(), protocol.Request{Verb: "doctor"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Herdr.Detected {
		t.Fatalf("doctor read no herdr schema: %+v", rep.Herdr)
	}
	if len(rep.Herdr.Missing) != 0 {
		t.Errorf("a herdr offering everything reports %v missing", rep.Herdr.Missing)
	}
	if rep.Herdr.Requests == 0 {
		t.Error("doctor counted no requests")
	}

	// A Herdr missing one is named, by capability. It gets a daemon of its
	// own because the schema is read once and cached, so the one above keeps
	// the answer it already has.

	lean, leanFake := newDaemon(t)
	leanFake.Write(t, testenv.HerdrSchemaFile, `{"protocol":1,"requests":["pane.list"]}`)
	rep, _ = lean.doctor(context.Background(), protocol.Request{Verb: "doctor"})
	if !contains(rep.Herdr.Missing, herdrclient.CapTabCreate) {
		t.Errorf("doctor does not name the missing capability: %+v", rep.Herdr)
	}
}

func contains(all []string, s string) bool {
	for _, v := range all {
		if v == s {
			return true
		}
	}
	return false
}

// §5.8: `<name> dump --json` prints the whole store, because a plugin whose
// data cannot be read without the plugin is not acceptable. Here the store is
// three things — the bindings, the reservations, and the parked actions — and
// a dump that printed two of them would be exactly the half-answer the
// section forbids.
func TestDumpPrintsTheWholeStore(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)
	if _, err := call(t, d, protocol.Request{Verb: "dispatch", Args: map[string]any{"task": "7"}}); err == nil {
		t.Fatal("a deferred dispatch succeeded")
	}
	// A reservation, made by letting the parked one through.
	held := d.Loop.Parked()
	if len(held) != 1 {
		t.Fatalf("parked = %+v", held)
	}
	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": held[0].ID}}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	raw, err := call(t, d, protocol.Request{Verb: "dump"})
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	var got DumpReport
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatal(uerr)
	}
	if got.Version != store.Version {
		t.Errorf("dump names version %d, and the document is %d", got.Version, store.Version)
	}
	if got.Path != d.Loop.BindingsPath() {
		t.Errorf("dump names %q as the file and the store is at %q", got.Path, d.Loop.BindingsPath())
	}
	if len(got.Reservations) != 1 || got.Reservations[0].TaskID == "" {
		t.Errorf("dump does not carry the reservations: %+v", got.Reservations)
	}
	// The decided row too: a dump that showed only what is still waiting
	// would be parked_list under another name.
	if len(got.Parked) != 1 || got.Parked[0].State != store.ParkedResolved {
		t.Errorf("dump does not carry the decided parked actions: %+v", got.Parked)
	}
	// Empty rather than null: a reader has to be able to tell "none" from
	// "this daemon could not say".
	if got.Bindings == nil {
		t.Error("dump prints the bindings as null, which reads as unknown rather than none")
	}
	if !strings.Contains(string(raw), `"bindings":[]`) {
		t.Errorf("dump does not carry an empty bindings list: %s", raw)
	}
}

// §7.5 where it actually reaches this plugin: the declaration is not the
// trail actor — the board principal this daemon writes with is fixed for the
// process (loop.principal) and never a caller's — but it IS the §9 gate's
// subject and the resolver a parked row records. A door registered in a
// desktop MCP client stands in no pane, so without the declaration both of
// those read `unknown`; with it they read the operator who wrote it.
func TestADeclaredPanelessDoorIsTheOperatorAtTheGateAndOnTheParkedRow(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	seen := gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)

	// No pane, Door "mcp", Operator true: the request the declared door
	// builds, exactly as internal/mcpdoor assembles it.
	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"},
		Door: "mcp", Operator: true})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	body, rerr := os.ReadFile(seen)
	if rerr != nil {
		t.Fatalf("the gate was never run: %v", rerr)
	}
	if !strings.Contains(string(body), `"subject":"human"`) {
		t.Errorf("the gate saw %s, and a declared paneless door is the operator (§7.5)", body)
	}
	held := d.Loop.Parked()
	if len(held) != 1 || held[0].Subject != "human" {
		t.Fatalf("the parked row does not name the operator as the subject: %+v", held)
	}

	// And the resolver, which is the other place Request.Caller is read:
	// ClaimParked records WHO decided, and the operator resolving through
	// their own declared door is not `none`.
	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id, "reject": true},
		Door: "mcp", Operator: true}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	state, _ := d.Loop.Store.Load()
	if len(state.Parked) != 1 || state.Parked[0].ResolvedBy != "human" {
		t.Fatalf("the row does not name the operator as who resolved it: %+v", state.Parked)
	}
}

// The half that says the declaration is what changed the answer: the same
// paneless call with no declaration is nobody in particular, at both places.
func TestAnUndeclaredPanelessDoorIsNobodyAtTheGateAndOnTheParkedRow(t *testing.T) {
	stateDir(t)
	d, _ := newDaemon(t)
	seen := gateScript(t, d, `echo '{"decision":"defer","reason":"ask the operator"}'`)

	_, err := call(t, d, protocol.Request{
		Verb: "dispatch", Args: map[string]any{"task": "7"}, Door: "mcp"})
	id := codes.ParkedOf(err)
	if id == "" {
		t.Fatalf("no parked row: %v", err)
	}
	body, rerr := os.ReadFile(seen)
	if rerr != nil {
		t.Fatalf("the gate was never run: %v", rerr)
	}
	if !strings.Contains(string(body), `"subject":"none"`) {
		t.Errorf("the gate saw %s, and a door nobody declared is nobody (§3.7)", body)
	}
	if held := d.Loop.Parked(); len(held) != 1 || held[0].Subject != "none" {
		t.Fatalf("the parked row = %+v", held)
	}
	if _, err := call(t, d, protocol.Request{
		Verb: "parked.resolve", Args: map[string]any{"id": id, "reject": true}, Door: "mcp"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	state, _ := d.Loop.Store.Load()
	if len(state.Parked) != 1 || state.Parked[0].ResolvedBy != "none" {
		t.Fatalf("the row = %+v", state.Parked)
	}
}
