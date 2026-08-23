package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/fake"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
)

const (
	trustDialog = "Do you trust the files in this folder?\n  1. Yes, proceed\n  2. No, exit"
	goalActive  = "⎿  Goal set: do the thing · Done when: ...\n  ◎ /goal active"
	goalRefused = "Goal condition is limited to 4000 characters (got 4271)"
	promptBox   = "❯ \n  ? for shortcuts"
)

// The herdr fake dispatches on the verb and reads its canned answers out of
// files, so each case only writes the answers it cares about. A read whose
// answer file has a sibling `.fail` refuses the way the real CLI refuses: a
// JSON error body on stderr and a non-zero exit.
const herdrScript = `case "$1 $2" in
"pane split") cat "$HDIS_FAKE_DIR/split.json" ;;
"tab create") cat "$HDIS_FAKE_DIR/tab.json" ;;
"tab list") cat "$HDIS_FAKE_DIR/tablist.json" ;;
"pane list") cat "$HDIS_FAKE_DIR/panelist.json" ;;
"pane read")
  n=$(cat "$HDIS_FAKE_DIR/readn" 2>/dev/null || echo 0)
  n=$((n+1)); printf %s "$n" > "$HDIS_FAKE_DIR/readn"
  f="$HDIS_FAKE_DIR/read.$n"; [ -f "$f" ] || f="$HDIS_FAKE_DIR/read.last"
  if [ -f "$f.fail" ]; then
    echo '{"error":{"code":"pane_not_found","message":"pane wM:p9 not found"},"id":"cli:pane:read"}' >&2
    exit 1
  fi
  cat "$f" ;;
"agent start") sh "$HDIS_FAKE_DIR/start.sh" ;;
"agent get") cat "$HDIS_FAKE_DIR/agentget.json" ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// unreadable stands where a screen would, for a read the CLI refuses.
const unreadable = "\x00unreadable"

// paneRead is what `herdr pane read` really answers with: the terminal's own
// text, no JSON envelope anywhere. Measured against the real CLI.
func paneRead(text string) string { return text }

func agentJSON(status string) string {
	return `{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"` + status +
		`","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}`
}

// startRegistered is what agent start does when the goal took: the worker
// goes straight to work and never reaches interactive readiness, so the
// command times out. startRefused is the opposite — a rejected goal leaves
// the worker idle at its prompt box and the command succeeds.
const (
	startRegistered = `echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1`
	startRefused    = `echo '{"id":"x","result":{"type":"agent_started","argv":["claude"],"agent":` + `PLACEHOLDER` + `}}'`
)

type harness struct {
	*fake.Fake
	pipe *Pipeline
}

// newHarness wires a pipeline onto fakes, with sleeping stubbed out so a
// bounded poll costs no wall-clock time.
func newHarness(t *testing.T, reads []string, start string) *harness {
	t.Helper()
	f := fake.New(t)
	f.Bin(t, "herdr", herdrScript)
	f.Write(t, "split.json", `{"id":"x","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}`)
	// What `herdr tab create` really answers with: the tab, and the pane
	// that came up in it. Measured against herdr 0.8.2.
	f.Write(t, "tab.json", `{"id":"x","result":{"type":"tab_created","tab":{"tab_id":"wM:t9","workspace_id":"wM","label":"hdis task 7","number":9,"pane_count":1,"agent_status":"unknown","focused":false},"root_pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t9","terminal_id":"x","focused":false,"agent_status":"unknown","revision":0}}}`)
	// The default world is all the operator's: two of their tabs, none of
	// this dispatcher's. A case that wants an hdis tab with room in it
	// writes its own tablist.json.
	f.Write(t, "tablist.json", `{"id":"x","result":{"type":"tab_list","tabs":[`+
		`{"tab_id":"wM:t1","workspace_id":"wM","label":"1","pane_count":1},`+
		`{"tab_id":"w7:t1","workspace_id":"w7","label":"1","pane_count":1}]}}`)
	// The daemon's own pane and the pane a task was filed from, in two
	// different workspaces, so a case can tell which one a spawn followed.
	f.Write(t, "panelist.json", `{"id":"x","result":{"type":"pane_list","panes":[`+
		`{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"working"},`+
		`{"pane_id":"w7:p3","workspace_id":"w7","tab_id":"w7:t1","agent_status":"working"},`+
		`{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t9","agent_status":"unknown"}]}}`)
	f.Write(t, "start.sh", start)
	f.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":`+agentJSON("idle")+`}}`)
	for i, r := range reads {
		name := "read." + string(rune('1'+i))
		if i == len(reads)-1 {
			name = "read.last"
		}
		if r == unreadable {
			f.Write(t, name, "")
			f.Write(t, name+".fail", "")
		} else {
			f.Write(t, name, paneRead(r))
		}
		if i == len(reads)-1 {
			break
		}
	}
	return &harness{Fake: f, pipe: &Pipeline{
		Herdr:          &herdr.Client{},
		Proxy:          &proxy.Client{},
		SettingsDir:    t.TempDir(),
		StartTimeout:   45 * time.Second,
		DialogCeiling:  4 * time.Second,
		ConfirmCeiling: 4 * time.Second,
		Poll:           time.Second,
		Sleep:          func(time.Duration) {},
	}}
}

func (h *harness) verbs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 {
			out = append(out, argv[0]+" "+argv[1])
		}
	}
	return out
}

func count(in []string, want string) int {
	n := 0
	for _, s := range in {
		if s == want {
			n++
		}
	}
	return n
}

func claudeProfile() config.Profile {
	return config.Profile{Provider: config.ProviderClaude, Agent: "claude", Effort: "low"}
}

func req(p config.Profile) Request {
	return Request{Name: "hdis-7", Label: TabLabel(7), BasePane: "wM:p1", Cwd: "/src/p", Profile: p, Goal: "do the thing · Done when: ..."}
}

// The whole point of the pipeline: a worker comes up with its completion
// condition already armed, delivered in the initial argv as a /goal.
func TestTheGoalTravelsInTheInitialArgvAsASlashCommand(t *testing.T) {
	h := newHarness(t, []string{promptBox, goalActive}, startRegistered)

	pane, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if pane != "wM:p9" {
		t.Fatalf("pane: %q", pane)
	}

	var start []string
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "agent" && argv[1] == "start" {
			start = argv
		}
	}
	if start == nil {
		t.Fatal("agent start was never called")
	}
	last := start[len(start)-1]
	if last != "/goal do the thing · Done when: ..." {
		t.Fatalf("the goal is not the last argument: %q", last)
	}
	joined := strings.Join(start, " ")
	for _, want := range []string{"--kind claude", "--pane wM:p9", "-- --agent claude --effort low"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("want %q in %q", want, joined)
		}
	}
	// The claude provider runs no proxy step and answers no dialog.
	if n := count(h.verbs(t), "pane run"); n != 0 {
		t.Fatalf("claude provider ran %d pane commands", n)
	}
	if n := count(h.verbs(t), "pane send-keys"); n != 0 {
		t.Fatalf("sent %d key presses with no dialog on screen", n)
	}
}

// The inverted return, positive half: agent start timing out is what success
// looks like, because a registered goal drives the worker past readiness.
func TestARegisteredGoalAlongsideAStartTimeoutIsSuccess(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("a timeout with the goal registered must be success, got %v", err)
	}
	if n := count(h.verbs(t), "tab close"); n != 0 {
		t.Fatal("a delivered goal must not retire its own pane")
	}
}

// The inverted return, negative half: agent start succeeding is what failure
// looks like, because a refused goal leaves the worker idle at its prompt.
func TestARefusedGoalAlongsideAStartSuccessIsFailure(t *testing.T) {
	h := newHarness(t, []string{goalRefused + "\n" + promptBox}, strings.Replace(startRefused, "PLACEHOLDER", agentJSON("idle"), 1))

	_, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err == nil {
		t.Fatal("a goal that never registered must fail the spawn")
	}
	if !strings.Contains(err.Error(), "limited to 4000 characters") {
		t.Fatalf("the error drops what the pane said: %v", err)
	}
	if n := count(h.verbs(t), "tab close"); n != 1 {
		t.Fatalf("a failed spawn must retire its half-built tab, closed %d times", n)
	}
}

