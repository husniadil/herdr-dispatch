package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// The whole point of the on-demand verb: it reserves the task and comes back,
// because bringing a worker up runs to minutes and no caller can wait that
// long. Nothing is split, started or typed until the tick that follows.
func TestDispatchReservesTheTaskAndReturnsBeforeAnythingIsSpawned(t *testing.T) {
	l, f := newLoop(t)

	res, err := l.Dispatch(context.Background(), "7")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.TaskID != "01AAA" || res.Seq != 7 || res.Project != "/src/p" {
		t.Fatalf("reservation: %+v", res)
	}
	if got := calls(t, f, "tab create"); len(got) != 0 {
		t.Fatalf("dispatch spawned before returning: %v", got)
	}
	if got := calls(t, f, "agent start"); len(got) != 0 {
		t.Fatalf("dispatch started an agent before returning: %v", got)
	}
	if got := l.Pending(); len(got) != 1 || got[0] != "01AAA" {
		t.Fatalf("pending: %v", got)
	}

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("the tick created %d tabs for the reserved task", len(got))
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("the reservation outlived its spawn: %v", got)
	}
}

// The reservation and the board's own ready row are the same task. Feeding
// both into one tick must still produce one worker.
func TestADispatchedTaskIsNotAlsoSpawnedByTheWatchingLoop(t *testing.T) {
	l, f := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "01AAA"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs for one task: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}
}

func TestDispatchRefusesATaskTheBoardWillNotHandOut(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p4"},"ready":false,"dependents":[]}`)

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.ReasonOf(err), codes.NotReady; got != want {
		t.Fatalf("dispatch of a claimed task = %v (%q), want %q", err, got, want)
	}
}

