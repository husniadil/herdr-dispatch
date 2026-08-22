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

var clock = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// The fake board refuses a bare number under --all-projects exactly as the
// real one does. A fake that answers whatever argv it is handed is why a
// restart shipped unable to read the row of any pane it had no binding for:
// every test drove a number through it and got a row back.
const htaskScript = `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get")
  case " $* " in
  *" --all-projects "*)
    case "$3" in
    ''|*[!0-9]*) cat "$HDIS_FAKE_DIR/get.json" ;;
    *) echo '{"error":{"code":"USAGE","message":"a number is only unique inside a project, so it cannot address a task across projects"}}'; exit 2 ;;
    esac ;;
  *) cat "$HDIS_FAKE_DIR/get.json" ;;
  esac ;;
"task goal") cat "$HDIS_FAKE_DIR/goal.txt" ;;
*) echo '{}' ;;
esac`

// `pane read` answers with the terminal's own text and no JSON, which is what
// the real CLI does. A `readfail` file makes the first N reads refuse, the
// way herdr refuses: a JSON error body on stderr and a non-zero exit.
const herdrScript = `case "$1 $2" in
"pane split") echo '{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}' ;;
"pane read")
  n=$(cat "$HDIS_FAKE_DIR/readn" 2>/dev/null || echo 0)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/readn"
  if [ -f "$HDIS_FAKE_DIR/readfail" ] && [ "$n" -le "$(cat "$HDIS_FAKE_DIR/readfail")" ]; then
    echo '{"error":{"code":"pane_not_found","message":"pane wM:p9 not found"},"id":"cli:pane:read"}' >&2
    exit 1
  fi
  cat "$HDIS_FAKE_DIR/screen.txt" ;;
"pane list") cat "$HDIS_FAKE_DIR/panes.json" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
"agent list") cat "$HDIS_FAKE_DIR/agents.json" ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

const readyOne = `{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}],"count":1}`

// gitScript stands in for the git a worktree Manager runs: it answers
// rev-parse with the directory it was asked in, creates the directory a
// `worktree add` names, and removes the one a `worktree remove` names. A
// fake that only exits zero makes nothing, and a test standing on one passes
// whether the code created a checkout or not.
const gitScript = `prev=""; last=""
for a in "$@"; do prev=$last; last=$a; done
case "$3" in
rev-parse)
  [ "$4" = --verify ] && exit 1
  if [ "$5" = --git-common-dir ]; then
    cat "$HDIS_FAKE_DIR/common.txt" 2>/dev/null || echo "$2/.git"
    exit 0
  fi
  echo "$2" ;;
worktree)
  case "$4" in
  add) mkdir -p "$prev" ;;
  remove) rm -rf "$last" ;;
  esac ;;