// The trust-folder dialog is answered when it is seen, once.
func TestTheTrustDialogIsAnsweredExactlyOnce(t *testing.T) {
	h := newHarness(t, []string{trustDialog, goalActive}, startRegistered)

	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	keys := 0
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "send-keys" {
			keys++
			if got, want := strings.Join(argv, " "), "pane send-keys wM:p9 enter"; got != want {
				t.Fatalf("keys: got %q, want %q", got, want)
			}
		}
	}
	if keys != 1 {
		t.Fatalf("want exactly one Enter, got %d", keys)
	}
}

// A dialog that never appears is never answered: no blind Enter, and the
// bounded ceiling ends the wait rather than the spawn.
func TestADialogThatNeverAppearsIsNeverAnswered(t *testing.T) {
	h := newHarness(t, []string{promptBox, promptBox, promptBox, goalActive}, startRegistered)

	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "send-keys" {
			t.Fatalf("sent a blind key press: %v", argv)
		}
	}
}

func codexProfile() config.Profile {
	return config.Profile{Provider: config.ProviderCodex, Agent: "claude", Effort: "low"}
}

// The codex provider is the two steps the proxy measurement recommended, in
// order: the environment half through the pane's shell, the settings half
// through the argv, compacted to one line.
func TestCodexRunsTheTwoMeasuredSteps(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", "printf '{\\n  \"env\": {\\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\"\\n  }\\n}\\n'")

	if _, err := h.pipe.Run(context.Background(), req(codexProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}

	var run, start []string
	for _, argv := range h.Argv(t) {
		switch {
		case len(argv) >= 2 && argv[0] == "pane" && argv[1] == "run":
			run = argv
		case len(argv) >= 2 && argv[0] == "agent" && argv[1] == "start":
			start = argv
		}
	}
	if run == nil || run[3] != `eval "$(proxenos env)"` {
		t.Fatalf("environment half: got %v", run)
	}
	if start == nil {
		t.Fatal("agent start was never called")
	}
	sep := -1
	for i, a := range start {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || start[sep+1] != "--settings" {
		t.Fatalf("--settings must lead the agent argv: %v", start)
	}
	// The settings half travels as a file the worker reads, so what the argv
	// carries is a path and what the document has to be is on disk.
	body, err := os.ReadFile(start[sep+2])
	if err != nil {
		t.Fatalf("the worker's settings file: %v", err)
	}
	if got, want := string(body), `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}`; got != want {
		t.Fatalf("settings: got %q, want %q", got, want)
	}
	if strings.ContainsAny(string(body), "\n\r") {
		t.Fatal("the settings document was not compacted")
	}
	// A shell that is already free costs one attempt and no waiting.
	if n := count(h.verbs(t), "agent start"); n != 1 {
		t.Fatalf("a free shell must be started into once, got %d agent starts", n)
	}
}

// Two --settings collide silently in the client, so a profile that carries
// one is refused before any pane exists.
func TestCodexRefusesAProfileThatAlreadyCarriesSettings(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", `echo '{}'`)

	p := codexProfile()
	p.Args = []string{"--settings", "/etc/mine.json"}
	_, err := h.pipe.Run(context.Background(), req(p))
	if err == nil || !strings.Contains(err.Error(), "--settings") {
		t.Fatalf("want a refusal naming --settings, got %v", err)
	}
	if len(h.Argv(t)) != 0 {
		t.Fatalf("nothing should have been spawned, got %v", h.Argv(t))
	}
}

// A down daemon fails at step zero, in the daemon's own words, before a pane
// is split — not thirty seconds later as a startup timeout.
func TestADownProxyDaemonFailsAtStepZero(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", `echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1`)

	_, err := h.pipe.Run(context.Background(), req(codexProfile()))
	if err == nil || !strings.Contains(err.Error(), "the daemon is not answering") {
		t.Fatalf("want the daemon's own message, got %v", err)
	}
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "split" {
			t.Fatal("a pane was split before the proxy answered")
		}
	}
}

// The live failure, in a test: the confirm could not read the pane at all.
// The goal may well have registered — in the live run it had, and the worker
// was already claiming — so the spawn fails loud and the pane stays up.
func TestAnUnreadableConfirmKeepsThePaneAndFailsLoud(t *testing.T) {
	h := newHarness(t, []string{unreadable}, startRegistered)

	pane, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err == nil {
		t.Fatal("a confirm that read nothing must fail loud, not pass silently")
	}
	if !strings.Contains(err.Error(), "pane_not_found") {
		t.Fatalf("the error drops what herdr said: %v", err)
	}
	var keep *KeepPaneError
	if !errors.As(err, &keep) {
		t.Fatalf("an unreadable confirm must be a keep-pane failure, got %T", err)
	}
	if n := count(h.verbs(t), "tab close"); n != 0 {
		t.Fatalf("a worker that could not be read was retired anyway, closed %d times", n)
	}
	if pane != "wM:p9" {
		t.Fatalf("the pane must come back so the dispatcher can hold its binding, got %q", pane)
	}
	// It kept looking rather than giving up on the first refusal.
	if n := count(h.verbs(t), "pane read"); n < 2 {
		t.Fatalf("only %d reads: an unreadable confirm must retry within the ceiling", n)
	}
}

// The contrast, and the reason the rule is not just "never close a pane":
// a confirm that DID read the screen and found a refusal on it knows the
// worker never got a goal, so that half-built pane is retired.
func TestAConfirmThatReadsARefusalRetiresThePane(t *testing.T) {
	h := newHarness(t, []string{goalRefused + "\n" + promptBox}, strings.Replace(startRefused, "PLACEHOLDER", agentJSON("idle"), 1))

	pane, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err == nil {
		t.Fatal("a goal that never registered must fail the spawn")
	}
	var keep *KeepPaneError
	if errors.As(err, &keep) {
		t.Fatal("a refusal that was read is not an unreadable confirm")
	}
	if n := count(h.verbs(t), "tab close"); n != 1 {
		t.Fatalf("a read refusal must retire its tab, closed %d times", n)
	}
	if pane != "" {
		t.Fatalf("a retired pane must not come back for a binding, got %q", pane)
	}
}

// A read that fails and then recovers inside the ceiling is not a failure at
// all: the spawn succeeds, so the binding is recorded and review is still
// announced later.
func TestAFlakyReadRecoversWithinTheCeiling(t *testing.T) {
	h := newHarness(t, []string{unreadable, unreadable, goalActive}, startRegistered)

	pane, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err != nil {
		t.Fatalf("a read that recovered must not fail the spawn: %v", err)
	}
	if pane != "wM:p9" {
		t.Fatalf("pane: %q", pane)
	}
	if n := count(h.verbs(t), "tab close"); n != 0 {
		t.Fatal("a recovered read must not retire its pane")
	}
}

// startBusyThen is what `agent start` really answers while the target pane's
// shell is still running something: an immediate refusal, with nothing typed
// and nothing started. The envelope is verbatim from herdr 0.8.2, measured by
// running `pane run <pane> 'sleep 20'` and starting an agent into it. After n
// of them the shell is free and the given script answers instead.
func startBusyThen(n int, then string) string {
	return `c=$(cat "$HDIS_FAKE_DIR/startn" 2>/dev/null || echo 0)
c=$((c+1)); printf %s "$c" > "$HDIS_FAKE_DIR/startn"
if [ "$c" -le ` + strconv.Itoa(n) + ` ]; then
  echo '{"error":{"code":"agent_pane_busy","message":"agent target pane wM:p9 is not an available shell"},"id":"cli:agent:start"}' >&2
  exit 1
fi
` + then
}

// The live failure on the codex path, in a test: `pane run` returns as soon
// as the environment eval is typed, not when it finishes, so agent start can
// arrive while that eval still owns the shell — and herdr refuses it outright
// with agent_pane_busy. The spawn waits on herdr's own verdict and starts the
// moment the shell is free, never surfacing a busy shell as a failure.
func TestTheCodexSpawnWaitsForItsPanesShellBeforeStartingTheAgent(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startBusyThen(3, startRegistered))
	h.pipe.ShellCeiling = 6 * time.Second // six tries at the harness's one-second poll
	h.Bin(t, "proxenos", `echo '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}'`)

	pane, err := h.pipe.Run(context.Background(), req(codexProfile()))
	if err != nil {
		t.Fatalf("a shell that frees inside the ceiling must not fail the spawn: %v", err)
	}
	if pane != "wM:p9" {
		t.Fatalf("pane: %q", pane)
	}
	verbs := h.verbs(t)
	if n := count(verbs, "agent start"); n != 4 {
		t.Fatalf("want three refusals and then one start, got %d agent starts", n)
	}
	if n := count(verbs, "tab close"); n != 0 {
		t.Fatal("a spawn that waited out a busy shell must not retire its pane")
	}
	// The wait belongs after the environment half, which is what made the
	// shell busy in the first place.
	run, start := -1, -1
	for i, v := range verbs {
		if v == "pane run" && run < 0 {
			run = i
		}
		if v == "agent start" && start < 0 {
			start = i
		}
	}
	if run < 0 || start < 0 || run > start {
		t.Fatalf("the environment half must precede every start attempt: %v", verbs)
	}
}

