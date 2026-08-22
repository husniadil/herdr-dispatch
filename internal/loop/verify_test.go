package loop

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

// The verification lane needs two panes at once, so this herdr hands out a
// fresh pane id per split rather than the one id the worker cases reuse.
const herdrTwoPanes = `case "$1 $2" in
"pane split")
  n=$(cat "$HDIS_FAKE_DIR/splitn" 2>/dev/null || echo 8)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/splitn"
  printf '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p%s","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}' "$n" ;;
"pane read") cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// row renders the board's answer for the one task, in the given project and
// status. The verification lane needs the project to be a real repository,
// so every case names its own.
func row(project, status, claimedBy string) string {
	return `{"task":{"id":"01AAA","seq":7,"project":"` + project + `","title":"do the thing","status":"` + status +
		`","claimed_by":"` + claimedBy + `"},"ready":false,"dependents":[]}`
}

// gitRepo makes a git repository with one commit, which is what a project
// has to be before a verifier can be given a worktree of it.
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

// cwdOf reads the --cwd a recorded `pane split` was called with.
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

// splits returns the argv of every recorded `pane split`.
func splits(t *testing.T, f *fake.Fake) [][]string {
	t.Helper()
	var out [][]string
	for _, argv := range f.Argv(t) {
		if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "split" {
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
	f.Write(t, "ready.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"`+project+`","title":"do the thing","status":"todo"}],"count":1}`)
	f.Write(t, "get.json", row(project, "todo", ""))
	f.Write(t, "panes.json", paneList())
	f.Write(t, "screen.txt", "⎿  Goal set: do the thing\n  ◎ /goal active\n")
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}`)

	doc := `{"default":"worker","profiles":{"worker":{"provider":"claude"},"verifier":{"provider":"claude","model":"sonnet","effort":"high"}}}`
	if enabled {
		doc = `{"default":"worker","profiles":{"worker":{"provider":"claude"},"verifier":{"provider":"claude","model":"sonnet","effort":"high"}},"verify":{"enabled":true,"profile":"verifier"}}`
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
		Store:     &store.Bindings{Path: filepath.Join(t.TempDir(), "hdis-bindings.json")},
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

// Off is the default: a submission is announced and no second pane opens.
func TestWithTheLaneOffASubmissionSpawnsNoVerifier(t *testing.T) {
	l, f, project := newVerifyLoop(t, false)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes with the lane off: %v", len(got), got)
	}
	if len(l.Bindings()) != 1 {
		t.Fatalf("bindings: %+v", l.Bindings())
	}
}

// On, the submission earns a verifier on a pane of its own, launched from the
// verifier's profile, with the condition that forbids judging it.
func TestASubmissionEarnsAVerifierOnItsOwnPane(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	if got := calls(t, f, "pane split"); len(got) != 2 {
		t.Fatalf("split %d panes: %v", len(got), got)
	}
	v, ok := bindingFor(l, "wM:p10")
	if !ok {
		t.Fatalf("no verifier binding: %+v", l.Bindings())
	}
	if !v.IsVerifier() || v.TaskID != "01AAA" {
		t.Fatalf("verifier binding: %+v", v)
	}
	w, _ := bindingFor(l, "wM:p9")
	if !w.Verified {
		t.Fatalf("the worker's binding does not remember its verifier: %+v", w)
	}

	start := calls(t, f, "agent start")
	if len(start) != 2 {
		t.Fatalf("started %d agents: %v", len(start), start)
	}
	for _, want := range []string{"--pane wM:p10", "--model sonnet", "--effort high", spawn.GoalPrefix + spawn.VerifierGoal(7)} {
		if !strings.Contains(start[1], want) {
			t.Fatalf("want %q in the verifier's start: %q", want, start[1])
		}
	}
	// Delegating verification is not delegating judgment.
	for _, verb := range []string{"task approve", "task reject"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran %q: %v", verb, got)
		}
	}
}

// One submission, one verifier, however many ticks pass over it.
func TestASecondTickDoesNotSpawnASecondVerifier(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))
	for i := 0; i < 3; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := calls(t, f, "pane split"); len(got) != 2 {
		t.Fatalf("split %d panes for one submission: %v", len(got), got)
	}
}

// A verifier that went quiet past the grace is retired, and its pane closes.
func TestAFinishedVerifierIsRetiredAndItsPaneClosed(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "idle"))
	l.Now = func() time.Time { return clock.Add(time.Hour) }
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}

	if _, ok := bindingFor(l, "wM:p10"); ok {
		t.Fatalf("the verifier binding outlived the verifier: %+v", l.Bindings())
	}
	closed := calls(t, f, "pane close")
	if len(closed) != 1 || !strings.Contains(closed[0], "wM:p10") {
		t.Fatalf("closed panes: %v", closed)
	}
	// The worker's own binding is untouched by its verifier ending.
	if _, ok := bindingFor(l, "wM:p9"); !ok {
		t.Fatalf("the worker's binding went with the verifier: %+v", l.Bindings())
	}
}

// A rejection puts the task back to doing. The verifier is retired with the
// submission it was reading, and a resubmission earns a new one.
func TestARejectionRetiresTheVerifierAndTheNextSubmissionEarnsANewOne(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	// Rejected: back to doing, with the worker still holding it.
	f.Write(t, "get.json", row(project, "doing", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList("wM:p9", "working", "wM:p10", "working"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if _, ok := bindingFor(l, "wM:p10"); ok {
		t.Fatalf("the verifier outlived the submission it was reading: %+v", l.Bindings())
	}
	w, _ := bindingFor(l, "wM:p9")
	if w.Verified || w.Notified {
		t.Fatalf("the worker's binding still remembers the settled submission: %+v", w)
	}

	// Submitted again: a new verifier, and review announced again.
	submitted(t, f, project)
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("fourth tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 3 {
		t.Fatalf("split %d panes over two submissions: %v", len(got), got)
	}
	if got := calls(t, f, "notification show"); len(got) != 2 {
		t.Fatalf("announced review %d times over two submissions: %v", len(got), got)
	}
}

// A restart re-adopts a verifier as a verifier. Adopted as a worker it would
// be nudged to claim a task it must never claim.
func TestARestartReadoptsAVerifierAsAVerifier(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))

	next := restarted(t, l)
	if _, err := next.Adopt(context.Background()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	v, ok := bindingFor(next, "wM:p10")
	if !ok || !v.IsVerifier() {
		t.Fatalf("the verifier was not re-adopted as one: %+v", next.Bindings())
	}
	w, ok := bindingFor(next, "wM:p9")
	if !ok || w.IsVerifier() || !w.Verified {
		t.Fatalf("the worker was not re-adopted as one: %+v", next.Bindings())
	}
	_ = f
}

// Status tells the operator which panes are doing the work and which are
// checking it.
func TestStatusNamesVerifiersApartFromWorkers(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "working"))

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	kinds := map[string]string{}
	for _, w := range st.Workers {
		kinds[w.Pane] = w.Kind
	}
	if kinds["wM:p9"] != decide.KindWorker || kinds["wM:p10"] != decide.KindVerifier {
		t.Fatalf("status kinds: %+v", kinds)
	}
}

// Worker and verifier alike: every pane this daemon splits is launched with
// the dispatcher's own pane in its environment, so whatever comes up in it
// knows where to answer.
func TestEveryPaneSplitCarriesTheDispatcherAddress(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	splits := calls(t, f, "pane split")
	if len(splits) != 2 {
		t.Fatalf("split %d panes: %v", len(splits), splits)
	}
	want := "--env " + spawn.DispatcherPaneVar + "=" + l.BasePane
	for i, got := range splits {
		if !strings.Contains(got, want) {
			t.Errorf("split %d does not carry %q: %q", i, want, got)
		}
	}
}

// The verifier works in a checkout of its own. The project directory is the
// one place it must never be: its worker still holds that tree and the
// operator reviews in it.
func TestAVerifierIsGivenAWorktreeAndNeverTheProjectDirectory(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	got := splits(t, f)
	if len(got) != 2 {
		t.Fatalf("split %d panes: %v", len(got), got)
	}
	cwd := cwdOf(t, got[1])
	if cwd == project {
		t.Fatalf("the verifier was given the project directory itself: %s", cwd)
	}
	if strings.HasPrefix(cwd, project+string(filepath.Separator)) {
		t.Fatalf("the verifier's worktree is inside the project: %s", cwd)
	}
	held := worktreesOf(t, project)
	if len(held) != 1 || held[0] != cwd {
		t.Fatalf("git records %v, the verifier was given %s", held, cwd)
	}
	if b, ok := bindingFor(l, "wM:p10"); !ok || b.Worktree != cwd {
		t.Fatalf("the binding does not own the worktree: %+v", b)
	}
}

// The worker keeps the project directory: its commits belong on the branch.
func TestTheWorkerIsGivenTheProjectDirectoryItself(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	got := splits(t, f)
	if len(got) != 1 {
		t.Fatalf("split %d panes: %v", len(got), got)
	}
	if cwd := cwdOf(t, got[0]); cwd != project {
		t.Fatalf("the worker was given %s, not the project %s", cwd, project)
	}
	if held := worktreesOf(t, project); len(held) != 0 {
		t.Fatalf("a worker earned a worktree: %v", held)
	}
}

// No worktree, no verifier. Verifying in the shared tree is worse than not
// verifying, so the reason is logged and the task simply stays in review.
func TestWithoutAWorktreeNoVerifierIsSpawned(t *testing.T) {
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

	if got := splits(t, f); len(got) != 1 {
		t.Fatalf("split %d panes with no worktree to be had: %v", len(got), got)
	}
	for _, b := range l.Bindings() {
		if b.IsVerifier() {
			t.Fatalf("a verifier was bound without a worktree: %+v", b)
		}
	}
	if !strings.Contains(logged.String(), plain) {
		t.Fatalf("the operator was not told why: %q", logged.String())
	}
	// Review was still announced: the submission is the operator's to judge.
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("announced review %d times: %v", len(got), got)
	}
	// And the board was left alone.
	for _, verb := range []string{"task approve", "task reject"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran %q: %v", verb, got)
		}
	}
}

// A run leaves nothing behind: the worktree goes with the binding.
func TestARunLeavesNoWorktreeBehind(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	held := worktreesOf(t, project)
	if len(held) != 1 {
		t.Fatalf("the verifier has no worktree: %v", held)
	}

	// The verifier goes quiet past the grace and is retired.
	f.Write(t, "panes.json", paneList("wM:p9", "idle", "wM:p10", "idle"))
	l.Now = func() time.Time { return clock.Add(time.Hour) }
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if left := worktreesOf(t, project); len(left) != 0 {
		t.Fatalf("git still records %v", left)
	}
	if _, err := os.Stat(held[0]); !os.IsNotExist(err) {
		t.Fatalf("the worktree directory outlived the binding: %v", err)
	}
}

// The pane going out from under a verifier drops the binding without
// retiring anything, and the worktree still goes with it.
func TestAVanishedVerifierPaneTakesItsWorktreeWithIt(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submitted(t, f, project)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	f.Write(t, "panes.json", paneList("wM:p9", "idle")) // the verifier's pane is gone
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if left := worktreesOf(t, project); len(left) != 0 {
		t.Fatalf("git still records %v", left)
	}
}