esac
exit 0`

func fakeGit(t *testing.T, f *fake.Fake) string {
	t.Helper()
	f.Bin(t, "git", gitScript)
	return filepath.Join(f.Dir, "git")
}

func newLoop(t *testing.T) (*Loop, *fake.Fake) {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "htask", htaskScript)
	f.Bin(t, "herdr", herdrScript)
	f.Write(t, "ready.json", readyOne)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"},"ready":false,"dependents":[]}`)
	f.Write(t, "goal.txt", "do the thing · Done when: it is done")
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[]}}`)
	f.Write(t, "agents.json", `{"id":"x","result":{"type":"agent_list","agents":[]}}`)
	f.Write(t, "screen.txt", "⎿  Goal set: do the thing\n  ◎ /goal active\n")
	// Idle, so the screen is what confirms the goal rather than the status.
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}`)

	cfg, err := config.Parse([]byte(`{"default":"worker","profiles":{"worker":{"provider":"claude"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	l := &Loop{
		Board:  &htask.Client{},
		Herdr:  &herdr.Client{},
		Config: cfg,
		Policy: decide.Policy{MaxWorkers: 2, ClaimTimeout: 5 * time.Minute, MaxPrompts: 2},
		Spawn: &spawn.Pipeline{
			Herdr: &herdr.Client{}, Proxy: &proxy.Client{},
			StartTimeout: time.Second, DialogCeiling: time.Second, ConfirmCeiling: 5 * time.Second,
			Poll: time.Second, Sleep: func(time.Duration) {},
		},
		Store:     &store.Bindings{Path: filepath.Join(t.TempDir(), "hdis-bindings.json")},
		Worktrees: &worktree.Manager{Root: t.TempDir(), Git: fakeGit(t, f)},
		BasePane:  "wM:p1",
		Now:       func() time.Time { return clock },
		Log:       log.New(io.Discard, "", 0),
	}
	return l, f
}

func calls(t *testing.T, f *fake.Fake, prefix string) []string {
	t.Helper()
	var out []string
	for _, c := range f.Calls(t) {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// One tick, end to end: a ready task on the board becomes a worker pane with
// the task's goal already armed, and the dispatcher records the one mapping
// that exists nowhere else until the worker claims.
func TestATickTakesAReadyTaskToADeliveredGoal(t *testing.T) {
	l, f := newLoop(t)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	b := l.bindings[0]
	if b.TaskID != "01AAA" || b.Pane != "wM:p9" || b.Prompts != 1 || !b.PromptedAt.Equal(clock) {
		t.Fatalf("binding: %+v", b)
	}

	if got := calls(t, f, "task list --ready --all-projects"); len(got) != 1 {
		t.Fatalf("ready was asked for %d times", len(got))
	}
	// The board's own goal document never reaches the typed line: the
	// condition is a pointer hdis composes, and the criteria stay on the
	// board for the worker to read with `task get`.
	if got := calls(t, f, "task goal"); len(got) != 0 {
		t.Fatalf("the board's goal document was rendered for the typed line: %v", got)
	}
	start := calls(t, f, "agent start")
	if len(start) != 1 {
		t.Fatalf("agent start ran %d times", len(start))
	}
	for _, want := range []string{"hdis-7", "--kind claude", "--pane wM:p9", spawn.GoalPrefix + spawn.PointerGoal(7)} {
		if !strings.Contains(start[0], want) {
			t.Fatalf("want %q in %q", want, start[0])
		}
	}
	// The worker claims for itself; the dispatcher never claims for it.
	if got := calls(t, f, "task claim"); len(got) != 0 {
		t.Fatalf("the dispatcher claimed on the worker's behalf: %v", got)
	}
}

// The task stays ready until the worker claims it, so the second tick must
// recognise its own binding rather than dispatching the task twice.
func TestASecondTickDoesNotDispatchATaskItAlreadyPrompted(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("split %d panes for one task", len(got))
	}
	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
}

// The dispatcher stops at review: it announces the task once and touches no
// review verb, ever.
func TestReviewIsAnnouncedOnceAndNeverActedOn(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	for i := 0; i < 2; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("announced review %d times: %v", len(got), got)
	}
	for _, verb := range []string{"task approve", "task reject", "note add"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("the dispatcher ran a review verb: %v", got)
		}
	}
	if !l.bindings[0].Notified {
		t.Fatal("the binding does not remember that review was announced")
	}
}

// A board that cannot be reached is a loud failure and an idle tick, never a
// guess at state and never an empty queue.
func TestAnUnreachableBoardFailsTheTickWithoutSpawning(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "htask", `echo "htask: the daemon is not answering" >&2; exit 1`)

	err := l.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the daemon is not answering") {
		t.Fatalf("want the board's own message, got %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("spawned with no board to read: %v", got)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
}

// A worker that never claims is re-prompted once the claim times out, and
// the prompt is a nudge rather than the goal: a slash command that long
// cannot arrive this way.
func TestAnUnclaimedWorkerIsNudgedAfterTheClaimTimeout(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)
	l.Now = func() time.Time { return clock.Add(10 * time.Minute) }

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	prompts := calls(t, f, "agent prompt")
	if len(prompts) != 1 {
		t.Fatalf("prompted %d times: %v", len(prompts), prompts)
	}
	if strings.Contains(prompts[0], "/goal") {
		t.Fatalf("the nudge carries a goal: %q", prompts[0])
	}
	if !strings.Contains(prompts[0], "7") {
		t.Fatalf("the nudge does not name the task: %q", prompts[0])
	}
	if l.bindings[0].Prompts != 2 || !l.bindings[0].PromptedAt.Equal(clock.Add(10*time.Minute)) {
		t.Fatalf("binding: %+v", l.bindings[0])
	}
}

// A pane that is gone drops its binding and nothing else. Releasing the
// lease is the board's own sweep, and a second writer racing it is the bug.
func TestAGonePaneOnlyDropsItsBinding(t *testing.T) {
	l, f := newLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	for _, verb := range []string{"task release", "sweep", "pane close"} {
		if got := calls(t, f, verb); len(got) != 0 {
			t.Fatalf("a gone pane triggered %q: %v", verb, got)
		}
	}
}

// The whole point of keeping a pane the dispatcher could not read: the
// binding survives with it, so when the worker submits, review is still
// announced. Here the reads recover inside the ceiling and the spawn passes
// cleanly — the flaky read costs nothing.
func TestAFlakyReadStillReachesTheReviewNotification(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "readfail", "2") // the dialog read and the first confirm read

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("a read that recovered must not fail the tick: %v", err)
	}
	if len(l.bindings) != 1 || l.bindings[0].Pane != "wM:p9" {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	if got := calls(t, f, "pane close"); len(got) != 0 {
		t.Fatalf("a worker that came back readable was retired: %v", got)
	}

	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("review was announced %d times: %v", len(got), got)
	}
}

// The live failure this fix is for: the confirm could not read the pane at
// all, so the goal's fate is unknown. The pane stays up and the binding with
// it, and the worker that was already claiming goes on to submit and be
// announced. The tick still says out loud what it could not read.
func TestAnUnreadableConfirmKeepsTheBindingSoReviewIsStillAnnounced(t *testing.T) {
	l, f := newLoop(t)
	var logged strings.Builder
	l.Log = log.New(&logged, "", 0)
	f.Write(t, "readfail", "99") // never readable

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "pane close"); len(got) != 0 {
		t.Fatalf("a worker that could not be read was killed: %v", got)
	}
	if len(l.bindings) != 1 || l.bindings[0].TaskID != "01AAA" || l.bindings[0].Pane != "wM:p9" {
		t.Fatalf("the binding did not survive an unreadable confirm: %+v", l.bindings)
	}
	if !strings.Contains(logged.String(), "pane_not_found") {
		t.Fatalf("the failure was not reported: %q", logged.String())
	}

	// The worker was alive all along: it claimed, worked, and submitted.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("review was announced %d times: %v", len(got), got)
	}
}

// panesWith is what `herdr pane list` answers while one worker pane is up.
// A pane herdr has not attached an agent to yet is listed there with
// agent_status "unknown" and no agent name at all, exactly as measured — and
// `agent list` omits such a pane entirely.
func panesWith(status string) string {
	agent := `"agent":"claude",`
	if status == "unknown" {
		agent = ""
	}
	return `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9",` + agent +
		`"agent_status":"` + status + `","focused":false,"revision":1}]}}`
}

// The live repro, in a test. In the codex run one task got FOUR spawn
// attempts across ticks, so three claude workers raced one claim and burned
// quota. The spawn had kept its pane, but herdr had no agent on that pane
// yet — a state `agent list` reports by omitting the pane entirely. Read as
// "the pane is gone", the binding was dropped, the task went back to ready,
// and the next tick spawned it again on top of a live worker. The binding has
// to outlive that window whatever herdr has attached yet.
func TestAKeptPaneBindingSuppressesASecondSpawnOnTheNextTick(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "readfail", "99") // the confirm never reads, so the pane is kept

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(l.bindings) != 1 {
		t.Fatalf("the spawn kept a pane without recording a binding: %+v", l.bindings)
	}
	f.Write(t, "panes.json", panesWith("unknown"))

	for i := 0; i < 3; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i+2, err)
		}
	}
	if got := calls(t, f, "pane split"); len(got) != 1 {
		t.Fatalf("one task got %d spawn attempts across ticks", len(got))
	}
	if len(l.bindings) != 1 || l.bindings[0].Pane != "wM:p9" {
		t.Fatalf("the binding did not survive the ticks: %+v", l.bindings)
	}
}