// A shell that never frees inside the ceiling fails loud, and the pane it
// would have started in is retired: herdr refuses before it types anything,
// so there is no worker in there to lose.
func TestAPaneShellThatNeverFreesFailsLoudAndRetiresThePane(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startBusyThen(99, startRegistered))
	h.pipe.ShellCeiling = 3 * time.Second
	h.Bin(t, "proxenos", `echo '{}'`)

	pane, err := h.pipe.Run(context.Background(), req(codexProfile()))
	if err == nil {
		t.Fatal("a shell that never frees must fail the spawn")
	}
	if !strings.Contains(err.Error(), "agent_pane_busy") {
		t.Fatalf("the error drops herdr's own refusal: %v", err)
	}
	verbs := h.verbs(t)
	if n := count(verbs, "agent start"); n != 3 {
		t.Fatalf("want three bounded tries, got %d agent starts", n)
	}
	if n := count(verbs, "tab close"); n != 1 {
		t.Fatalf("a tab nothing was started in must be retired, closed %d times", n)
	}
	if pane != "" {
		t.Fatalf("a retired pane must not come back for a binding, got %q", pane)
	}
}

// The other half of the live codex failure: the pane read fine and simply had
// no goal on it yet, because the worker was still starting. Retiring it there
// killed a worker that was seconds from registering, and the task went back
// to ready to be spawned again. A worker herdr does not call idle is still
// coming up, so the pane is kept and the next tick decides.
func TestAConfirmThatTimesOutOnAStartingWorkerKeepsThePane(t *testing.T) {
	h := newHarness(t, []string{promptBox}, startRegistered)
	h.Write(t, "agentget.json", `{"id":"x","result":{"type":"agent_info","agent":`+agentJSON("unknown")+`}}`)

	pane, err := h.pipe.Run(context.Background(), req(claudeProfile()))
	if err == nil {
		t.Fatal("a confirm that ran out of ceiling must still say so")
	}
	var keep *KeepPaneError
	if !errors.As(err, &keep) {
		t.Fatalf("a worker that is still coming up must be a keep-pane failure, got %T: %v", err, err)
	}
	if pane != "wM:p9" {
		t.Fatalf("the pane must come back so the dispatcher can hold its binding, got %q", pane)
	}
	if n := count(h.verbs(t), "tab close"); n != 0 {
		t.Fatalf("a worker that was still starting was retired anyway, closed %d times", n)
	}
}

// The confirm ceiling is a knob, not a constant: the codex path's startup is
// what it has to cover, and that is measured rather than assumed.
func TestTheConfirmCeilingIsConfigurableAndDefaulted(t *testing.T) {
	p := &Pipeline{Poll: time.Second}
	if got := p.confirmCeiling(); got != DefaultConfirmCeiling {
		t.Fatalf("an unset ceiling must fall back to the default, got %s", got)
	}
	p.ConfirmCeiling = 7 * time.Second
	if got := p.confirmCeiling(); got != 7*time.Second {
		t.Fatalf("a set ceiling must be honoured, got %s", got)
	}
}

// realProxySettings is the document `proxenos settings` actually
// produced on 2026-08-21, compacted the way the proxy adapter compacts it.
// It is 473 characters, and the budget below is derived from that number, so
// the test carries the real thing rather than a stub that would drift.
const realProxySettings = `{"disableClaudeAiConnectors":true,"env":{"ANTHROPIC_AUTH_TOKEN":"unused","ANTHROPIC_BASE_URL":"http://127.0.0.1:8787","ANTHROPIC_DEFAULT_FABLE_MODEL":"gpt-5.6-sol","ANTHROPIC_DEFAULT_HAIKU_MODEL":"gpt-5.6-luna","ANTHROPIC_DEFAULT_OPUS_MODEL":"gpt-5.6-luna","ANTHROPIC_DEFAULT_SONNET_MODEL":"gpt-5.6-luna","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"258400","CLAUDE_CODE_DISABLE_1M_CONTEXT":"1","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"258400"},"permissions":{"deny":["Skill(claude-api)"]}}`

// agentArgsOf returns the argv herdr forwarded to the worker: everything
// after the `--` separator of the recorded `agent start`.
func agentArgsOf(t *testing.T, h *harness) []string {
	t.Helper()
	for _, argv := range h.Argv(t) {
		if len(argv) < 2 || argv[0] != "agent" || argv[1] != "start" {
			continue
		}
		for i, a := range argv {
			if a == "--" {
				return argv[i+1:]
			}
		}
	}
	t.Fatal("agent start carried no agent argv")
	return nil
}

// The condition hdis arms a worker with is a pointer to the board row, not
// the row itself: the line it travels on is TYPED into a pane, and a long one
// intermittently arrives broken. The pointer has to carry three things — how
// to take the task, where to read what it asks for, and the end state /goal
// judges from the transcript.
func TestTheSpawnConditionIsAShortPointerToTheBoard(t *testing.T) {
	got := PointerGoal(14)
	for _, want := range []string{
		"htask task claim 14",
		"htask task get 14",
		"htask task submit 14",
		"review",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the pointer condition does not name %q: %s", want, got)
		}
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("the condition carries a line break, which herdr refuses outright: %q", got)
	}
	if len(got) > 256 {
		t.Fatalf("the condition is %d characters, which is no longer a pointer: %s", len(got), got)
	}
	t.Logf("pointer condition: %d characters, %q", len(got), got)
}

// The line herdr types into a worker's pane is typed, character by character,
// and a long one intermittently arrives broken. Now that the condition is a
// pointer hdis composes, the WHOLE line is hdis's own choice, so the whole
// line is what the budget bounds.
func TestTheTypedSpawnLineStaysUnderItsBudgetWithACodexProfile(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	// The real temp directory, because the path's own length is what is
	// being measured. A t.TempDir() path carries the test's name and would
	// measure something no spawn ever types.
	h.pipe.SettingsDir = ""
	t.Cleanup(func() { h.pipe.Discard("wM:p9") })
	h.Bin(t, "proxenos", `cat "$HDIS_FAKE_DIR/settings.json"`)
	h.Write(t, "settings.json", realProxySettings)

	p := codexProfile()
	p.Model = "haiku"
	r := req(p)
	r.Goal = PointerGoal(14)
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := agentArgsOf(t, h)
	line := TypedLine(args)
	t.Logf("typed line: %d of %d budgeted, on %q", len(line), TypedLineBudget, line)
	if len(line) > TypedLineBudget {
		t.Fatalf("the typed line is %d characters, over the %d budget:\n%s",
			len(line), TypedLineBudget, line)
	}

	// And the budget bites: the shape the two corrupted live runs typed —
	// the settings document inline and the board's whole goal document
	// behind it — is far past it.
	inline := append([]string{"--settings", realProxySettings}, args[2:len(args)-1]...)
	inline = append(inline, GoalPrefix+strings.Repeat("x", 1300))
	if got := len(TypedLine(inline)); got <= TypedLineBudget {
		t.Fatalf("the corrupted live shape measures %d, which the budget of %d would have allowed",
			got, TypedLineBudget)
	}
}

