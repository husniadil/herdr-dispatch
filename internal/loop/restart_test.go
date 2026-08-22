package loop

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// restarted is the same dispatcher started again: a fresh Loop with nothing
// in memory, pointed at the store the one before it wrote.
func restarted(t *testing.T, l *Loop) *Loop {
	t.Helper()
	next := &Loop{
		Board:     l.Board,
		Herdr:     l.Herdr,
		Spawn:     l.Spawn,
		Config:    l.Config,
		Policy:    l.Policy,
		Store:     l.Store,
		Worktrees: l.Worktrees,
		BasePane:  l.BasePane,
		Now:       l.Now,
		Log:       log.New(io.Discard, "", 0),
	}
	return next
}

// The one mapping that exists nowhere else until the worker claims outlives
// the process that made it.
func TestAPromptedButUnclaimedWorkerIsStillTrackedAfterARestart(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-adopted %d bindings, want 1", n)
	}
	b := next.Bindings()
	if len(b) != 1 || b[0].TaskID != "01AAA" || b[0].Pane != "wM:p9" || b[0].Prompts != 1 {
		t.Fatalf("bindings: %+v", b)
	}
	// The claim timeout keeps running against the time the goal was really
	// delivered, not against the restart.
	if !b[0].PromptedAt.Equal(clock) {
		t.Fatalf("prompted_at came back as %s, want %s", b[0].PromptedAt, clock)
	}
	if next.Readopted() != 1 {
		t.Fatalf("readopted: %d", next.Readopted())
	}
}

// The orphan split: before the bindings were durable, a restart forgot the
// pane and the next tick dispatched the same task into a second one.
func TestARestartDoesNotDispatchATaskWhoseWorkerPaneIsStillAlive(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	// The task is still todo and still ready — a worker has not claimed yet
	// — which is exactly the shape that produced the second pane.
	if err := next.Tick(context.Background()); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs for one task: %v", len(got), got)
	}
	if got := next.Bindings(); len(got) != 1 || got[0].Pane != "wM:p9" {
		t.Fatalf("bindings: %+v", got)
	}
}

// A pane herdr no longer lists is a worker that is gone. The binding is
// dropped with a word to the operator, never acted on blindly.
func TestARestartDropsABindingWhosePaneIsGone(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)

	next := restarted(t, l)
	var said strings.Builder
	next.Log = log.New(&said, "", 0)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 || len(next.Bindings()) != 0 {
		t.Fatalf("re-adopted %d: %+v", n, next.Bindings())
	}
	if !strings.Contains(said.String(), "wM:p9") {
		t.Fatalf("the operator was not told which pane went: %q", said.String())
	}
	// And the drop is durable: a second restart does not find it again.
	again := restarted(t, l)
	if _, err := again.Adopt(context.Background()); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if got := again.Bindings(); len(got) != 0 {
		t.Fatalf("a dropped binding came back: %+v", got)
	}
}

