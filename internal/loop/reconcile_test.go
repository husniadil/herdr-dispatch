package loop

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// heldByUs makes the board answer `task list --mine` with one row, but only
// for the principal the argv actually carries. A peer daemon's principal gets
// an empty board, which is what `--mine` really does.
func heldByUs(t *testing.T, f *testenv.Fake, principal string) {
	t.Helper()
	f.Write(t, "mine.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"`+principal+`"}],"count":1}`)
	f.Bin(t, "htask", `mine=no
for a in "$@"; do
  [ "$a" = "--mine" ] && mine=yes
done
case "$1" in
"list")
  if [ "$mine" = yes ]; then
    case " $* " in
      *" --as `+principal+` "*) cat "$HDIS_FAKE_DIR/mine.json" ;;
      *) echo '{"tasks":[],"count":0}' ;;
    esac
  else
    cat "$HDIS_FAKE_DIR/ready.json"
  fi ;;
"get") cat "$HDIS_FAKE_DIR/get.json" ;;
"release") echo '{"task":{"id":"01AAA","seq":7,"status":"todo"}}' ;;
*) echo '{}' ;;
esac`)
}

// A reservation this daemon made and never spawned for leaves the board
// holding the task. The restart that finds a live worker pane on it takes the
// worker back rather than releasing work that is under way.
func TestARestartAdoptsAStaleReservationWhoseWorkerIsAlive(t *testing.T) {
	l, f := newLoop(t)
	l.Board = &htask.Client{Principal: htask.PrincipalFor(l.BasePane)}
	heldByUs(t, f, htask.PrincipalFor("wM:p1"))
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"/src/p"}`)
	// Nothing is ready any more: the board is holding the task for us.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	got := l.Bindings()
	if len(got) != 1 || got[0].TaskID != "01AAA" || got[0].Pane != "wM:p9" {
		t.Fatalf("the live worker was not adopted: %+v", got)
	}
	if rel := calls(t, f, "release"); len(rel) != 0 {
		t.Fatalf("a live worker's task was released: %v", rel)
	}
	if !strings.Contains(said.String(), "01AAA") {
		t.Fatalf("the operator was not told why: %q", said.String())
	}
}

// The other half: no pane is working it, so the hold is stale and the task
// goes back on the board instead of sitting reserved forever.
func TestARestartReleasesAStaleReservationWithNoLiveWorker(t *testing.T) {
	l, f := newLoop(t)
	l.Board = &htask.Client{Principal: htask.PrincipalFor(l.BasePane)}
	heldByUs(t, f, htask.PrincipalFor("wM:p1"))
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"}]}}`)
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	f.Bin(t, "herdr", `case "$1 $2" in
"tab create") echo '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"wM:t9","workspace_id":"wM","label":"hdis-7"},"root_pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t9","terminal_id":"x","focused":false,"agent_status":"unknown","revision":0}}}' ;;
"tab list") cat "$HDIS_FAKE_DIR/tabs.json" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") echo '{"error":{"code":"agent_not_found","message":"no agent hdis-7"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := l.Bindings(); len(got) != 0 {
		t.Fatalf("a binding was invented for a pane that is not there: %+v", got)
	}
	rel := calls(t, f, "release 01AAA")
	if len(rel) != 1 {
		t.Fatalf("the stale hold was not released: %v", f.Calls(t))
	}
	if !strings.Contains(said.String(), "01AAA") {
		t.Fatalf("the operator was not told why: %q", said.String())
	}
}

// Both readings of the same board. A hold carrying this daemon's own pane is
// its own to resolve; a hold carrying another daemon's is a peer's, and a
// restart must not touch it.
func TestARestartTellsItsOwnStaleReservationFromAPeers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		holder   string
		wantOurs bool
	}{
		{"its own", htask.PrincipalFor("wM:p1"), true},
		{"a live peer's", htask.PrincipalFor("wM:pZ"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newLoop(t)
			l.Board = &htask.Client{Principal: htask.PrincipalFor(l.BasePane)}
			heldByUs(t, f, tc.holder)
			f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"}]}}`)
			f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
			f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
			l.Log = log.New(io.Discard, "", 0)
			if _, err := l.Adopt(context.Background()); err != nil {
				t.Fatalf("adopt: %v", err)
			}
			rel := calls(t, f, "release")
			if tc.wantOurs && len(rel) != 1 {
				t.Fatalf("this daemon's own stale hold was left: %v", f.Calls(t))
			}
			if !tc.wantOurs && len(rel) != 0 {
				t.Fatalf("a peer daemon's hold was released: %v", rel)
			}
		})
	}
}