// Composing the condition is the caller's job; the pipeline's job is to
// deliver it untouched. Nothing here trims, wraps or re-renders it.
func TestThePipelineDeliversTheConditionItWasGivenUnchanged(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", `cat "$HDIS_FAKE_DIR/settings.json"`)
	h.Write(t, "settings.json", realProxySettings)

	r := req(codexProfile())
	r.Goal = PointerGoal(14)
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := agentArgsOf(t, h)
	if got, want := args[len(args)-1], GoalPrefix+r.Goal; got != want {
		t.Fatalf("the condition did not travel whole:\n got %q\nwant %q", got, want)
	}
}

// The settings document leaves the typed line for a file the worker reads.
// The file carries the proxy's auth token and lands in a shared temp
// directory, so nobody else on the machine gets to read it.
func TestTheSettingsDocumentTravelsAsAFileOnlyItsOwnerCanRead(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", `cat "$HDIS_FAKE_DIR/settings.json"`)
	h.Write(t, "settings.json", realProxySettings)

	if _, err := h.pipe.Run(context.Background(), req(codexProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := agentArgsOf(t, h)
	if args[0] != "--settings" {
		t.Fatalf("--settings must lead the agent argv: %v", args)
	}
	path := args[1]
	if !filepath.IsAbs(path) {
		t.Fatalf("--settings must carry an absolute path, got %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the worker's settings file: %v", err)
	}
	if string(body) != realProxySettings {
		t.Fatalf("the file does not carry the proxy's document:\n%s", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != SettingsFileMode {
		t.Fatalf("settings file mode is %04o, want %04o", got, SettingsFileMode)
	}
}

// Every spawn writes its own, so two live workers never share one file and
// retiring one never disarms the other.
func TestEachSpawnWritesItsOwnSettingsFile(t *testing.T) {
	first := newHarness(t, []string{goalActive}, startRegistered)
	first.Bin(t, "proxenos", `echo '{"env":{}}'`)
	if _, err := first.pipe.Run(context.Background(), req(codexProfile())); err != nil {
		t.Fatalf("first run: %v", err)
	}
	a := agentArgsOf(t, first)[1]

	second := newHarness(t, []string{goalActive}, startRegistered)
	second.Bin(t, "proxenos", `echo '{"env":{}}'`)
	if _, err := second.pipe.Run(context.Background(), req(codexProfile())); err != nil {
		t.Fatalf("second run: %v", err)
	}
	b := agentArgsOf(t, second)[1]

	if a == b {
		t.Fatalf("two spawns shared one settings file: %s", a)
	}
}

// Retiring a pane is what ends the worker that was reading the file, so it is
// where the file goes. The same call is what a give-up runs.
func TestRetiringAPaneRemovesItsSettingsFile(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	h.Bin(t, "proxenos", `echo '{"env":{}}'`)

	pane, err := h.pipe.Run(context.Background(), req(codexProfile()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	path := agentArgsOf(t, h)[1]

	if err := h.pipe.Retire(context.Background(), pane); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if n := count(h.verbs(t), "tab close"); n != 1 {
		t.Fatalf("retire must close the tab, closed %d times", n)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the settings file outlived its worker: %v", err)
	}
}

// A spawn that fails in a way it could READ retires its own half-built pane,
// and the file goes with it.
func TestAFailedSpawnRemovesItsSettingsFile(t *testing.T) {
	h := newHarness(t, []string{goalRefused + "\n" + promptBox}, strings.Replace(startRefused, "PLACEHOLDER", agentJSON("idle"), 1))
	h.Bin(t, "proxenos", `echo '{"env":{}}'`)

	if _, err := h.pipe.Run(context.Background(), req(codexProfile())); err == nil {
		t.Fatal("a goal that never registered must fail the spawn")
	}
	path := agentArgsOf(t, h)[1]
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a retired spawn left its settings file behind: %v", err)
	}
}

// The contrast: a pane that is KEPT may hold a worker already at work, and
// Claude Code re-reads its settings file during a session. Removing it there
// would disarm a live worker to tidy up after it.
func TestAKeptPaneKeepsItsSettingsFile(t *testing.T) {
	h := newHarness(t, []string{unreadable}, startRegistered)
	h.Bin(t, "proxenos", `echo '{"env":{}}'`)

	pane, err := h.pipe.Run(context.Background(), req(codexProfile()))
	if err == nil {
		t.Fatal("a confirm that read nothing must fail loud")
	}
	if pane != "wM:p9" {
		t.Fatalf("the pane must come back, got %q", pane)
	}
	path := agentArgsOf(t, h)[1]
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a kept pane's settings file was removed anyway: %v", err)
	}
}

// The claude provider types no settings at all, so it writes no file.
func TestTheClaudeProviderWritesNoSettingsFile(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)

	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, a := range agentArgsOf(t, h) {
		if a == "--settings" {
			t.Fatalf("the claude provider spliced a --settings: %v", agentArgsOf(t, h))
		}
	}
	entries, err := os.ReadDir(h.pipe.SettingsDir)
	if err != nil {
		t.Fatalf("read settings dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the claude provider wrote %d settings files", len(entries))
	}
}

// CRITERION 4. The second condition a submission earns must ask for the
// MECHANICAL thing. What it asks for is pinned here piece by piece, so that
// rewording it into "review your work" fails.
func TestTheSelfReviewConditionAsksForAMutationPerClaim(t *testing.T) {
	goal := SelfReviewCondition(23)
	if missing := mechanicalAsks(goal); len(missing) > 0 {
		t.Fatalf("the self-review condition does not ask for %v: %s", missing, goal)
	}
	if !strings.Contains(goal, "task 23") && !strings.Contains(goal, "Task 23") {
		t.Fatalf("the self-review condition does not name the task: %s", goal)
	}
}

// And the guard is only worth what it catches. A reread request, however
// earnestly worded, is what the lane exists to not be.
func TestARereadRequestIsNotASelfReviewCondition(t *testing.T) {
	for name, text := range map[string]string{
		"review your work": "Task 23 is submitted. Review your work and fix anything wrong before the operator reads it.",
		"reread the diff":  "Task 23 is submitted. Reread the whole diff and check your report against the code.",
		"no failure asked": "Task 23 is submitted. For every guard your report claims, write a COMPILING mutation that removes it, run the tests your report names, and revert each one. Then report which mutations bit and which did not, and whether it is a missing test or bad aim.",
		"no bite report":   "Task 23 is submitted. For every guard your report claims, write a COMPILING mutation that removes it, run the tests your report names, and confirm they FAIL. Revert each one.",
		"no revert":        "Task 23 is submitted. For every guard your report claims, write a COMPILING mutation that removes it, run the tests your report names, and confirm they FAIL. Then report which mutations bit and which did not, and whether it is a missing test or bad aim.",
		"no mutation":      "Task 23 is submitted. For every guard your report claims, run the tests your report names and confirm they FAIL. Then report which checks bit and which did not, and whether it is a missing test or bad aim.",
	} {
		if missing := mechanicalAsks(text); len(missing) == 0 {
			t.Errorf("%q passes as a self-review condition: %s", name, text)
		}
	}
}

// mechanicalAsks names the parts of the second condition that are missing
// from a text. Each is one step of the check the operator has been running by
// hand: a mutation per claim, the tests the report named, the failure
// confirmed, the mutation reverted, and the worker's own reading of every
// mutation that did not bite.
func mechanicalAsks(goal string) []string {
	lower := strings.ToLower(goal)
	var missing []string
	for ask, phrases := range map[string][]string{
		"a mutation per claimed guard":       {"mutation"},
		"every claim in the report":          {"for every guard"},
		"a mutation that compiles":           {"compiling"},
		"the tests the report names":         {"run the tests your report names"},
		"the failure confirmed":              {"confirm they fail"},
		"the mutation reverted":              {"revert"},
		"which mutations bit":                {"which mutations bit and which did not"},
		"a reading of the ones that did not": {"missing test or bad aim"},
	} {
		found := false
		for _, phrase := range phrases {
			if strings.Contains(lower, phrase) {
				found = true
			}
		}
		if !found {
			missing = append(missing, ask)
		}
	}
	sort.Strings(missing)
	return missing
}

// A worker needs an address for the dispatcher that spawned it, and the one
// place it can carry without being typed into a condition is the pane's own
// environment. Every split this pipeline makes publishes it.
func TestTheSplitPublishesTheDispatcherPane(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	split := tabArgv(t, h)
	if !hasPair(split, "--env", DispatcherPaneVar+"=wM:p1") {
		t.Fatalf("the split carries no %s pair: %v", DispatcherPaneVar, split)
	}
}

// And it publishes the DAEMON's base pane, never the worker's own pane or
// anything else: an address that names the wrong pane is worse than none,
// because a reply reaches a stranger.
func TestTheDispatcherPaneEnvNamesTheBasePaneAndNotTheWorkersOwn(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	r := req(claudeProfile())
	r.BasePane = "wM:p4"
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}
	split := tabArgv(t, h)
	for i, a := range split {
		if a == "--env" && i+1 < len(split) && strings.HasPrefix(split[i+1], DispatcherPaneVar+"=") {
			if got, want := strings.TrimPrefix(split[i+1], DispatcherPaneVar+"="), "wM:p4"; got != want {
				t.Fatalf("the dispatcher address is %q, and the daemon's base pane is %q", got, want)
			}
			return
		}
	}
	t.Fatalf("the split carries no %s pair: %v", DispatcherPaneVar, split)
}

// The condition hands the agent the variable rather than a pane id baked into
// its text: the text is composed once per task, and the address is whatever
// the daemon is running on this time. No condition may carry a pane id, and
// the one that names a route to the dispatcher must name it as the variable.
func TestNoConditionWritesAPaneIDIntoItsText(t *testing.T) {
	for name, goal := range map[string]string{"worker": PointerGoal(23), "self-review": SelfReviewCondition(23)} {
		if paneID.MatchString(goal) {
			t.Errorf("the %s condition writes a pane id into its text: %s", name, goal)
		}
	}
	if !strings.Contains(PointerGoal(23), "$"+DispatcherPaneVar) {
		t.Errorf("the worker condition does not name $%s: %s", DispatcherPaneVar, PointerGoal(23))
	}
}

// paneID is a herdr pane id — a workspace, a colon, a p and a number — which
// is exactly what neither condition may carry.
var paneID = regexp.MustCompile(`\bw[A-Za-z0-9]+:p[0-9]+\b`)

// tabArgv is the argv the fake herdr recorded for the one tab create.
func tabArgv(t *testing.T, h *harness) []string {
	t.Helper()
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "tab" && argv[1] == "create" {
			return argv
		}
	}
	t.Fatal("no tab was created")
	return nil
}

func hasPair(argv []string, flag, value string) bool {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}

// A worker is short-lived and disposable, so its pane is launched asking the
// client for the short prompt-cache TTL. The operator's own session is not
// touched by this, which is the whole reason the variable lives on the split.
func TestTheSplitAsksForTheShortPromptCache(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	split := tabArgv(t, h)
	if !hasPair(split, "--env", ShortPromptCacheVar+"=1") {
		t.Fatalf("the split carries no %s pair: %v", ShortPromptCacheVar, split)
	}
}

// A report belongs to whoever wanted the work. When the board says which
// pane a task came from, that pane is the address the worker is handed —
// not the daemon's, which is only the pane this binary happens to run on.
func TestTheDispatcherAddressIsTheTasksPaneOfOrigin(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	r := req(claudeProfile())
	r.BasePane = "wM:p4"
	r.OriginPane = "wZ:p2"
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}
	split := tabArgv(t, h)
	if !hasPair(split, "--env", DispatcherPaneVar+"=wZ:p2") {
		t.Fatalf("the split does not address the task's pane of origin: %v", split)
	}
	if hasPair(split, "--env", DispatcherPaneVar+"=wM:p4") {
		t.Fatalf("the split still addresses the daemon: %v", split)
	}
}