// A task the board has finished with, or handed to someone else, is not this
// dispatcher's to drive any more.
func TestARestartDropsABindingWhoseTaskMovedOn(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		// A finished task takes its pane with it; a task another pane holds
		// leaves the pane alone, because whose worker it is now is the
		// board's answer and not a restart's.
		retires bool
	}{
		{name: "done", row: `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`, retires: true},
		{name: "claimed by another pane", row: `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:pZ"},"ready":false,"dependents":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newLoop(t)
			if err := l.Tick(context.Background()); err != nil {
				t.Fatalf("tick: %v", err)
			}
			f.Write(t, "panes.json", panesWith("working"))
			f.Write(t, "get.json", tc.row)

			next := restarted(t, l)
			var said strings.Builder
			next.Log = log.New(&said, "", 0)
			n, err := next.Adopt(context.Background())
			if err != nil {
				t.Fatalf("adopt: %v", err)
			}
			if n != 0 || len(next.Bindings()) != 0 {
				t.Fatalf("re-adopted %d: %+v", n, next.Bindings())
			}
			if !strings.Contains(said.String(), "01AAA") {
				t.Fatalf("the operator was not told which task went: %q", said.String())
			}
			want := 0
			if tc.retires {
				want = 1
			}
			if got := calls(t, f, "tab close"); len(got) != want {
				t.Fatalf("closed %d tabs, want %d: %v", len(got), want, got)
			}
		})
	}
}

// A board that cannot answer for one task is not evidence that the task is
// gone. The binding is held, exactly as a tick holds it.
func TestARestartHoldsABindingWhoseRowCannotBeRead(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))
	f.Bin(t, "htask", `case "$1 $2" in
"task get") echo "htask: the daemon is not answering" >&2; exit 1 ;;
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
*) echo '{}' ;;
esac`)

	next := restarted(t, l)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-adopted %d bindings, want the unreadable one held", n)
	}
}

// Herdr unreachable means the pane half of the check cannot be made at all.
// Adopting on a guess is how a live worker gets a second pane, so nothing is
// adopted, the failure is loud, and the store is left for the next start.
func TestARestartWithNoHerdrAdoptsNothingAndKeepsTheStore(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Bin(t, "herdr", `echo "herdr: no server" >&2; exit 1`)

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err == nil {
		t.Fatal("a restart with no herdr adopted quietly")
	}
	if got := next.Bindings(); len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
	held, err := l.Store.Load()
	if err != nil || len(held.Bindings) != 1 {
		t.Fatalf("the store was not left intact: %+v (%v)", held, err)
	}
}

// The claim timeout is measured from the moment the goal was delivered, and
// a restart in between neither restarts nor skips it.
func TestTheClaimTimeoutKeepsRunningAcrossARestart(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("working"))

	next := restarted(t, l)
	later := clock.Add(6 * time.Minute)
	next.Now = func() time.Time { return later }
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := next.Tick(context.Background()); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	prompts := calls(t, f, "agent prompt")
	if len(prompts) != 1 {
		t.Fatalf("the claim timeout did not carry across the restart: %v", prompts)
	}
	if got := next.Bindings(); len(got) != 1 || got[0].Prompts != 2 {
		t.Fatalf("bindings: %+v", got)
	}
}

// A store nobody has written is a first start, not a failure.
func TestAFirstStartAdoptsNothingAndSaysSo(t *testing.T) {
	l, _ := newLoop(t)
	n, err := l.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 || l.Readopted() != 0 {
		t.Fatalf("re-adopted %d", n)
	}
}

// The fourth shape of the restart window. Task 28 closed a reservation with
// no binding, a live pane with no binding, and a worktree no binding names;
// this is the one left: a binding whose task finished while the daemon was
// down. Dropping it is right, but the pane it named is one this daemon
// opened, and nothing else will ever close it.
func TestARestartRetiresThePaneOfABindingWhoseTaskFinished(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", panesWith("idle"))
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`)

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if got := next.Bindings(); len(got) != 0 {
		t.Fatalf("the binding survived a finished task: %+v", got)
	}
	// Unbound is not enough: the pane has to be gone.
	got := calls(t, f, "tab close")
	if len(got) != 1 || !strings.Contains(got[0], "wM:t9") {
		t.Fatalf("the tab of a finished task was left open: %v", got)
	}
}

// The two edges the retire must not cross: a pane this daemon never opened,
// and a pane whose task is still unfinished.
func TestARestartRetiresNoPaneItDidNotStrand(t *testing.T) {
	t.Run("a pane no binding names is never touched", func(t *testing.T) {
		l, f := newLoop(t)
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		// A second pane herdr lists that this daemon never opened, alongside
		// the one it did, whose task has finished.
		f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
			`{"pane_id":"wM:p9","agent":"claude","agent_status":"idle","focused":false,"revision":1},`+
			`{"pane_id":"wM:pX","agent":"claude","agent_status":"working","focused":false,"revision":1}]}}`)
		f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"done"},"ready":false,"dependents":[]}`)

		next := restarted(t, l)
		if _, err := next.Adopt(context.Background()); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		for _, c := range calls(t, f, "tab close") {
			if strings.Contains(c, "wM:pX") {
				t.Fatalf("a restart closed a pane it never opened: %v", c)
			}
		}
	})

	t.Run("an unfinished task keeps its pane", func(t *testing.T) {
		l, f := newLoop(t)
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		f.Write(t, "panes.json", panesWith("working"))
		f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)

		next := restarted(t, l)
		n, err := next.Adopt(context.Background())
		if err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if n != 1 {
			t.Fatalf("re-adopted %d bindings, want 1", n)
		}
		if got := calls(t, f, "tab close"); len(got) != 0 {
			t.Fatalf("a restart closed a live worker's pane: %v", got)
		}
	})
}

// agentsAre makes herdr's `agent list` name the panes this daemon opened.
func agentsAre(t *testing.T, f *fake.Fake, agents string) {
	t.Helper()
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[`+agents+`]}}`)
}

