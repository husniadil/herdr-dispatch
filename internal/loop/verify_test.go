package loop

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// This herdr hands out a fresh tab and pane id per create rather than the one
// id the worker cases reuse, so a case can prove that a SECOND pane was never
// opened rather than that a reused id was.
const herdrTwoPanes = `case "$1 $2" in
"tab create")
  n=$(cat "$HDIS_FAKE_DIR/splitn" 2>/dev/null || echo 8)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/splitn"
  printf '{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"wM:t%s","workspace_id":"wM","label":"hdis-7"},"root_pane":{"pane_id":"wM:p%s","workspace_id":"wM","tab_id":"wM:t%s","terminal_id":"x","focused":false,"agent_status":"unknown","revision":0}}}' "$n" "$n" "$n" ;;
"tab list") cat "$HDIS_FAKE_DIR/tabs.json" ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// row renders the board's answer for the one task, in the given project and
// status. A worker needs the project to be a real repository, so every case
// names its own.
func row(project, status, claimedBy string) string {
	return rowFrom(project, status, claimedBy, "")
}

// rowFrom is row with the pane the board says the task was created from. An
// empty origin renders the field the way a board answers for a task an
// operator filed at a terminal.
func rowFrom(project, status, claimedBy, origin string) string {
	return `{"task":{"id":"01AAA","seq":7,"project":"` + project + `","title":"do the thing","status":"` + status +
		`","claimed_by":"` + claimedBy + `","pane_id":"` + origin + `"},"ready":false,"dependents":[]}`
}

// gitRepo makes a git repository with one commit, which is what a project
// has to be before a worker can be given a worktree of it.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := realPath(t, t.TempDir())
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "one"},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

// realPath resolves the symlinks a temp directory carries on macOS, where
// t.TempDir hands back /var/... and git records /private/var/... for the
// same directory. Every path a case compares goes through here.
func realPath(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return real
}

// worktreesOf is what git says the project currently has checked out
// elsewhere, one line per worktree.
func worktreesOf(t *testing.T, project string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", project, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	var dirs []string
	for _, line := range strings.Split(string(out), "\n") {
		if dir, ok := strings.CutPrefix(line, "worktree "); ok && strings.TrimSpace(dir) != project {
			dirs = append(dirs, strings.TrimSpace(dir))
		}
	}
	return dirs
}

// cwdOf reads the --cwd a recorded `tab create` was called with.
func cwdOf(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if a == "--cwd" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("no --cwd in %v", argv)
	return ""
}

// splits returns the argv of every recorded `tab create`: one per agent this
// dispatcher brought up.
func splits(t *testing.T, f *fake.Fake) [][]string {
	t.Helper()
	var out [][]string
	for _, argv := range f.Argv(t) {
		if len(argv) >= 2 && argv[0] == "tab" && argv[1] == "create" {
			out = append(out, argv)
		}
	}
	return out
}

// pane renders a pane list herdr would answer with, one entry per id/status
// pair given.
func paneList(entries ...string) string {
	var rows []string
	for i := 0; i+1 < len(entries); i += 2 {
		rows = append(rows, `{"pane_id":"`+entries[i]+`","name":"n","agent":"claude","agent_status":"`+entries[i+1]+
			`","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}`)
	}
	return `{"id":"x","result":{"type":"pane_list","panes":[` + strings.Join(rows, ",") + `]}}`
}

// newVerifyLoop is newLoop with the verification lane on and a herdr that can
// hand out more than one pane.
func newVerifyLoop(t *testing.T, enabled bool) (*Loop, *fake.Fake, string) {
	t.Helper()
	return newVerifyLoopIn(t, enabled, gitRepo(t))
}

// newVerifyLoopIn is newVerifyLoop with the project named, so a case can
// hand it a directory that is not a repository.
func newVerifyLoopIn(t *testing.T, enabled bool, project string) (*Loop, *fake.Fake, string) {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "htask", htaskScript)
	f.Bin(t, "herdr", herdrTwoPanes)
	f.Write(t, "tabs.json", `{"id":"x","result":{"type":"tab_list","tabs":[]}}`)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"`+project+`","title":"do the thing","status":"todo"}],"count":1}`)
	f.Write(t, "get.json", row(project, "todo", ""))
	f.Write(t, "panes.json", paneList())
	f.Write(t, "screen.txt", "⎿  Goal set: do the thing\n  ◎ /goal active\n")
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}`)

	doc := `default = "worker"
[profiles.worker]
provider = "claude"
`
	if enabled {
		doc = `default = "worker"
[profiles.worker]
provider = "claude"
[verify]
enabled = true
`
	}
	cfg, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{
		Board:  &htask.Client{},
		Herdr:  &herdr.Client{},
		Config: cfg,
		Policy: decide.Policy{MaxWorkers: 4, ClaimTimeout: 5 * time.Minute, MaxPrompts: 2, Verify: cfg.Verify.Enabled},
		Spawn: &spawn.Pipeline{
			Herdr: &herdr.Client{}, Proxy: &proxy.Client{},
			StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: 5 * time.Second,
			Poll: time.Second, Sleep: func(time.Duration) {},
		},
		Worktrees: &worktree.Manager{Root: realPath(t, t.TempDir())},
		Store:     &store.Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")},
		BasePane:  "wM:p1",
		Now:       func() time.Time { return clock },
		Log:       log.New(io.Discard, "", 0),
	}
	return l, f, project
}