// A task an operator filed at a terminal has no pane of origin, and there is
// nothing better to answer to than the daemon that dispatched it.
func TestTheDispatcherAddressFallsBackToTheBasePaneWithNoOrigin(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	r := req(claudeProfile())
	r.BasePane = "wM:p4"
	r.OriginPane = ""
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}
	split := tabArgv(t, h)
	if !hasPair(split, "--env", DispatcherPaneVar+"=wM:p4") {
		t.Fatalf("the split does not fall back to the daemon's pane: %v", split)
	}
}

// CRITERION 2. A worker comes up in a TAB of its own, and the operator's tab
// is never split to make room for it.
//
// This is the placement itself, pinned at the one call that makes it: the
// argv herdr is handed. Reverting the pipeline to `pane split --pane
// <base>` fails here, because no `tab create` is recorded at all.
//
// The three flags are each load bearing. --workspace is where the tab lands,
// --label is the ownership evidence that outlives the agent name, and --env
// is how the report address and the cache TTL reach a worker without being
// typed into its condition.
func TestAWorkerComesUpInItsOwnTabAndNeverSplitsTheOperatorsTab(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	r := req(claudeProfile())
	r.BasePane = "wM:p1"
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}

	if n := count(h.verbs(t), "pane split"); n != 0 {
		t.Errorf("a worker was placed by splitting a live tab %d time(s); the whole point of a tab is that no existing pane narrows", n)
	}
	argv := tabArgv(t, h)
	if !hasPair(argv, "--label", TabLabel(7)) {
		t.Errorf("the tab carries no --label %q, and the label is what an operator finds the work by and what keeps their own tabs from ever being closed here: %v", TabLabel(7), argv)
	}
	if !hasPair(argv, "--workspace", "wM") {
		t.Errorf("the tab names no --workspace, so herdr places it wherever it likes: %v", argv)
	}
	if !hasPair(argv, "--cwd", r.Cwd) {
		t.Errorf("the tab does not open in the worker's checkout %q: %v", r.Cwd, argv)
	}
	var envs int
	for _, a := range argv {
		if a == "--env" {
			envs++
		}
	}
	if envs < 2 {
		t.Errorf("the tab carries %d --env flags, and the report address and the cache TTL both travel that way: %v", envs, argv)
	}
}

// CRITERION 3. The workspace is the one the task was FILED from when the
// board named a pane and that pane is alive, and the daemon's own otherwise.
//
// A dead origin pane FALLS BACK. A task filed from a window the operator has
// since closed is an ordinary task, and refusing to spawn for it would
// strand real work on nothing worse than a closed window.
func TestTheTabOpensInTheOriginsWorkspaceAndFallsBackWhenThatPaneIsGone(t *testing.T) {
	for _, c := range []struct {
		name   string
		origin string
		want   string
	}{
		// w7:p3 is alive in the fake's pane list, in a workspace of its own.
		{"a live origin pane is followed", "w7:p3", "w7"},
		// wZ:p2 is in nobody's pane list: the window it was filed from is
		// gone, and the daemon's own workspace is what is left.
		{"a dead origin pane falls back", "wZ:p2", "wM"},
		{"a task nothing with a pane filed", "", "wM"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			r := req(claudeProfile())
			r.BasePane = "wM:p1"
			r.OriginPane = c.origin
			if _, err := h.pipe.Run(context.Background(), r); err != nil {
				t.Fatalf("run: %v", err)
			}
			argv := tabArgv(t, h)
			if !hasPair(argv, "--workspace", c.want) {
				t.Fatalf("the tab was not opened in workspace %q: %v", c.want, argv)
			}
		})
	}
}

// CRITERION 5. A tab is closed only if its LABEL says this dispatcher opened
// it. Reaching retire with a pane is not a licence over whatever tab happens
// to be holding that pane.
//
// The daemon that opened the tab is gone here — a fresh pipeline, which is
// exactly the restart case — so the only thing left to ask is herdr, and the
// only thing herdr has to answer with is the label. An operator's tab is
// closed as a PANE, which retires the worker and leaves the operator's window
// exactly as they arranged it.
func TestRetireClosesOnlyATabThisDispatcherLabelled(t *testing.T) {
	for _, c := range []struct {
		name       string
		tabs       string
		wantTab    int
		wantPane   int
		wantClosed string
	}{
		{
			name:       "a tab this dispatcher opened",
			tabs:       `{"tab_id":"wM:t9","workspace_id":"wM","label":"hdis task 7"}`,
			wantTab:    1,
			wantPane:   0,
			wantClosed: "wM:t9",
		},
		{
			name:       "a tab the operator made",
			tabs:       `{"tab_id":"wM:t9","workspace_id":"wM","label":"my notes"}`,
			wantTab:    0,
			wantPane:   1,
			wantClosed: "wM:p9",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			h.Write(t, "tablist.json", `{"id":"x","result":{"type":"tab_list","tabs":[`+c.tabs+`]}}`)
			if err := h.pipe.Retire(context.Background(), "wM:p9"); err != nil {
				t.Fatalf("retire: %v", err)
			}
			verbs := h.verbs(t)
			if n := count(verbs, "tab close"); n != c.wantTab {
				t.Errorf("tab close ran %d time(s), want %d", n, c.wantTab)
			}
			if n := count(verbs, "pane close"); n != c.wantPane {
				t.Errorf("pane close ran %d time(s), want %d", n, c.wantPane)
			}
			var closed string
			for _, argv := range h.Argv(t) {
				if len(argv) >= 3 && argv[1] == "close" {
					closed = argv[2]
				}
			}
			if closed != c.wantClosed {
				t.Errorf("closed %q, want %q", closed, c.wantClosed)
			}
		})
	}
}

