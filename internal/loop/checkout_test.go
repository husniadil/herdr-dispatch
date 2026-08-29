package loop

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// herdrCheckout is herdrTwoPanes with `agent list` answered from a file,
// because Adopt asks it and these cases run a restart against a REAL git
// project rather than the fake git the other restart cases use.
const herdrCheckout = `case "$1 $2" in
"tab create")
  n=$(cat "$HDIS_FAKE_DIR/splitn" 2>/dev/null || echo 8)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/splitn"
  printf '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"wM:t%s","workspace_id":"wM","label":"hdis-7"},"root_pane":{"pane_id":"wM:p%s","workspace_id":"wM","tab_id":"wM:t%s","terminal_id":"x","focused":false,"agent_status":"unknown","revision":0}}}' "$n" "$n" "$n" ;;
"tab list") cat "$HDIS_FAKE_DIR/tabs.json" ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent list") cat "$HDIS_FAKE_DIR/agents.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// paneAt is a pane list with one worker pane in it, working in a directory —
// which is the half of the reap's predicate that kept a retired pane's
// checkout alive.
func paneAt(pane, status, cwd string) string {
	return `{"id":"x","result":{"type":"pane_list","panes":[` +
		`{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"},` +
		`{"pane_id":"` + pane + `","agent":"claude","agent_status":"` + status + `","cwd":"` + cwd +
		`","tab_id":"wM:t9","workspace_id":"wM","focused":false,"revision":1}]}}`
}

// newCheckoutLoop is a loop over a real git project, a real worktree manager
// and a herdr that answers `agent list`.
func newCheckoutLoop(t *testing.T) (*Loop, *testenv.Fake, string) {
	t.Helper()
	l, f, project := newVerifyLoop(t, false)
	f.Bin(t, "herdr", herdrCheckout)
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[]}}`)
	return l, f, project
}

// A restart that retires a restored pane takes the pane's checkout with it.
//
// Measured on box-a on 2026-08-29, hdis 0.10.0, task #10: the daemon came
// back, retired the restored idle pane and the board released the claim. The
// checkout stayed, because Adopt's reap had read which panes were alive
// BEFORE the retire and the pane was still sitting in that directory. Nothing
// reaps again after a retire, so the directory kept `hdis/task-10` checked
// out and every respawn for that task was refused by git — three times a
// minute, for as long as the daemon ran.
func TestARetiredRestoredPanesCheckoutGoesWithIt(t *testing.T) {
	l, f, project := newCheckoutLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	trees := worktreesOf(t, project)
	if len(trees) != 1 {
		t.Fatalf("the worker holds %d checkouts: %v", len(trees), trees)
	}
	tree := trees[0]

	// The box came back. Herdr restored the pane in its own checkout, and
	// the client in it is not working: idle, on a row the board still has in
	// `doing`.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", row(project, "doing", "agent:wM:p9"))
	f.Write(t, "panes.json", paneAt("wM:p9", "idle", tree))
	agentGetIs(t, f, "idle")

	next := restarted(t, l)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-adopted %d bindings; a pane with no worker in it is not a worker", n)
	}
	if got := worktreesOf(t, project); len(got) != 0 {
		t.Fatalf("the retired pane's checkout still holds %s: %v", worktree.Branch(7), got)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the retired pane's checkout is still there: %v", err)
	}
	ev := one(t, next, EventName(store.EntityWorker, KindRetired))
	if got := ev.Detail["worktree_removed"]; got != tree {
		t.Fatalf("the retire says the checkout %v was removed, want %s: %+v", got, tree, ev.Detail)
	}
}

// A pane that is gone takes its checkout with it too, and the event already
// filed for it says so: a checkout removed silently is indistinguishable from
// one left behind.
func TestAGonePanesCheckoutGoesWithItsBinding(t *testing.T) {
	l, f, project := newCheckoutLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	trees := worktreesOf(t, project)
	if len(trees) != 1 {
		t.Fatalf("the worker holds %d checkouts: %v", len(trees), trees)
	}
	tree := trees[0]

	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", row(project, "doing", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList())

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := worktreesOf(t, project); len(got) != 0 {
		t.Fatalf("the gone pane's checkout still holds %s: %v", worktree.Branch(7), got)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindGone))
	if got := ev.Detail["worktree_removed"]; got != tree {
		t.Fatalf("the gone pane says the checkout %v was removed, want %s: %+v", got, tree, ev.Detail)
	}
}

// A checkout a previous run left behind still holds the task's branch, and
// git refuses a second `worktree add` on it. Nothing names it and nothing is
// working in it, so it is exactly what the reap removes — and the spawn
// removes it rather than failing into it forever.
func TestASpawnClearsAnOrphanCheckoutHoldingItsBranch(t *testing.T) {
	l, f, project := newCheckoutLoop(t)
	m := &worktree.Manager{Root: l.Worktrees.RootDir()}
	orphan, _, err := m.Worker(context.Background(), project, 7)
	if err != nil {
		t.Fatalf("orphan checkout: %v", err)
	}

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("an orphan checkout cost the task its worker: %d tab create(s): %v", len(got), got)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("the orphan checkout holding %s was not removed: %v", worktree.Branch(7), err)
	}
	b := l.Bindings()
	if len(b) != 1 || b[0].Worktree == "" || b[0].Worktree == orphan {
		t.Fatalf("the worker was not given a checkout of its own: %+v", b)
	}
	if _, err := os.Stat(b[0].Worktree); err != nil {
		t.Fatalf("the worker's own checkout is not there: %v", err)
	}
}

// And a checkout something is still using is a refusal that NAMES it. git's
// own `already used by worktree at ...` says which directory and nothing about
// whose it is, which is the whole question an operator has to answer.
func TestASpawnIsRefusedWhenSomethingHoldsTheBranchesCheckout(t *testing.T) {
	t.Run("a binding names it", func(t *testing.T) {
		l, _, _ := newCheckoutLoop(t)
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		b := l.Bindings()
		if len(b) != 1 {
			t.Fatalf("bindings: %+v", b)
		}
		tree := b[0].Worktree

		err := l.spawn(context.Background(), decide.Action{Kind: decide.Spawn, TaskID: "01AAA"})
		refusalNames(t, err, tree, "wM:p9")
		if _, statErr := os.Stat(tree); statErr != nil {
			t.Fatalf("the refused spawn removed a bound checkout: %v", statErr)
		}
	})

	t.Run("a live pane is working in it", func(t *testing.T) {
		l, f, project := newCheckoutLoop(t)
		m := &worktree.Manager{Root: l.Worktrees.RootDir()}
		tree, _, err := m.Worker(context.Background(), project, 7)
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		// The binding was lost, which is the case the reap's second half
		// exists for: a pane is alive in there all the same.
		f.Write(t, "panes.json", paneAt("wM:p9", "working", tree))
		// One snapshot, which is what puts the board's row where a spawn
		// reads it. Nothing is dispatched off it here.
		if _, err := l.snapshot(context.Background()); err != nil {
			t.Fatalf("snapshot: %v", err)
		}

		err = l.spawn(context.Background(), decide.Action{Kind: decide.Spawn, TaskID: "01AAA"})
		refusalNames(t, err, tree, "wM:p9")
		if _, statErr := os.Stat(tree); statErr != nil {
			t.Fatalf("the refused spawn removed an inhabited checkout: %v", statErr)
		}
	})
}

// refusalNames fails unless the refusal carries the checkout and the pane,
// and is this daemon's own sentence rather than git's.
func refusalNames(t *testing.T, err error, tree, pane string) {
	t.Helper()
	if err == nil {
		t.Fatal("a spawn onto a held branch was not refused")
	}
	for _, want := range []string{tree, pane} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "already used by worktree") {
		t.Fatalf("the refusal is git's own error text: %v", err)
	}
}