// NOT_FOUND is the board's own word, and it is reserved for a board that
// answered. The refusal arrives the way htask writes it: a JSON error
// envelope naming its code, on stdout, with a non-zero exit.
func TestDispatchRefusesATaskTheBoardDoesNotHave(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Bin(t, "htask", `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") echo '{"error":{"code":"NOT_FOUND","message":"no task 999 in /src/p"}}'; exit 3 ;;
*) echo '{}' ;;
esac`)

	// By id: a number names no board on its own, and the door says so
	// without asking, which TestDispatchOfANumberTheBoardIsNotOfferingPins.
	_, err := l.Dispatch(context.Background(), "01MISSING")
	if got, want := codes.Of(err), codes.NotFound; got != want {
		t.Fatalf("dispatch of a missing task = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "no task 999 in /src/p") {
		t.Fatalf("the board's own words are missing from %v", err)
	}
}

// The live failure this pins: the door the validation step shells out to
// refused for a reason of its own — a build skew, a flag it did not know —
// and the caller was told NOT_FOUND for a task that may well exist. A door
// that could not answer is not a board that answered.
func TestDispatchNamesTheUnderlyingRefusalWhenTheBoardCannotBeRead(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Bin(t, "htask", brokenGet)

	_, err := l.Dispatch(context.Background(), "01AAA")
	if got := codes.Of(err); got == codes.NotFound {
		t.Fatalf("a door that refused was reported as %q: %v", got, err)
	}
	if got, want := codes.Of(err), codes.Unavailable; got != want {
		t.Fatalf("dispatch over a refusing door = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "unknown flag --as") {
		t.Fatalf("the refusal's own words are missing from %v", err)
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("a validation failure left a standing reservation: %v", got)
	}
	if got := calls(t, f, "tab create"); len(got) != 0 {
		t.Fatalf("a failed dispatch split a pane: %v", got)
	}
}

// The caller retries on an error, which is exactly what happened live. Two
// refusals must leave nothing behind, and the tick that follows once the door
// is whole again must bring up one worker, not two.
func TestRetryingAFailedDispatchCannotProduceTwoReservationsOrTwoWorkers(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Bin(t, "htask", brokenGet)

	for i := 0; i < 2; i++ {
		if _, err := l.Dispatch(context.Background(), "7"); err == nil {
			t.Fatalf("dispatch %d over a refusing door succeeded", i+1)
		}
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("two failed dispatches left %d reservations: %v", len(got), got)
	}

	// The door is whole again and the board offers the task.
	f.Bin(t, "htask", htaskScript)
	f.Write(t, "ready.json", readyOne)
	if _, err := l.Dispatch(context.Background(), "7"); err != nil {
		t.Fatalf("dispatch after the door recovered: %v", err)
	}
	if got := l.Pending(); len(got) != 1 {
		t.Fatalf("reservations after one accepted dispatch: %v", got)
	}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs for one task: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}
}

// A door that refuses `task get` for a reason of its own, the way a binary
// built against a different contract does: nothing on stdout to parse, the
// complaint on stderr, a non-zero exit. The ready list still answers.
const brokenGet = `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") echo 'unknown flag --as' >&2; exit 2 ;;
*) echo '{}' ;;
esac`

func TestDispatchRefusesWhenTheFleetIsAtMaxWorkers(t *testing.T) {
	l, _ := newLoop(t)
	l.Policy.MaxWorkers = 1
	l.bindings = []decide.Binding{{TaskID: "01ZZZ", Pane: "wM:p8", PromptedAt: clock, Prompts: 1}}

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.ReasonOf(err), codes.AtCapacity; got != want {
		t.Fatalf("dispatch at capacity = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesWithoutABasePane(t *testing.T) {
	l, _ := newLoop(t)
	l.BasePane = ""

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.ReasonOf(err), codes.NoBasePane; got != want {
		t.Fatalf("dispatch with no base pane = %v (%q), want %q", err, got, want)
	}
}

func TestDispatchRefusesATaskItIsAlreadyDriving(t *testing.T) {
	l, _ := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7"); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.ReasonOf(err), codes.AlreadyDispatched; got != want {
		t.Fatalf("second dispatch = %v (%q), want %q", err, got, want)
	}
}

// A reservation the board has taken back is dropped rather than spawned: the
// worker that claimed it in the meantime is the one doing the work.
func TestAReservationTheBoardTookBackIsDroppedAtTheNextTick(t *testing.T) {
	l, f := newLoop(t)
	if _, err := l.Dispatch(context.Background(), "7"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p4"},"ready":false,"dependents":[]}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 0 {
		t.Fatalf("a task the board took back was still spawned: %v", got)
	}
	if got := l.Pending(); len(got) != 0 {
		t.Fatalf("pending: %v", got)
	}
}

func TestStatusReportsTheBindingsAndWhatHerdrSaysAboutTheirPanes(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.BasePane != "wM:p1" || st.MaxWorkers != 2 {
		t.Fatalf("status: %+v", st)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	w := st.Workers[0]
	if w.TaskID != "01AAA" || w.Seq != 7 || w.Title != "do the thing" || w.Project != "/src/p" {
		t.Fatalf("worker row: %+v", w)
	}
	if w.Pane != "wM:p9" || w.AgentStatus != "working" || !w.PaneAlive {
		t.Fatalf("worker pane: %+v", w)
	}
	if w.Prompts != 1 || !w.PromptedAt.Equal(clock) || w.Notified {
		t.Fatalf("worker delivery: %+v", w)
	}
}

func TestStatusSaysAPaneIsGoneWhenHerdrNoLongerListsIt(t *testing.T) {
	l, _ := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].PaneAlive {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if st.Workers[0].AgentStatus != "" {
		t.Fatalf("a pane herdr does not list has no agent_status: %+v", st.Workers[0])
	}
}

// Dispatch runs on a door's goroutine while the tick runs on the daemon's.
// The race detector is the assertion.
func TestDispatchAndStatusAreSafeAlongsideATick(t *testing.T) {
	l, _ := newLoop(t)
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			l.Status(ctx)
			l.Dispatch(ctx, "7")
			l.Bindings()
			l.Pending()
		}
	}()
	for i := 0; i < 3; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Errorf("tick: %v", err)
		}
	}
	<-done
}