// ownTab is a tab list holding one of this dispatcher's own tabs with the
// given panes already in it, plus an operator tab that must never be chosen.
func ownTab(t *testing.T, h *harness, panes ...string) {
	t.Helper()
	rows := []string{`{"pane_id":"wM:p1","workspace_id":"wM","tab_id":"wM:t1","agent_status":"working"}`}
	for _, p := range panes {
		rows = append(rows, `{"pane_id":"`+p+`","workspace_id":"wM","tab_id":"wM:t5","agent_status":"working"}`)
	}
	h.Write(t, "panelist.json", `{"id":"x","result":{"type":"pane_list","panes":[`+strings.Join(rows, ",")+`]}}`)
	h.Write(t, "tablist.json", `{"id":"x","result":{"type":"tab_list","tabs":[`+
		`{"tab_id":"wM:t1","workspace_id":"wM","label":"1","pane_count":1},`+
		`{"tab_id":"wM:t5","workspace_id":"wM","label":"hdis task 7","pane_count":`+
		strconv.Itoa(len(panes))+`}]}}`)
}

// A tab already holding the cap does not take another worker. The next one
// opens a tab of its own, in the same workspace, rather than narrowing panes
// past the width their detection text still reads at.
func TestAFullTabOverflowsIntoAnotherTabRatherThanNarrowingPastTheCap(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	full := make([]string, config.DefaultMaxPanesPerTab)
	for i := range full {
		full[i] = "wM:p" + strconv.Itoa(i+9)
	}
	ownTab(t, h, full...)

	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := count(h.verbs(t), "pane split"); n != 0 {
		t.Errorf("a tab already holding %d panes was split %d more time(s); past the cap a pane stops being readable",
			config.DefaultMaxPanesPerTab, n)
	}
	argv := tabArgv(t, h)
	if !hasPair(argv, "--workspace", "wM") {
		t.Errorf("the overflow tab left the workspace the work is in: %v", argv)
	}
}

// A tab is given back only when its LAST worker leaves. Retiring one of two
// workers sharing a tab closes that worker's pane and leaves the tab, because
// the other worker is still in it.
func TestATabIsClosedOnlyWhenItsLastWorkerLeaves(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	ownTab(t, h, "wM:p9", "wM:pA")

	if err := h.pipe.Retire(context.Background(), "wM:p9"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	verbs := h.verbs(t)
	if n := count(verbs, "tab close"); n != 0 {
		t.Errorf("the tab was closed %d time(s) while another worker was still in it", n)
	}
	if n := count(verbs, "pane close"); n != 1 {
		t.Errorf("the retiring worker's pane was closed %d time(s), want 1", n)
	}
}

// splitArgvOf is the argv of the one recorded `pane split`.
func splitArgvOf(t *testing.T, h *harness) []string {
	t.Helper()
	for _, argv := range h.Argv(t) {
		if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "split" {
			return argv
		}
	}
	t.Fatal("no pane was split into the tab that had room")
	return nil
}

// A tab belongs to ONE task. A tab this dispatcher opened for a different
// task is not a candidate however much room it has: its label is the
// operator's signpost, and a tab holding two tasks names only one of them.
//
// Dropping the task comparison and keeping only the hdis prefix fails this:
// the split would be taken and no tab created.
func TestAWorkerJoinsOnlyTheTabOpenedForItsOwnTask(t *testing.T) {
	h := newHarness(t, []string{goalActive}, startRegistered)
	ownTab(t, h, "wM:p9")
	h.Write(t, "tablist.json", `{"id":"x","result":{"type":"tab_list","tabs":[`+
		`{"tab_id":"wM:t1","workspace_id":"wM","label":"1","pane_count":1},`+
		`{"tab_id":"wM:t5","workspace_id":"wM","label":"`+TabLabel(3)+`","pane_count":1}]}}`)

	// The request is task 7's, and the only tab with room is task 3's.
	if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := count(h.verbs(t), "pane split"); n != 0 {
		t.Errorf("task 7's worker was split into task 3's tab %d time(s); a tab holds one task", n)
	}
	argv := tabArgv(t, h)
	if !hasPair(argv, "--label", TabLabel(7)) {
		t.Errorf("no tab was opened for task 7's own work: %v", argv)
	}
}

// CRITERION 4. Four panes in one tab are a 2x2 grid, and the reason is the
// split TARGET rather than the direction alone: the third goes DOWN off the
// FIRST, not off the last pane in the tab.
//
// Splitting the third off the last pane instead fails this by name.
func TestFourPanesInOneTabAreATwoByTwoGrid(t *testing.T) {
	for _, tc := range []struct {
		name      string
		panes     []string
		target    string
		direction string
	}{
		{"the second splits right off the first", []string{"wM:p9"}, "wM:p9", "right"},
		{"the third splits down off the FIRST", []string{"wM:p9", "wM:pA"}, "wM:p9", "down"},
		{"the fourth splits down off the second", []string{"wM:p9", "wM:pA", "wM:pB"}, "wM:pA", "down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			ownTab(t, h, tc.panes...)
			if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
				t.Fatalf("run: %v", err)
			}
			if n := count(h.verbs(t), "tab create"); n != 0 {
				t.Fatalf("this task's own tab had room and %d new one(s) opened instead", n)
			}
			argv := splitArgvOf(t, h)
			if !hasPair(argv, "--ratio", "0.5") {
				t.Errorf("the split passed no --ratio, so herdr places the pane wherever it likes: %v", argv)
			}
			if !hasPair(argv, "--pane", tc.target) {
				t.Errorf("the split was taken off the wrong pane, want %q: %v", tc.target, argv)
			}
			if !hasPair(argv, "--direction", tc.direction) {
				t.Errorf("the split went the wrong way, want %q: %v", tc.direction, argv)
			}
		})
	}
}

// toolchainCommands finds every place a condition names a build tool as a
// command to run. hdis takes ready tasks off every project's board on the
// machine, and nothing about a board says which language the project is in,
// so this binary names no toolchain at all: it says what it wants and lets
// the project's own CLAUDE.md say which command delivers it. That is the
// same rule the worker's condition already follows.
//
// What is matched is a COMMAND, not a word. Four of these tools are also
// ordinary English — "go through the diff", "just read the report", "make one
// mutation", "cargo cult" — so naming one of them is only a finding when a
// subcommand or a flag follows it, which is what an instruction to run a
// toolchain looks like and what prose is not. The rest are not English words,
// so the bare name is enough and the list can grow without costing the author
// of the condition a word. "make one COMPILING mutation" now passes the guard;
// the verifier's condition keeps "write" anyway, because the sentence sits two
// clauses after "run the gate" and "make" there reads as a build step rather
// than as authorship. The word is a choice now, not a concession.
func toolchainCommands(goal string) []string {
	// A tool whose name is also an English word: reported only as a command.
	ambiguous := map[string][]string{
		"go":    {"clean", "test", "build", "vet", "run", "mod", "generate", "install", "fmt", "get"},
		"make":  {"test", "test-full", "build", "install", "check", "all", "lint", "gate", "fmt", "ci"},
		"just":  {"test", "gate", "build", "check", "lint", "ci", "fmt", "all"},
		"cargo": {"test", "build", "check", "clippy", "run", "fmt", "nextest", "bench"},
	}
	// A tool whose name is nobody's English: the bare name is the finding.
	plain := []string{
		"npm", "pnpm", "yarn", "bun", "gradle", "gradlew", "mvn", "bazel",
		"uv", "dotnet", "poetry", "pip", "cmake", "ninja", "sbt", "tox",
		"pytest", "rake", "composer", "swift", "zig",
	}

	var found []string
	for _, tool := range sortedKeys(ambiguous) {
		// The tool name, then either a flag or one of its own subcommands.
		next := `(-{1,2}[A-Za-z0-9]` + `|` + strings.Join(ambiguous[tool], "|") + `)\b`
		if regexp.MustCompile(`(?i)\b` + tool + `\s+` + next).MatchString(goal) {
			found = append(found, tool)
		}
	}
	for _, tool := range plain {
		if regexp.MustCompile(`(?i)\b` + tool + `\b`).MatchString(goal) {
			found = append(found, tool)
		}
	}
	return found
}