// A kept pane whose goal registered late needs no second confirm to recover:
// the worker claims, the board says so, and the binding carries on to the
// review notification. Nothing is re-delivered and no pane is retired.
func TestAKeptPaneWhoseGoalRegisteredLateContinuesNormally(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "readfail", "99")

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	// The goal registered a moment after the confirm gave up: the worker
	// claimed, and herdr now has an agent on the pane.
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"doing","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", panesWith("working"))

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	for _, verb := range []string{"pane close", "agent prompt", "pane split"} {
		if got := calls(t, f, verb); verb == "pane split" && len(got) != 1 {
			t.Fatalf("split %d panes for one task", len(got))
		} else if verb != "pane split" && len(got) != 0 {
			t.Fatalf("a late goal triggered %q: %v", verb, got)
		}
	}
	if len(l.bindings) != 1 || l.bindings[0].Prompts != 1 {
		t.Fatalf("binding: %+v", l.bindings)
	}

	f.Write(t, "get.json", `{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", panesWith("idle"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("review was announced %d times: %v", len(got), got)
	}
}

// The other side of keeping a pane: one that reads fine, never shows a goal,
// and leaves its task sitting in todo is given up — but past the larger
// ceiling the prompt ladder makes, never on the tick the confirm ran out.
func TestAKeptPaneStillGoallessPastTheLargerCeilingIsGivenUp(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "screen.txt", "❯ \n  ? for shortcuts\n") // readable, and no goal on it
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"unknown","interactive_ready":false,"focused":false,"launch_pending":true,"revision":1,"screen_detection_skipped":false}}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := calls(t, f, "pane close"); len(got) != 0 {
		t.Fatalf("the pane was retired on the tick the confirm ran out: %v", got)
	}
	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	f.Write(t, "panes.json", panesWith("idle"))

	// ClaimTimeout is five minutes and MaxPrompts is two: one nudge, then
	// the give-up that retires the pane.
	for _, at := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
		l.Now = func() time.Time { return clock.Add(at) }
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick at %s: %v", at, err)
		}
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("nudged %d times before giving up: %v", len(got), got)
	}
	if got := calls(t, f, "pane close"); len(got) != 1 {
		t.Fatalf("a pane that never got a goal was closed %d times", len(got))
	}
	if len(l.bindings) != 0 {
		t.Fatalf("the binding outlived the give-up: %+v", l.bindings)
	}
}

// codexLoop is the same loop with the codex profile, so its spawns write the
// settings file the typed line no longer carries.
func codexLoop(t *testing.T) (*Loop, *fake.Fake, string) {
	t.Helper()
	l, f := newLoop(t)
	cfg, err := config.Parse([]byte(`{"default":"worker","profiles":{"worker":{"provider":"codex"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	l.Config = cfg
	dir := t.TempDir()
	l.Spawn.SettingsDir = dir
	f.Bin(t, "proxenos", `echo '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}'`)
	return l, f, dir
}

func settingsFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read settings dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The settings file belongs to the pane, so the give-up that retires the pane
// is what takes it away. Left behind it is the proxy's auth token sitting in
// a shared temp directory for as long as the machine is up.
func TestGivingUpOnACodexWorkerRemovesItsSettingsFile(t *testing.T) {
	l, f, dir := codexLoop(t)
	f.Write(t, "screen.txt", "❯ \n  ? for shortcuts\n") // readable, and no goal on it
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","agent_status":"unknown","interactive_ready":false,"focused":false,"launch_pending":true,"revision":1,"screen_detection_skipped":false}}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := settingsFiles(t, dir); len(got) != 1 {
		t.Fatalf("a codex spawn wrote %d settings files: %v", len(got), got)
	}
	f.Write(t, "panes.json", panesWith("idle"))

	for _, at := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
		l.Now = func() time.Time { return clock.Add(at) }
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick at %s: %v", at, err)
		}
	}
	if got := calls(t, f, "pane close"); len(got) != 1 {
		t.Fatalf("the pane was closed %d times", len(got))
	}
	if got := settingsFiles(t, dir); len(got) != 0 {
		t.Fatalf("the settings file outlived the give-up: %v", got)
	}
}