// submitted moves the board's one task into review and leaves the worker's
// pane alive, which is the state the verification lane acts on.
func submitted(t *testing.T, f *fake.Fake, project string) {
	t.Helper()
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", row(project, "review", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
}

func bindingFor(l *Loop, pane string) (decide.Binding, bool) {
	for _, b := range l.Bindings() {
		if b.Pane == pane {
			return b, true
		}
	}
	return decide.Binding{}, false
}

// Off is the default: a submission is announced and nothing else is sent.
func TestWithTheLaneOffASubmissionEarnsNoSelfReviewShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, false)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 0 {
		t.Fatalf("%d prompts were sent with the lane off: %v", len(got), got)
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("review was announced %d times: %v", len(got), got)
	}
}

// CRITERION 2. On, the submission earns a second condition in the pane that
// produced it. One pane exists over the whole run: the lane costs a prompt,
// not an agent.
func TestASubmissionEarnsASelfReviewShotInTheWorkersOwnPane(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs; the shot takes no pane of its own: %v", len(got), got)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("split %d panes for the shot: %v", len(got), got)
	}
	if got := calls(t, f, "agent start"); len(got) != 1 {
		t.Fatalf("started %d agents: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}

	shots := calls(t, f, "agent prompt")
	if len(shots) != 1 {
		t.Fatalf("sent %d prompts: %v", len(shots), shots)
	}
	if !strings.Contains(shots[0], "wM:p9") {
		t.Fatalf("the shot did not go to the worker's own pane: %q", shots[0])
	}
	if !strings.Contains(shots[0], spawn.GoalPrefix+spawn.SelfReviewCondition(7)) {
		t.Fatalf("the shot did not carry the second condition as a /goal: %q", shots[0])
	}
	w, _ := bindingFor(l, "wM:p9")
	if !w.Verified {
		t.Fatalf("the binding does not remember its shot: %+v", w)
	}
	// Asking for the check is not judging it.
	for _, verb := range []string{"task approve", "task reject"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran %q: %v", verb, got)
		}
	}
}

// CRITERION 3, first half. One submission, one shot, however many ticks pass
// over it. Binding.Verified is what says the submission has had it.
func TestASecondTickDoesNotSendASecondShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	for i := 0; i < 4; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("one submission earned %d shots: %v", len(got), got)
	}
}