// A reservation that never became a binding survives the process and carries
// the daemon that made it, so the restart above has something to attribute.
func TestAReservationOutlivesTheDaemonThatMadeIt(t *testing.T) {
	l, f := newLoop(t)
	l.Board = &htask.Client{Principal: htask.PrincipalFor(l.BasePane)}
	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	state, err := l.Store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("the reservation was not written: %+v", state)
	}
	if got, want := state.Reservations[0].Owner, htask.PrincipalFor("wM:p1"); got != want {
		t.Fatalf("the reservation records owner %q, want %q", got, want)
	}

	next := restarted(t, l)
	next.Board = l.Board
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"idle"}]}}`)
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := next.Pending(); len(got) != 1 || got[0] != "01AAA" {
		t.Fatalf("the reservation did not survive the restart: %v", got)
	}
}

// removingGit stands in for what `git worktree remove --force` really does:
// it takes the directory named last on the line. A fake that only exits zero
// deletes nothing, and a reap test standing on one passes whether the code
// removed anything or not.
func removingGit(t *testing.T, f *testenv.Fake) {
	t.Helper()
	f.Bin(t, "git", `for a in "$@"; do last=$a; done
[ "$3" = worktree ] && rm -rf "$last"
exit 0`)
}

// A worktree under this daemon's own state dir that no binding names is a
// checkout whose binding went before the retire could run. The next start
// takes it.
func TestARestartReapsAWorktreeNoBindingNames(t *testing.T) {
	l, f := newLoop(t)
	root := t.TempDir()
	l.Worktrees = &worktree.Manager{Root: root, Git: filepath.Join(f.Dir, "git")}
	removingGit(t, f)
	stranded := filepath.Join(root, "hdis-work-7-abc")
	if err := os.MkdirAll(stranded, 0o700); err != nil {
		t.Fatal(err)
	}

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("the stranded worktree is still there: %v", err)
	}
	if !strings.Contains(said.String(), "hdis-work-7-abc") {
		t.Fatalf("the operator was not told what was reaped: %q", said.String())
	}
}

// The reap is bounded by what this daemon makes. A worktree a binding still
// names stays, and anything else in the directory is not ours to remove.
func TestARestartKeepsWhatItDidNotStrand(t *testing.T) {
	l, f := newLoop(t)
	root := t.TempDir()
	l.Worktrees = &worktree.Manager{Root: root, Git: filepath.Join(f.Dir, "git")}
	removingGit(t, f)
	bound := filepath.Join(root, "hdis-work-7-live")
	foreign := filepath.Join(root, "notes")
	// A directory that IS this daemon's to reap, so the reap is known to have
	// run and to really remove. Without it the two survivors below prove
	// nothing: a reap that removed nothing at all would leave them too.
	stranded := filepath.Join(root, "hdis-work-9-gone")
	for _, d := range []string{bound, foreign, stranded} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review"},"ready":false,"dependents":[]}`)
	if err := l.Store.Save(store.State{Bindings: []decide.Binding{
		{TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker, Worktree: bound, PromptedAt: clock, Prompts: 1},
	}}); err != nil {
		t.Fatal(err)
	}

	l.Log = log.New(io.Discard, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("the reap did not run, so nothing below is proven: %v", err)
	}
	for _, d := range []string{bound, foreign} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was removed: %v", d, err)
		}
	}
}

// A worker's checkout is reaped by one rule: under this daemon's own root,
// carrying the prefix this daemon names them with, and named by no binding.
// A directory hdis did not make is left where it is.
func TestARestartReapsAWorkerCheckoutAndLeavesWhatItDidNotMake(t *testing.T) {
	l, f := newLoop(t)
	root := t.TempDir()
	l.Worktrees = &worktree.Manager{Root: root, Git: fakeGit(t, f)}
	stranded := filepath.Join(root, worktree.WorkPrefix+"7-abc")
	foreign := filepath.Join(root, "the-operators-own-checkout")
	for _, d := range []string{stranded, foreign} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	if _, err := l.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(stranded); !os.IsNotExist(err) {
		t.Fatalf("the stranded worker checkout is still there: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("a directory hdis did not make was removed: %v", err)
	}
	if !strings.Contains(said.String(), filepath.Base(stranded)) {
		t.Fatalf("the operator was not told what was reaped: %q", said.String())
	}
}