// The sixth shape. The daemon went down between delivering a goal and
// writing the binding, and by the time it came back the worker had already
// CLAIMED the task — so the board's holder is the worker's own pane and not
// this dispatcher's, and nothing looking at holds under its own principal
// sees the task at all. The pane is live, it is one this daemon opened, and
// the row it is working says what it is: it is adopted.
func TestARestartAdoptsALivePaneThatAlreadyClaimedItsTask(t *testing.T) {
	l, f := newLoop(t)
	agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"/src/p"}`)
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	// Nothing was ever written: the restart has no binding to walk.
	next := restarted(t, l)
	var said strings.Builder
	next.Log = log.New(&said, "", 0)
	n, err := next.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted %d, want the live worker taken back", n)
	}
	b := next.Bindings()
	if len(b) != 1 || b[0].TaskID != "01AAA" || b[0].Pane != "wM:p9" || b[0].Kind != decide.KindWorker {
		t.Fatalf("bindings: %+v", b)
	}
	if got := calls(t, f, "tab close"); len(got) != 0 {
		t.Fatalf("a live worker's pane was closed: %v", got)
	}
	if got := calls(t, f, "task release"); len(got) != 0 {
		t.Fatalf("a live worker's task was released: %v", got)
	}
	if !strings.Contains(said.String(), "wM:p9") {
		t.Fatalf("the operator was not told which pane was taken back: %q", said.String())
	}
}

// The three edges the reconciliation never crosses, whatever it finds.
func TestARestartTouchesNothingThatIsNotItsOwn(t *testing.T) {
	t.Run("a pane this daemon never opened", func(t *testing.T) {
		l, f := newLoop(t)
		agentsAre(t, f, `{"name":"someone-else","pane_id":"wM:pX","agent":"claude","agent_status":"working"}`)
		f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
			`{"pane_id":"wM:pX","agent":"claude","agent_status":"working","focused":false,"revision":1}]}}`)
		f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

		if _, err := l.Adopt(context.Background()); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if got := l.Bindings(); len(got) != 0 {
			t.Fatalf("a pane this daemon never opened was adopted: %+v", got)
		}
		if got := calls(t, f, "tab close"); len(got) != 0 {
			t.Fatalf("a pane this daemon never opened was closed: %v", got)
		}
	})

	t.Run("a task held by someone else", func(t *testing.T) {
		l, f := newLoop(t)
		l.Board = &htask.Client{Principal: htask.PrincipalFor(l.BasePane)}
		heldByUs(t, f, htask.PrincipalFor("wM:pZ"))
		f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)
		f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

		if _, err := l.Adopt(context.Background()); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if got := calls(t, f, "task release"); len(got) != 0 {
			t.Fatalf("a hold belonging to another daemon was released: %v", got)
		}
	})

	t.Run("a directory outside its own state dir", func(t *testing.T) {
		l, f := newLoop(t)
		outside := t.TempDir()
		root := t.TempDir()
		l.Worktrees = &worktree.Manager{Root: root, Git: filepath.Join(f.Dir, "git")}
		removingGit(t, f)
		theirs := filepath.Join(outside, "hdis-verify-9-elsewhere")
		stranded := filepath.Join(root, "hdis-verify-7-gone")
		for _, d := range []string{theirs, stranded} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}

		if _, err := l.Adopt(context.Background()); err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if _, err := os.Stat(stranded); !os.IsNotExist(err) {
			t.Fatalf("the reap did not run, so nothing below is proven: %v", err)
		}
		if _, err := os.Stat(theirs); err != nil {
			t.Fatalf("a checkout outside this daemon's own root was removed: %v", err)
		}
	})
}

// The pane the restart rule exists for is the one no binding names, and
// until this test it was the one pane whose row could not be read: the pane
// names its task by number, and a by-id read is board-agnostic, so the board
// refused the number by design. The pane is addressed the way a number is
// unique — project plus number — with the project read off the checkout the
// pane is working in.
func TestARestartReadsTheRowOfAPaneNoBindingNames(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "common.txt", "/src/p/.git\n")
	agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working",`+
		`"cwd":"/state/hdis/worktrees/hdis-work-7-abc"}`)
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	n, err := l.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted %d, want the unbound worker taken back", n)
	}
	if b := l.Bindings(); len(b) != 1 || b[0].TaskID != "01AAA" || b[0].Pane != "wM:p9" {
		t.Fatalf("bindings: %+v", b)
	}
	if strings.Contains(said.String(), "left as it is") {
		t.Fatalf("the pane the rule exists for was left alone: %q", said.String())
	}
	got := calls(t, f, "task get")
	if len(got) != 1 {
		t.Fatalf("board reads: %v", got)
	}
	if want := "task get 7 --project /src/p --json"; !strings.HasPrefix(got[0], want) {
		t.Fatalf("the row was asked for as %q, want it scoped to the project the checkout belongs to (%q)", got[0], want)
	}
}

