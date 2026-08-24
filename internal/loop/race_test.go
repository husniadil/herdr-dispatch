package loop

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// inCheckout puts one live worker pane in a checkout under this daemon's own
// worktree root, which is the state a restart finds after a spawn whose
// binding never landed.
func inCheckout(t *testing.T, f *testenv.Fake, root string) string {
	t.Helper()
	dir := filepath.Join(root, worktree.WorkPrefix+"7-abc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"`+dir+`"}`)
	return dir
}

// A restart that adopts a live worker has to recover WHERE that worker is
// working, not only which task it is on. A binding with no checkout and no
// branch is a worker the reap cannot see, and the reap removes what no
// binding names — out from under a worker that is still editing in it.
func TestARestartRecordsTheCheckoutItAdoptsAWorkerIn(t *testing.T) {
	l, f := newLoop(t)
	root := t.TempDir()
	l.Worktrees = &worktree.Manager{Root: root, Git: fakeGit(t, f)}
	dir := inCheckout(t, f, root)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"`+dir+`","title":"do the thing","status":"doing"},"ready":false,"dependents":[]}`)

	l.Log = log.New(io.Discard, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	got := l.Bindings()
	if len(got) != 1 {
		t.Fatalf("the live worker was not adopted: %+v", got)
	}
	if got[0].Worktree != dir {
		t.Fatalf("the adopted binding names no checkout: %q, want %q", got[0].Worktree, dir)
	}
	if got[0].Branch != worktree.Branch(7) {
		t.Fatalf("the adopted binding names no branch: %q, want %q", got[0].Branch, worktree.Branch(7))
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the reap removed the checkout of a worker that is alive in it: %v", err)
	}
}

// The reap's predicate is "no binding names it, so the agent it belonged to
// is gone". Bindings alone cannot carry that: a restart can lose one while
// the worker it named is alive and still sitting in the directory. Herdr's
// word about where a live pane is working is the check that makes the
// predicate true.
func TestTheReapLeavesACheckoutALivePaneIsWorkingIn(t *testing.T) {
	l, f := newLoop(t)
	root := t.TempDir()
	l.Worktrees = &worktree.Manager{Root: root, Git: fakeGit(t, f)}
	dir := inCheckout(t, f, root)
	// The board hands the task to the worker's own pane, so the restart
	// leaves the pane unbound and no binding comes out of it at all.
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"`+dir+`","title":"do the thing","status":"doing","claimed_by":"agent:wZ:p1"},"ready":false,"dependents":[]}`)
	// A checkout nothing is alive in, so the reap is known to have run.
	stranded := filepath.Join(root, worktree.WorkPrefix+"9-gone")
	if err := os.MkdirAll(stranded, 0o700); err != nil {
		t.Fatal(err)
	}

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if len(l.Bindings()) != 0 {
		t.Fatalf("the pane was bound, so this proves nothing about the reap: %+v", l.Bindings())
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("the reap did not run, so nothing below is proven: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the reap removed the checkout a live pane is working in: %v", err)
	}
}