// sortedKeys keeps the guard's finding list stable across runs, so a failure
// message names the same tools in the same order every time.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// An agent on a Rust board was once told to run `go clean -testcache` and
// reached the right outcome only by ignoring the instruction. No condition
// this binary composes may put an agent in that position: hdis takes ready
// tasks off every board on the machine, and nothing about a board says which
// toolchain the project has. The self-review condition names the tests the
// REPORT names, which is the project's own vocabulary rather than ours.
func TestTheSelfReviewConditionNamesNoToolchainCommand(t *testing.T) {
	goal := SelfReviewCondition(23)
	if got := toolchainCommands(goal); len(got) > 0 {
		t.Fatalf("the self-review condition names %v, a toolchain no board promises: %s", got, goal)
	}
	if !strings.Contains(goal, "run the tests your report names") {
		t.Fatalf("the self-review condition does not send the worker to the tests its report named: %s", goal)
	}
}

// And the guard above is only worth what it catches. Put any one of those
// toolchains back into the condition as a literal and it has to fail.
func TestTheToolchainGuardCatchesEachToolchainPutBack(t *testing.T) {
	for _, mutation := range []string{
		"go clean -testcache then run the tests your report names",
		"cargo test after the tests your report names",
		"npm ci then run the tests your report names",
		"make test-full, and the tests your report names",
		"just gate, and the tests your report names",
		"gradle build then the tests your report names",
		"mvn verify, and the tests your report names",
	} {
		mutant := strings.Replace(SelfReviewCondition(23), "run the tests your report names", mutation, 1)
		if got := toolchainCommands(mutant); len(got) == 0 {
			t.Errorf("%q put back into the condition is not caught: %s", mutation, mutant)
		}
	}
}

// The other half of the guard's worth: it must not fire on the same words
// used as English. A condition is prose, and prose says "go through the diff"
// and "just read the report" without naming a toolchain. A guard that bans
// the words rather than the commands makes a test the author of the prose.
func TestTheToolchainGuardDoesNotFireOnOrdinaryEnglish(t *testing.T) {
	for _, phrase := range []string{
		"go through the diff",
		"just read the report",
		"make one COMPILING mutation",
		"the run has to go green",
		"cargo cult the previous fix",
	} {
		mutant := strings.Replace(SelfReviewCondition(23), "run the tests your report names", phrase, 1)
		if got := toolchainCommands(mutant); len(got) > 0 {
			t.Errorf("%q is ordinary English and the guard reports %v: %s", phrase, got, mutant)
		}
	}
}

// The rule task 46 chose was that hdis names no toolchain command at all.
// Every condition this binary hands an agent is covered by it.
func TestNoDispatchedConditionNamesAToolchainCommand(t *testing.T) {
	for name, goal := range map[string]string{"worker": PointerGoal(23), "self-review": SelfReviewCondition(23)} {
		if got := toolchainCommands(goal); len(got) > 0 {
			t.Errorf("the %s condition names %v, a toolchain no board promises: %s", name, got, goal)
		}
	}
}

// recordedTrustDialog2_1_239 is what claude 2.1.239 actually renders in a
// fresh untrusted directory, read back on 2026-08-23 through the exact call
// the pipeline makes — `herdr pane read --source detection` — in a 173-column
// pane under herdr 0.8.2. It is quoted rather than paraphrased because a
// marker measured against a paraphrase is how this detector broke.
const recordedTrustDialog2_1_239 = ` Accessing workspace:

 /private/var/tmp/hdis-trust49/x5312

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review
 what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel`

// recordedTrustDialogOlder is the wording claude rendered before 2.1.239, the
// one the single marker was written for. An operator still on that build must
// not be broken by the fix, so it stays in the set.
const recordedTrustDialogOlder = "Do you trust the files in this folder?\n" +
	"/private/var/tmp/hdis-trust49/x5312\n" +
	"Claude Code may read, edit, and execute files here.\n" +
	"  1. Yes, proceed\n  2. No, exit"

// Both wordings are answered: the one claude renders today, and the one it
// rendered before. Each drives the whole pipeline, so what is proved is the
// Enter, not just a substring.
func TestTheTrustDialogIsAnsweredInTodaysWordingAndTheOlderOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
	}{
		{"claude 2.1.239", recordedTrustDialog2_1_239},
		{"the older wording", recordedTrustDialogOlder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, []string{tc.screen, goalActive}, startRegistered)

			if _, err := h.pipe.Run(context.Background(), req(claudeProfile())); err != nil {
				t.Fatalf("run: %v", err)
			}
			keys := 0
			for _, argv := range h.Argv(t) {
				if len(argv) >= 2 && argv[0] == "pane" && argv[1] == "send-keys" {
					keys++
					if got, want := strings.Join(argv, " "), "pane send-keys wM:p9 enter"; got != want {
						t.Fatalf("keys: got %q, want %q", got, want)
					}
				}
			}
			if keys != 1 {
				t.Fatalf("want exactly one Enter for this dialog, got %d", keys)
			}
		})
	}
}

// The row floor is the column floor's counterpart, and it needs the same
// protection for a reason this repo has already been bitten by: a constant
// measured against a marker set outlives the set that was measured. The
// column floor survived TrustDialogMarkers changing because it is DERIVED
// from the set; the row floor cannot be derived, because what a marker costs
// in rows is where it sits in the rendered block rather than how long it is.
// So the coupling is pinned instead.
//
// Every marker the dispatcher matches on must carry a measured row cost in
// config.MarkerRows, and each read costs its CHEAPEST marker because a read
// matches an OR. The floor is the tallest of the reads. Adding, removing or
// rewording a marker here fails this test until the row cost is measured too.
func TestTheReadableRowFloorIsDerivedFromTheMarkerSets(t *testing.T) {
	reads := map[string][]string{
		"the trust-dialog read": TrustDialogMarkers,
		"the goal read":         GoalMarkers,
	}

	for name, markers := range reads {
		for _, m := range markers {
			if _, ok := config.MarkerRows[m]; !ok {
				t.Fatalf("%s matches on %q, which has no measured row cost. "+
					"config.MeasuredReadableRows is derived from config.MarkerRows, "+
					"so a new marker needs a height measured for it before it ships",
					name, m)
			}
		}
	}

	inUse := map[string]bool{}
	for _, markers := range reads {
		for _, m := range markers {
			inUse[m] = true
		}
	}
	for m := range config.MarkerRows {
		if !inUse[m] {
			t.Errorf("config.MarkerRows carries a row cost for %q, which nothing matches on any more", m)
		}
	}

	floor, costliest := 0, ""
	for name, markers := range reads {
		rows := config.ReadableRowsFor(markers)
		if rows == config.RowsNotDependable {
			t.Fatalf("%s has no marker with a dependable row cost, so no pane height can be trusted to satisfy it", name)
		}
		if rows > floor {
			floor, costliest = rows, name
		}
	}
	if config.MeasuredReadableRows != floor {
		t.Fatalf("the floor must follow the marker sets: %s is the tallest read at %d rows, "+
			"but config.MeasuredReadableRows is %d",
			costliest, floor, config.MeasuredReadableRows)
	}
}