// A task id belongs to the board, not to a project. Dispatching one that is
// ready on another project's board reserves and spawns like any other, and
// the by-id lookup that validates it looks across every project, exactly as
// `task list --ready` already does.
func TestDispatchResolvesATaskFiledOnAnotherProjectsBoard(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"todo"}],"count":1}`)
	f.Write(t, "get.json", `{"task":{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"todo"},"ready":true,"dependents":[]}`)

	res, err := l.Dispatch(context.Background(), "01ZZZ")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.TaskID != "01ZZZ" || res.Project != "/src/other" {
		t.Fatalf("reservation: %+v", res)
	}
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs for the reserved task: %v", len(got), got)
	}
	// The tick that follows reads the bound task back by id; that lookup is
	// the one a single-project scope would lose.
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	for _, c := range calls(t, f, "task get") {
		if !strings.Contains(c, "--all-projects") {
			t.Fatalf("a by-id lookup was scoped to one project: %q", c)
		}
	}
	if len(calls(t, f, "task get")) == 0 {
		t.Fatal("no by-id lookup was recorded")
	}
}

// An id that is on no board at all is still NOT_FOUND, and the refusal must
// not tell the caller it was looked for in one project only — that reading is
// what sends an operator hunting for the wrong board.
func TestDispatchOfAnIdOnNoBoardDoesNotBlameOneProject(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Bin(t, "htask", `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get") echo '{"error":{"code":"NOT_FOUND","message":"no task 01ZZZ"}}'; exit 3 ;;
*) echo '{}' ;;
esac`)

	_, err := l.Dispatch(context.Background(), "01ZZZ")
	if got, want := codes.Of(err), codes.NotFound; got != want {
		t.Fatalf("dispatch of a missing task = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "no board has task 01ZZZ") {
		t.Fatalf("the refusal reads as a single-project miss: %v", err)
	}
}

// The widened lookup must not soften the refusal a not-ready task already
// gets, whichever project's board it sits on.
func TestDispatchStillRefusesATaskThatIsNotReadyOnAnotherBoard(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"doing","claimed_by":"agent:wM:p4"},"ready":false,"dependents":[]}`)

	_, err := l.Dispatch(context.Background(), "01ZZZ")
	if got, want := codes.ReasonOf(err), codes.NotReady; got != want {
		t.Fatalf("dispatch of a claimed task on another board = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "agent:wM:p4") {
		t.Fatalf("the refusal lost who holds the task: %v", err)
	}
}

// An operator reading status has to be able to find the work, which is now
// on a branch rather than in the project directory. So status names it.
func TestStatusNamesTheBranchBesideThePane(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"n","agent":"claude","agent_status":"working","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if got, want := st.Workers[0].Branch, worktree.Branch(7); got != want {
		t.Fatalf("status names branch %q, the worker is on %q", got, want)
	}
}

// The door's own half of the same defect: a bare number cannot be read
// across projects, so the explainer must not ask the board for one. Before
// this, a dispatch of a number the board was not offering came back as
// UNAVAILABLE quoting a USAGE error, which reads as a broken door rather
// than as a task that is not on offer.
func TestDispatchOfANumberTheBoardIsNotOffering(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	_, err := l.Dispatch(context.Background(), "7")
	if got, want := codes.ReasonOf(err), codes.NotReady; got != want {
		t.Fatalf("dispatch of a number not on offer = %v (%q), want %q", err, got, want)
	}
	if got := calls(t, f, "task get"); len(got) != 0 {
		t.Fatalf("the board was asked for a bare number across projects: %v", got)
	}
}

// The merge-time surprise, made visible while the work is still running: a
// worker's branch was cut from the project's HEAD, and by the time it
// submits another task may have landed and moved that HEAD on. Status is
// where the operator would see it, so status is where it is measured. Both
// directions are pinned here: a check that always reports and a check that
// never reports each fail one half of this case.
func TestStatusSaysABranchIsBehindOnlyOnceTheProjectHeadHasMovedPastIt(t *testing.T) {
	l, f, project := newVerifyLoop(t, false)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "working"))

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if st.Workers[0].Behind {
		t.Fatalf("a branch cut from the project's own HEAD is reported behind")
	}

	gitIn(t, project, "commit", "-q", "--allow-empty", "-m", "another task landed")

	st, err = l.Status(context.Background())
	if err != nil {
		t.Fatalf("status after the project moved: %v", err)
	}
	if !st.Workers[0].Behind {
		t.Fatalf("the project moved past the branch and status did not say so")
	}
}