// The three shapes a pane's checkout can have, all named the same way: git
// is asked which repository the directory belongs to, and a worktree answers
// with the repository it was cut from rather than with itself.
func TestAPanesProjectIsReadFromItsCheckoutWhateverShapeItHas(t *testing.T) {
	for _, tc := range []struct{ name, cwd string }{
		{"a worker in its worktree", "/state/hdis/worktrees/hdis-work-7-abc"},
		{"a verifier in a detached worktree", "/state/hdis/worktrees/hdis-verify-7-abc"},
		{"a pane opened before worktrees, sitting in the project", "/src/p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newLoop(t)
			f.Write(t, "common.txt", "/src/p/.git\n")
			agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"`+tc.cwd+`"}`)
			f.Write(t, "panes.json", panesWith("working"))
			f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
			f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

			n, err := l.Adopt(context.Background())
			if err != nil {
				t.Fatalf("adopt: %v", err)
			}
			if n != 1 {
				t.Fatalf("adopted %d from a pane in %s, want 1", n, tc.cwd)
			}
			var asked bool
			for _, c := range f.Argv(t) {
				if len(c) > 2 && c[0] == "-C" && c[1] == tc.cwd && c[len(c)-1] == "--git-common-dir" {
					asked = true
				}
			}
			if !asked {
				t.Fatalf("git was never asked which repository %s belongs to: %v", tc.cwd, f.Argv(t))
			}
		})
	}
}

// A pane whose checkout names no repository is not a pane to guess about.
// Nothing is adopted, and the operator is told which pane and why.
func TestARestartLeavesAPaneWhoseProjectCannotBeRead(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "git", `exit 1`)
	agentsAre(t, f, `{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"/gone"}`)
	f.Write(t, "panes.json", panesWith("working"))
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	var said strings.Builder
	l.Log = log.New(&said, "", 0)
	n, err := l.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if n != 0 || len(l.Bindings()) != 0 {
		t.Fatalf("adopted %d from a pane whose project is unknown: %+v", n, l.Bindings())
	}
	if !strings.Contains(said.String(), "wM:p9") {
		t.Fatalf("the operator was not told which pane was left: %q", said.String())
	}
	if got := calls(t, f, "task get"); len(got) != 0 {
		t.Fatalf("the board was asked about a task nothing could name: %v", got)
	}
}

// CRITERION 4. Ownership survives a pane whose agent name Herdr has dropped.
//
// This is note 24's observation made into a test. Herdr does not keep the
// agent name: the same pane, the same process and the same agent_session
// were measured answering with "name":"hdis-20" at 00:39 and with no name
// field at all at 00:57. Until this change that prefix WAS the ownership
// test, so a pane in that state stopped being this daemon's — nothing logged
// it, nothing adopted it, nothing retired it, and it sat live forever with
// its task already finished.
//
// The fake here reports exactly that: a live pane, no name, and `agent list`
// empty, which is what Herdr answers once the name is gone. What still
// identifies it is the CHECKOUT — a directory under this daemon's own state
// dir, named for the lane and the task, which nothing else on the machine
// writes to and no process can take away.
//
// It is proved at both ends, because recognition that does not lead anywhere
// is not ownership: the pane is ADOPTED while its row is live, and RETIRED
// when its row is finished.
func TestAPaneWhoseAgentNameHerdrDroppedIsStillAdoptedAndStillRetired(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  string
		label   string
		adopted int
		closed  int
		// wantTab is the tab the binding may name. It is empty for a pane
		// in the operator's own tab, so no later retire can ever close it.
		wantTab string
		// says is what the operator must be told, when anything is owed.
		says string
	}{
		{name: "a live row is adopted", status: "doing", label: "hdis task 7", adopted: 1, wantTab: "wM:t9"},
		{name: "a finished row is retired", status: "done", label: "hdis task 7", closed: 1},
		// The two evidences DISAGREE. The checkout says the pane is this
		// daemon's; the tab label says the tab is the operator's. That
		// happens when the operator drags a worker into a tab of their own,
		// and it is the case the other two subcases assume away.
		//
		// The worker is still driven. Abandoning it is what note 24
		// described: a live pane on a live task that nothing adopts,
		// nothing retires and nothing logs. What the daemon gives up is the
		// TAB, not the worker — the binding names no tab, so the operator's
		// tab can never be closed out from under them.
		{
			name:    "a worker the operator moved into their own tab is still driven",
			status:  "doing",
			label:   "notes",
			adopted: 1,
			wantTab: "",
			says:    "leaving the tab alone",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, f := newLoop(t)
			// The checkout this daemon would have handed the worker for
			// task 7, made where a real one is made and named as one.
			root := l.Worktrees.RootDir()
			tree := filepath.Join(root, worktree.WorkPrefix+"7-abc123")
			if err := os.MkdirAll(tree, 0o755); err != nil {
				t.Fatalf("checkout: %v", err)
			}

			// A live pane with NO name field, which is the whole point, and
			// an agent list that does not mention it either.
			f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
				`{"pane_id":"wM:p9","agent":"claude","agent_status":"working","cwd":"`+tree+
				`","tab_id":"wM:t9","workspace_id":"wM","focused":false,"revision":1}]}}`)
			agentsAre(t, f, "")
			f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[`+
				`{"tab_id":"wM:t9","workspace_id":"wM","label":"`+tc.label+`","pane_count":1}]}}`)
			f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"`+root+
				`","title":"do the thing","status":"`+tc.status+`","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
			f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

			var said strings.Builder
			l.Log = log.New(&said, "", 0)
			n, err := l.Adopt(context.Background())
			if err != nil {
				t.Fatalf("adopt: %v", err)
			}
			if n != tc.adopted {
				t.Fatalf("adopted %d pane(s) with no agent name, want %d; the operator was told: %q",
					n, tc.adopted, said.String())
			}
			if tc.adopted == 1 {
				b := l.Bindings()
				if len(b) != 1 || b[0].Pane != "wM:p9" || b[0].TaskID != "01AAA" {
					t.Fatalf("a nameless pane was not bound to its task: %+v", b)
				}
				if b[0].Tab != tc.wantTab {
					t.Fatalf("the binding names tab %q, want %q; a binding that names the operator's tab is a tab this daemon would later close on them",
						b[0].Tab, tc.wantTab)
				}
			}
			if tc.says != "" && !strings.Contains(said.String(), tc.says) {
				t.Fatalf("the operator was not told the tab was left alone; the log said: %q", said.String())
			}
			if got := len(calls(t, f, "tab close")) + len(calls(t, f, "pane close")); got != tc.closed {
				t.Fatalf("a nameless pane was closed %d time(s), want %d", got, tc.closed)
			}
		})
	}
}
