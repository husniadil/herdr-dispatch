package loop

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// sweeps is every `htask sweep` the fake board was asked for.
func sweeps(t *testing.T, f *testenv.Fake) []string {
	t.Helper()
	var out []string
	for _, argv := range f.Argv(t) {
		if len(argv) >= 1 && argv[0] == "sweep" {
			out = append(out, strings.Join(argv, " "))
		}
	}
	return out
}

// gone takes the worker's pane out of Herdr's pane list, which is what makes
// the binding a gone one and the sweep legal at all.
func gone(t *testing.T, f *testenv.Fake) {
	t.Helper()
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "panes.json", paneList())
}

// §11.7: the pane died with the machine it ran on and cannot sweep itself, so
// the daemon that put the worker there hands the claim back — for that pane
// alone, as its plugin principal, with no pane of its own in the call.
func TestAGonePaneHasItsClaimSweptBack(t *testing.T) {
	l, f, _ := newVerifyLoop(t, true)
	var said bytes.Buffer
	l.Log = log.New(&said, "", 0)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "sweep.json", `{"released":["01AAA"],"count":1}`)
	gone(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	asked := sweeps(t, f)
	if len(asked) != 1 {
		t.Fatalf("the board was asked to sweep %d times: %v", len(asked), asked)
	}
	// The principal carries this daemon's base pane in a running daemon; the
	// loop's own client is built without one, and the pane-suffixed form is
	// pinned where the principal is composed — see the htask adapter's
	// TestSweepingAGonePaneAsksTheBoardForThatPaneAlone.
	if want := "sweep --pane wM:p9 --json --as plugin:hdis"; asked[0] != want {
		t.Fatalf("argv: got %q, want %q", asked[0], want)
	}
	// The `--as` is refused from a process carrying a pane, and this daemon
	// may well have been started from a door inside one.
	if got := paneSeenBySweep(t, f); got != "unset" {
		t.Fatalf("the sweep carried HERDR_PANE_ID=%q", got)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindGone))
	if got := releasedIn(ev.Detail); len(got) != 1 || got[0] != "01AAA" {
		t.Fatalf("the trail does not name what came back: %+v", ev.Detail)
	}
	if !strings.Contains(said.String(), "01AAA") {
		t.Errorf("the log does not name the released task:\n%s", said.String())
	}
}

// FORBIDDEN means the pane is alive after all — Herdr's answer is the whole
// authority — so it is recorded and never retried.
func TestASweepTheBoardForbidsIsNotRetried(t *testing.T) {
	l, f, _ := newVerifyLoop(t, true)
	var said bytes.Buffer
	l.Log = log.New(&said, "", 0)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "sweeprefusal", `{"error":{"code":"FORBIDDEN","message":"herdr still lists pane wM:p9"}}`)
	gone(t, f)
	for i := 0; i < 3; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if asked := sweeps(t, f); len(asked) != 1 {
		t.Fatalf("a refused sweep was asked %d times: %v", len(asked), asked)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindGone))
	if got := ev.Detail["sweep_refused"]; got != "FORBIDDEN" {
		t.Fatalf("the trail records the refusal as %v", got)
	}
	if !strings.Contains(said.String(), "herdr still lists") {
		t.Errorf("the log does not say the pane is alive after all:\n%s", said.String())
	}
}

// UNAVAILABLE and TIMEOUT are "ask again": Herdr could not be reached, and
// the claim is still owed. The note is kept and the next tick asks again.
func TestASweepThatCouldNotBeAskedIsRetriedNextTick(t *testing.T) {
	l, f, _ := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "sweeprefusal", `{"error":{"code":"UNAVAILABLE","message":"herdr could not be asked"}}`)
	f.Write(t, "sweepuntil", "1")
	f.Write(t, "sweep.json", `{"released":["01AAA"],"count":1}`)
	gone(t, f)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindGone))
	if ev.Detail["sweep_retry"] != true {
		t.Fatalf("the trail does not say the sweep is still owed: %+v", ev.Detail)
	}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if asked := sweeps(t, f); len(asked) != 2 {
		t.Fatalf("the owed sweep was asked %d times: %v", len(asked), asked)
	}
	// And it is owed once: the second attempt answered, so nothing is left.
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("fourth tick: %v", err)
	}
	if asked := sweeps(t, f); len(asked) != 2 {
		t.Fatalf("a sweep that succeeded was asked again: %v", asked)
	}
}