// The operator reads status through a door as often as on a terminal, so the
// same fact has to survive the socket rather than live only in the CLI's
// formatting.
func TestTheJSONStatusCarriesBehindAsAField(t *testing.T) {
	l, f, project := newVerifyLoop(t, false)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "working"))
	gitIn(t, project, "commit", "-q", "--allow-empty", "-m", "another task landed")

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	doc, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back struct {
		Workers []struct {
			Branch string `json:"branch"`
			Behind bool   `json:"behind"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(doc, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Workers) != 1 {
		t.Fatalf("workers in %s", doc)
	}
	if !back.Workers[0].Behind {
		t.Fatalf("the JSON status lost the fact: %s", doc)
	}
	if back.Workers[0].Branch != worktree.Branch(7) {
		t.Fatalf("behind is reported against %q: %s", back.Workers[0].Branch, doc)
	}
}

// gitIn runs one git command in a repository a case is driving.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, out)
	}
}

// The other half of the same property, where the operator actually reads it:
// a git that cannot answer leaves the row UNMARKED and says why in the log.
// Marking it instead would send the operator to rebase a branch that was
// never behind, which replaces a reviewed commit with one nobody reviewed.
func TestAGitThatCannotAnswerLeavesTheRowUnmarkedAndSaysWhy(t *testing.T) {
	l, f, _ := newVerifyLoop(t, false)
	var logged bytes.Buffer
	l.Log = log.New(&logged, "", 0)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "working"))
	// Broken only at the question, so the call gets as far as asking it.
	l.Worktrees.(*worktree.Manager).Git = gitFailingAt(t, "merge-base")

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("workers: %+v", st.Workers)
	}
	if st.Workers[0].Behind {
		t.Fatalf("a git that could not answer marked the branch behind")
	}
	if !strings.Contains(logged.String(), worktree.Branch(7)) {
		t.Fatalf("the reason never reached the operator's log: %q", logged.String())
	}
}

// gitFailingAt is git, except that the named subcommand exits 2 — not 1,
// which is an answer this code reads rather than a failure.
func gitFailingAt(t *testing.T, verb string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"" + verb + "\" ]; then exit 2; fi\ndone\nexec git \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub git: %v", err)
	}
	return path
}

// duringWorker is the worktree manager with a hook on the one call a spawn
// makes before it starts opening panes. It is how a test gets inside the
// minutes-long window a spawn runs in with the lock released.
type duringWorker struct {
	Trees
	hook func()
}

func (d duringWorker) Worker(ctx context.Context, project string, seq int) (string, string, error) {
	d.hook()
	return d.Trees.Worker(ctx, project, seq)
}

// A spawn started by a tick runs for minutes with mu released. For that whole
// window the task it is bringing a worker up for had no binding and no
// reservation, so a `dispatch` arriving inside it was told the task was free
// and a slot was free, and reserved a task that already had a worker coming.
// The fleet then ran one worker over max-workers, and every later tick read
// live >= max-workers and dispatched nothing at all.
func TestATickSpawnHoldsItsSlotWhileItRuns(t *testing.T) {
	l, _ := newLoop(t)
	l.Policy.MaxWorkers = 1

	var inside error
	var seen []string
	l.Worktrees = duringWorker{Trees: l.Worktrees, hook: func() {
		seen = l.Pending()
		_, inside = l.Dispatch(context.Background(), "7")
	}}

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(seen) != 1 || seen[0] != "01AAA" {
		t.Errorf("the in-flight spawn was invisible to anything else: pending %v", seen)
	}
	if inside == nil {
		t.Fatal("a dispatch inside the spawn window was accepted for a task already being spawned for")
	}
	if got := codes.ReasonOf(inside); got != codes.AlreadyDispatched {
		t.Errorf("the dispatch was refused with %s: %v", got, inside)
	}
	// And the slot is not held past the spawn: the binding is what holds it
	// now.
	if got := l.Pending(); len(got) != 0 {
		t.Errorf("the reservation outlived the spawn: %v", got)
	}
}