// The readable-column floor is derived from the markers, never restated
// beside them. Claude renders its body at the pane's columns minus three and
// word-wraps, so the longest phrase the detector matches needs its own length
// plus three columns to land on one line, and one that wraps never matches.
// Change the marker set and this test moves the floor with it.
func TestTheReadableColumnFloorIsDerivedFromTheLongestMarker(t *testing.T) {
	const inset = 3

	longest, phrase := 0, ""
	for _, m := range append(append([]string{}, TrustDialogMarkers...), GoalMarkers...) {
		if len(m) > longest {
			longest, phrase = len(m), m
		}
	}
	if want := longest + inset; config.MeasuredReadableColumns != want {
		t.Fatalf("the floor must follow the longest marker %q (%d chars + %d inset = %d), "+
			"but config.MeasuredReadableColumns is %d",
			phrase, longest, inset, want, config.MeasuredReadableColumns)
	}
}

// paneList writes a `herdr pane list` answer built from rows of
// pane id, workspace, tab and cwd, so a case can say exactly which desks are
// alive and what repository each is sitting in.
func paneList(t *testing.T, h *harness, rows ...[4]string) {
	t.Helper()
	var out []string
	for _, r := range rows {
		out = append(out, `{"pane_id":"`+r[0]+`","workspace_id":"`+r[1]+`","tab_id":"`+r[2]+
			`","cwd":"`+r[3]+`","agent_status":"working"}`)
	}
	h.Write(t, "panelist.json", `{"id":"x","result":{"type":"pane_list","panes":[`+strings.Join(out, ",")+`]}}`)
}

// addressOf is the report address the spawn published, read off the argv
// herdr was handed.
func addressOf(t *testing.T, h *harness) string {
	t.Helper()
	argv := tabArgv(t, h)
	for i, a := range argv {
		if a == "--env" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], DispatcherPaneVar+"=") {
			return strings.TrimPrefix(argv[i+1], DispatcherPaneVar+"=")
		}
	}
	t.Fatalf("the tab carries no %s pair: %v", DispatcherPaneVar, argv)
	return ""
}

// CRITERION 2. The middle rung: with no pane of origin on the board, the
// report is owed at a LIVE pane whose cwd resolves to the task's project,
// and only at the daemon's base pane when there is no such desk.
//
// Deleting the middle rung fails the first case: the address falls all the
// way back to wM:p1, the operator who happened to start the daemon.
func TestTheAddressIsALivePaneInTheTasksProjectBeforeTheDaemonsOwn(t *testing.T) {
	const project = "/Users/husni/github.com/husniadil/codex-cc-proxy"
	for _, c := range []struct {
		name   string
		origin string
		rows   [][4]string
		want   string
	}{
		{
			name: "a live pane in the project is the desk",
			rows: [][4]string{
				{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				{"w15:p1", "w15", "w15:t1", project},
			},
			want: "w15:p1",
		},
		{
			name: "a subdirectory of the project is still the project",
			rows: [][4]string{
				{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				{"w15:p1", "w15", "w15:t1", project + "/internal/api"},
			},
			want: "w15:p1",
		},
		{
			name: "nothing live in the project falls back to the daemon",
			rows: [][4]string{
				{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				{"w15:p1", "w15", "w15:t1", "/Users/husni/github.com/husniadil/something-else"},
			},
			want: "wM:p1",
		},
		{
			// A near-miss neighbour is a different repository. Matching on
			// the raw string prefix would take it.
			name: "a sibling whose name merely starts the same is not the project",
			rows: [][4]string{
				{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				{"w15:p1", "w15", "w15:t1", project + "-fork"},
			},
			want: "wM:p1",
		},
		{
			// The board naming a pane still outranks the cwd evidence: the
			// desk that filed the task is the desk that asked for it.
			name:   "a named pane of origin still wins",
			origin: "wZ:p2",
			rows: [][4]string{
				{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				{"w15:p1", "w15", "w15:t1", project},
			},
			want: "wZ:p2",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			paneList(t, h, c.rows...)
			r := req(claudeProfile())
			r.BasePane = "wM:p1"
			r.OriginPane = c.origin
			r.Project = project
			if _, err := h.pipe.Run(context.Background(), r); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := addressOf(t, h); got != c.want {
				t.Fatalf("the report was addressed at %q, want %q", got, c.want)
			}
		})
	}
}

// CRITERION 3. A pane sitting in one of THIS daemon's own checkouts is never
// the desk. Task 40 puts every worker in a worktree of its task's own
// project, so without this bound the first worker for a project becomes the
// desk and every later report for that project is delivered to a worker
// rather than to a human.
//
// The checkout root is HDIS_STATE_DIR/worktrees, and HDIS_STATE_DIR is the
// operator's to point anywhere — including inside a project, which is the
// case this pins. With the default root, outside every project, the project
// test already excludes our checkouts and the bound is what keeps that true
// when the root moves.
func TestAPaneInThisDaemonsOwnCheckoutsIsNeverTheAddress(t *testing.T) {
	const project = "/Users/husni/github.com/husniadil/codex-cc-proxy"
	for _, c := range []struct {
		name  string
		trees string
	}{
		{"a state dir relocated inside the project", project + "/.hdis/worktrees"},
		{"the default state dir, outside every project", "/Users/husni/.local/state/hdis/worktrees"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			h.pipe.OwnTrees = c.trees
			paneList(t, h,
				[4]string{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
				// A worker this daemon opened for this very project.
				[4]string{"wM:p4N", "wM", "wM:t4", c.trees + "/hdis-work-22-81789488"},
			)
			r := req(claudeProfile())
			r.BasePane = "wM:p1"
			r.Project = project
			if _, err := h.pipe.Run(context.Background(), r); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := addressOf(t, h); got != "wM:p1" {
				t.Fatalf("the report was addressed at %q, and a worker of this daemon's own is never the desk", got)
			}
		})
	}
}

// CRITERION 4. Two live panes in the project is a rule and not a coin toss:
// the LOWEST pane id wins. It is the one answer that is stable across ticks,
// so every report for a project lands at the same desk; most recently active
// is a guess about which human is watching, and iterating a map is no rule at
// all. The order the panes arrive in does not change the answer.
func TestTheDeskAmongTwoLivePanesInTheProjectIsTheLowestPaneID(t *testing.T) {
	const project = "/Users/husni/github.com/husniadil/codex-cc-proxy"
	for _, c := range []struct {
		name string
		rows [][4]string
	}{
		{"listed low first", [][4]string{
			{"w15:p1", "w15", "w15:t1", project},
			{"w15:p2", "w15", "w15:t2", project},
		}},
		{"listed high first", [][4]string{
			{"w15:p2", "w15", "w15:t2", project},
			{"w15:p1", "w15", "w15:t1", project},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, []string{goalActive}, startRegistered)
			paneList(t, h, append(c.rows, [4]string{"wM:p1", "wM", "wM:t1", "/elsewhere"})...)
			r := req(claudeProfile())
			r.BasePane = "wM:p1"
			r.Project = project
			if _, err := h.pipe.Run(context.Background(), r); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := addressOf(t, h); got != "w15:p1" {
				t.Fatalf("the desk was %q, want the lowest pane id w15:p1", got)
			}
		})
	}
}

// ONE RESOLVER. The workspace a worker's tab opens in and the pane its report
// is owed at are the same question — which desk owns this work — and are
// answered once. A tab for a task filed at a terminal opens beside the
// session already working that repository, not beside the daemon.
func TestTheTabOpensInTheWorkspaceOfTheDeskTheReportIsAddressedTo(t *testing.T) {
	const project = "/Users/husni/github.com/husniadil/codex-cc-proxy"
	h := newHarness(t, []string{goalActive}, startRegistered)
	paneList(t, h,
		[4]string{"wM:p1", "wM", "wM:t1", "/Users/husni/github.com/husniadil/herdr-dispatch"},
		[4]string{"w15:p1", "w15", "w15:t1", project},
	)
	r := req(claudeProfile())
	r.BasePane = "wM:p1"
	r.Project = project
	if _, err := h.pipe.Run(context.Background(), r); err != nil {
		t.Fatalf("run: %v", err)
	}
	argv := tabArgv(t, h)
	if !hasPair(argv, "--workspace", "w15") {
		t.Fatalf("the tab did not open in the desk's workspace w15: %v", argv)
	}
	if got := addressOf(t, h); got != "w15:p1" {
		t.Fatalf("the report address %q and the workspace came from different answers", got)
	}
}