// A pane that is already gone has no worker left to read the file either, and
// nothing else in the repo knows where it is.
func TestAGoneCodexPaneTakesItsSettingsFileWithIt(t *testing.T) {
	l, f, dir := codexLoop(t)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := settingsFiles(t, dir); len(got) != 1 {
		t.Fatalf("a codex spawn wrote %d settings files: %v", len(got), got)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	if got := settingsFiles(t, dir); len(got) != 0 {
		t.Fatalf("a gone pane left its settings file behind: %v", got)
	}
}

// The worse half of the same defect, and the one that never recovers: the
// per-binding board read the tick does. A binding for a task filed on another
// project's board could not be read at all, so every tick logged that it was
// holding the binding and the pane was never retired. Here the fake board
// answers NOT_FOUND to any by-id read that is scoped to one project, exactly
// as the real one does, so a lookup that drops --all-projects leaves the
// worker stuck instead of announcing its review.
func TestATickReadsABoundTaskFiledOnAnotherProjectsBoard(t *testing.T) {
	l, f := newLoop(t)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"todo"}],"count":1}`)
	f.Write(t, "get.json", `{"task":{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"todo"},"ready":true,"dependents":[]}`)
	f.Bin(t, "htask", `case "$1 $2" in
"task list") cat "$HDIS_FAKE_DIR/ready.json" ;;
"task get")
  case " $* " in
    *" --all-projects "*) cat "$HDIS_FAKE_DIR/get.json" ;;
    *) echo '{"error":{"code":"NOT_FOUND","message":"no task in /src/p"}}'; exit 3 ;;
  esac ;;