// UNSUPPORTED is a Herdr too old to list panes, which will not change by
// asking again: the board's own lease is the fallback, and it is said once.
func TestASweepAHerdrCannotSupportFallsBackToTheLease(t *testing.T) {
	l, f, _ := newVerifyLoop(t, true)
	var said bytes.Buffer
	l.Log = log.New(&said, "", 0)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "sweeprefusal", `{"error":{"code":"UNSUPPORTED","message":"this herdr cannot list panes"}}`)
	gone(t, f)
	for i := 0; i < 3; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if asked := sweeps(t, f); len(asked) != 1 {
		t.Fatalf("an unsupported sweep was asked %d times: %v", len(asked), asked)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindGone))
	if got := ev.Detail["sweep_refused"]; got != "UNSUPPORTED" {
		t.Fatalf("the trail records the refusal as %v", got)
	}
	if !strings.Contains(said.String(), "lease") {
		t.Errorf("the log does not name the fallback:\n%s", said.String())
	}
}

// releasedIn is the task ids an event's detail says came back. The trail holds
// them as written until the document is read back, so both shapes are read.
func releasedIn(detail map[string]any) []string {
	switch v := detail["released"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, s.(string))
		}
		return out
	}
	return nil
}

// paneSeenBySweep is the HERDR_PANE_ID the fake board saw, or "unset".
func paneSeenBySweep(t *testing.T, f *testenv.Fake) string {
	t.Helper()
	b, err := os.ReadFile(f.Path("sweep.env"))
	if err != nil {
		t.Fatalf("read the sweep's environment: %v", err)
	}
	return string(b)
}

// The same hand-back on the F3a path: a pane a restart took back with no
// worker in it is retired, and the claim it was holding is swept once the pane
// is gone — the retire is what makes Herdr stop listing it, so the sweep is
// legal only after it.
func TestARestoredPaneWithNoWorkerHasItsClaimSweptAfterTheRetire(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("idle"))
	agentGetIs(t, f, "idle")
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "sweep.json", `{"released":["01AAA"],"count":1}`)

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	asked := sweeps(t, f)
	if len(asked) != 1 {
		t.Fatalf("the retired pane's claim was swept %d times: %v", len(asked), asked)
	}
	if want := "sweep --pane wM:p9 --json --as plugin:hdis"; asked[0] != want {
		t.Fatalf("argv: got %q, want %q", asked[0], want)
	}
	// The order is not incidental: the board asks HERDR whether the pane is
	// gone, so a sweep sent before the pane was closed is refused FORBIDDEN.
	var closedAt, sweptAt = -1, -1
	for i, argv := range f.Argv(t) {
		if len(argv) >= 2 && argv[0] == "tab" && argv[1] == "close" {
			closedAt = i
		}
		if len(argv) >= 1 && argv[0] == "sweep" && sweptAt < 0 {
			sweptAt = i
		}
	}
	if closedAt < 0 || sweptAt < 0 || sweptAt < closedAt {
		t.Fatalf("the pane was closed at call %d and swept at %d; the sweep must follow the retire", closedAt, sweptAt)
	}
	var named bool
	for _, ev := range next.Dump().Events {
		if ev.Kind != KindRetired {
			continue
		}
		if got := releasedIn(ev.Detail); len(got) == 1 && got[0] == "01AAA" {
			named = true
		}
	}
	if !named {
		t.Fatalf("the trail does not name what the retire handed back: %+v", next.Dump().Events)
	}
}