// CRITERION 3, second half. A rejection takes the task out of review, which
// rearms the binding, and the NEXT submission earns its own shot.
func TestANewSubmissionEarnsAnotherShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	// Rejected: back to doing, with the worker still holding it and working.
	f.Write(t, "get.json", row(project, "doing", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList("wM:p9", "working"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	w, _ := bindingFor(l, "wM:p9")
	if w.Verified || w.Notified {
		t.Fatalf("the binding still remembers the settled submission: %+v", w)
	}

	// Submitted again: a second shot, and review announced a second time.
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("fourth tick: %v", err)
	}
	shots := calls(t, f, "agent prompt")
	if len(shots) != 2 {
		t.Fatalf("two submissions earned %d shots: %v", len(shots), shots)
	}
	for i, got := range shots {
		if !strings.Contains(got, spawn.GoalPrefix+spawn.SelfReviewCondition(7)) {
			t.Fatalf("shot %d is not the second condition as a /goal: %q", i, got)
		}
	}
	if got := calls(t, f, "notification show"); len(got) != 2 {
		t.Fatalf("announced review %d times over two submissions: %v", len(got), got)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("created %d tabs over two submissions: %v", len(got), got)
	}
}

// A restart re-adopts the worker and the shot it has already had, so the same
// submission is not shot twice across a restart.
func TestARestartRemembersTheShotASubmissionHasHad(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	next := restarted(t, l)
	next.Worktrees = l.Worktrees
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	w, ok := bindingFor(next, "wM:p9")
	if !ok || !w.Verified {
		t.Fatalf("the shot was forgotten across the restart: %+v", next.Bindings())
	}
	if err := next.Tick(context.Background()); err != nil {
		t.Fatalf("tick after restart: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("the restart shot the same submission again: %v", got)
	}
}

// Status names every pane this daemon drives, and they are all workers now.
func TestStatusNamesEveryPaneAsAWorker(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 {
		t.Fatalf("status names %d panes: %+v", len(st.Workers), st.Workers)
	}
	if st.Workers[0].Pane != "wM:p9" || st.Workers[0].Kind != decide.KindWorker {
		t.Fatalf("status: %+v", st.Workers[0])
	}
}

// Every pane this daemon splits is launched with the dispatcher's own pane in
// its environment, so whatever comes up in it knows where to answer.
func TestEveryPaneSplitCarriesTheDispatcherAddress(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	all := calls(t, f, "tab create")
	if len(all) != 1 {
		t.Fatalf("created %d tabs: %v", len(all), all)
	}
	want := "--env " + spawn.DispatcherPaneVar + "=" + l.BasePane
	if !strings.Contains(all[0], want) {
		t.Errorf("the split does not carry %q: %q", want, all[0])
	}
}

// The worker's spend follows the task, not the daemon: a lane that reported
// to whoever started hdis charged one operator's tokens to answer another
// operator's board.
func TestAWorkerIsAddressedAtTheTasksPaneOfOrigin(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"`+project+`","title":"do the thing","status":"todo","pane_id":"wZ:p2"}],"count":1}`)
	f.Write(t, "get.json", rowFrom(project, "todo", "", "wZ:p2"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	all := calls(t, f, "tab create")
	if len(all) != 1 {
		t.Fatalf("created %d tabs: %v", len(all), all)
	}
	want := "--env " + spawn.DispatcherPaneVar + "=wZ:p2"
	if !strings.Contains(all[0], want) {
		t.Errorf("the split does not carry %q: %q", want, all[0])
	}
	if strings.Contains(all[0], spawn.DispatcherPaneVar+"="+l.BasePane) {
		t.Errorf("the split still addresses the daemon: %q", all[0])
	}
}

// The worker gets a checkout of its own, on a branch named for the task and
// starting at the project's HEAD. Two workers in the shared tree is how one
// task's commit swept up another task's uncommitted work.
func TestTheWorkerIsGivenAWorktreeOfItsOwnOnItsOwnBranch(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	got := splits(t, f)
	if len(got) != 1 {
		t.Fatalf("created %d tabs: %v", len(got), got)
	}
	cwd := cwdOf(t, got[0])
	if cwd == project || strings.HasPrefix(cwd, project+string(filepath.Separator)) {
		t.Fatalf("the worker was put in the project directory: %s", cwd)
	}
	if held := worktreesOf(t, project); len(held) != 1 || held[0] != cwd {
		t.Fatalf("git records %v, the worker was given %s", held, cwd)
	}
	branch := worktree.Branch(7)
	out, err := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("the worker's checkout is detached: %v", err)
	}
	if on := strings.TrimSpace(string(out)); on != branch {
		t.Fatalf("the worker is on %q, want %q", on, branch)
	}
	b, ok := bindingFor(l, "wM:p9")
	if !ok || b.Worktree != cwd || b.Branch != branch {
		t.Fatalf("the binding does not carry the checkout and its branch: %+v", b)
	}
}

// No worktree, nothing spawns. Working in the shared tree is worse than not
// working at all, so the reason is logged and the task simply stays where it
// is for a tick that can hand out a checkout.
func TestWithoutAWorktreeNothingIsSpawned(t *testing.T) {
	plain := t.TempDir() // a project directory that is not a git repository
	l, f, project := newVerifyLoopIn(t, true, plain)
	var logged strings.Builder
	l.Log = log.New(&logged, "", 0)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	if got := splits(t, f); len(got) != 0 {
		t.Fatalf("created %d tabs with no worktree to be had: %v", len(got), got)
	}
	if len(l.Bindings()) != 0 {
		t.Fatalf("something was bound without a worktree: %+v", l.Bindings())
	}
	if !strings.Contains(logged.String(), plain) {
		t.Fatalf("the operator was not told why: %q", logged.String())
	}
	// And the board was left alone.
	for _, verb := range []string{"task approve", "task reject"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran %q: %v", verb, got)
		}
	}
}

// A retire leaves nothing behind: the worktree goes with the binding it
// belonged to.
func TestARunLeavesNoWorktreeBehind(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	w, ok := bindingFor(l, "wM:p9")
	if !ok || w.Worktree == "" {
		t.Fatalf("the worker has no worktree: %+v", l.Bindings())
	}

	// Approved: the task is terminal and the pane goes with it.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", row(project, "done", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if left := worktreesOf(t, project); slices.Contains(left, w.Worktree) {
		t.Fatalf("git still records the retired worker's checkout: %v", left)
	}
	if _, err := os.Stat(w.Worktree); !os.IsNotExist(err) {
		t.Fatalf("the worktree directory outlived the binding: %v", err)
	}
}

// The pane going out from under a worker drops the binding without retiring
// anything, and the worktree still goes with it.
func TestAVanishedPaneTakesItsWorktreeWithIt(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	w, ok := bindingFor(l, "wM:p9")
	if !ok || w.Worktree == "" {
		t.Fatalf("the worker has no worktree: %+v", l.Bindings())
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "panes.json", paneList()) // the worker's pane is gone
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if left := worktreesOf(t, project); slices.Contains(left, w.Worktree) {
		t.Fatalf("git still records the vanished worker's checkout: %v", left)
	}
}

// CRITERION 4. Shot two is armed as a /goal and not sent as a plain prompt.
// A plain prompt fires exactly once, so a shallow pass at the mutations ends
// the check: nothing asks again. A /goal condition is evaluated after every
// turn, so the worker's own loop refuses to stop on a half-done pass.
//
// The blocker this replaces was never measured. The comment on prompt() said
// a slash command that long "cannot arrive through a prompt anyway", and
// TypedLineBudget — the only number nearby — bounds the SPAWN line, which
// spawn.go says explicitly is not this path. The real ceiling is the
// operator's measurement, 1024 with 1023 safe, and it lives in
// spawn.PromptedGoalBudget with the whole delivered text pinned under it.
//
// The nudges are NOT goals. A nudge is one instruction for one turn — claim
// the row, read the rejection, say what is left — and arming any of them as a
// standing condition would leave a worker re-satisfying it forever.
func TestOnlyTheSelfReviewShotIsArmedAsAGoal(t *testing.T) {
	l := &Loop{}
	for _, reason := range []string{decide.ReasonUnclaimed, decide.ReasonStalled} {
		if got := l.nudge(decide.Action{TaskID: "t1", Reason: reason}); strings.HasPrefix(got, spawn.GoalPrefix) {
			t.Errorf("the %q nudge is armed as a standing goal: %q", reason, got)
		}
	}
	got := l.nudge(decide.Action{TaskID: "t1", Reason: decide.ReasonSelfReview})
	if !strings.HasPrefix(got, spawn.GoalPrefix) {
		t.Fatalf("the self-review shot is not armed as a goal: %q", got)
	}
	if len(got) > spawn.PromptedGoalBudget {
		t.Fatalf("the self-review shot is %d characters against a budget of %d",
			len(got), spawn.PromptedGoalBudget)
	}
}

// §5.9: a consumer that renders stored text into a bounded artifact MUST clamp
// at RENDER time and say what it dropped. Until this, the self-review /goal
// sat at ~985 characters against a 1023 budget with nothing but a test between
// them — and a test that pins one rendering says nothing about the next one.
//
// A /goal is clamped to nothing rather than to a prefix: the tail of this
// condition is what makes the worker keep fixing rather than report and stop,
// so a truncated one is a different condition under the same name.
func TestAPromptedGoalOverItsBudgetIsNotDelivered(t *testing.T) {
	over := spawn.GoalPrefix + strings.Repeat("x", spawn.PromptedGoalBudget)
	err := promptBudget(over, decide.ReasonSelfReview)
	if err == nil {
		t.Fatal("a /goal past its measured ceiling was delivered anyway")
	}
	if !strings.Contains(err.Error(), "dropped") {
		t.Errorf("the refusal does not say what it dropped: %v", err)
	}

	if err := promptBudget(spawn.GoalPrefix+"short enough", decide.ReasonSelfReview); err != nil {
		t.Errorf("a /goal inside the budget was refused: %v", err)
	}
	// A plain nudge reaches the composer rather than the command parser, and
	// has no measured ceiling to clamp against.
	if err := promptBudget(strings.Repeat("x", spawn.PromptedGoalBudget*2), decide.ReasonStalled); err != nil {
		t.Errorf("a plain nudge was clamped against a ceiling that is not its own: %v", err)
	}
}