"task goal") cat "$HDIS_FAKE_DIR/goal.txt" ;;
*) echo '{}' ;;
esac`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(l.bindings) != 1 || l.bindings[0].TaskID != "01ZZZ" {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", `{"task":{"id":"01ZZZ","seq":42,"project":"/src/other","title":"elsewhere","status":"review","claimed_by":"agent:wM:p9"},"ready":false,"dependents":[]}`)
	f.Write(t, "panes.json", `{"id":"x","result":{"type":"pane_list","panes":[{"pane_id":"wM:p9","name":"hdis-42","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("the bound task on another board never reached review: %v", got)
	}
	if !l.bindings[0].Notified {
		t.Fatal("the binding does not remember that review was announced")
	}
}

// A worker edits, stages and commits, so it works in a checkout of its own on
// a branch of its own. The shared project directory — the one the operator
// sits in and every other worker would otherwise hold — is never its Cwd.
func TestAWorkerIsSpawnedInItsOwnWorktreeNeverTheProjectDirectory(t *testing.T) {
	l, f := newLoop(t)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	split := splits(t, f)
	if len(split) != 1 {
		t.Fatalf("pane split ran %d times: %v", len(split), split)
	}
	cwd := cwdOf(t, split[0])
	if cwd == "/src/p" {
		t.Fatalf("the worker was put in the shared checkout %s", cwd)
	}
	if !strings.HasPrefix(cwd, l.Worktrees.RootDir()) {
		t.Fatalf("the worker's cwd %s is not under this daemon's worktree root %s", cwd, l.Worktrees.RootDir())
	}
	if len(l.bindings) != 1 {
		t.Fatalf("bindings: %+v", l.bindings)
	}
	b := l.bindings[0]
	if b.Worktree != cwd {
		t.Fatalf("the binding records worktree %q, the worker was put in %q", b.Worktree, cwd)
	}
	if b.Branch != worktree.Branch(7) {
		t.Fatalf("the binding records branch %q, want %q", b.Branch, worktree.Branch(7))
	}
}

// No checkout is no worker. Falling back to the project directory is the
// defect this lane exists to close, so the spawn refuses and the task stays
// ready for a tick that can give it a tree of its own.
func TestAWorkerIsNotSpawnedAtAllWhenItCanBeGivenNoWorktree(t *testing.T) {
	l, f := newLoop(t)
	l.Worktrees = nil

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "pane split"); len(got) != 0 {
		t.Fatalf("a worker came up with nowhere of its own to work: %v", got)
	}
	if len(l.bindings) != 0 {
		t.Fatalf("a binding was written for a worker that never came up: %+v", l.bindings)
	}
}
