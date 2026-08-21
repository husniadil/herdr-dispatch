package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	return Request{Name: "hdis-7", BasePane: "wM:p1", Cwd: "/src/p", Profile: p, Goal: "do the thing · Done when: ..."}
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
	if n := count(h.verbs(t), "pane close"); n != 0 {
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
	if n := count(h.verbs(t), "pane close"); n != 1 {
		t.Fatalf("a failed spawn must retire its half-built pane, closed %d times", n)
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
	if n := count(h.verbs(t), "pane close"); n != 0 {
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
	if n := count(h.verbs(t), "pane close"); n != 1 {
		t.Fatalf("a read refusal must retire its pane, closed %d times", n)
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
	if n := count(h.verbs(t), "pane close"); n != 0 {
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
	if n := count(verbs, "pane close"); n != 0 {
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
	if n := count(verbs, "pane close"); n != 1 {
		t.Fatalf("a pane nothing was started in must be retired, closed %d times", n)
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
	if n := count(h.verbs(t), "pane close"); n != 0 {
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
	if n := count(h.verbs(t), "pane close"); n != 1 {
		t.Fatalf("retire must close the pane, closed %d times", n)
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

// The verifier's condition says in one line what it must never do. It is the
// only place in this repo that names approve or reject at all, and it names
// them to forbid them.
func TestTheVerifierGoalForbidsApprovingAndRejecting(t *testing.T) {
	goal := VerifierGoal(23)
	for _, want := range []string{"never run", "task approve", "task reject"} {
		if !strings.Contains(strings.ToLower(goal), want) {
			t.Fatalf("the verifier goal does not carry %q: %s", want, goal)
		}
	}
}

// It is a pointer, like the worker's: it names the task, where the report is
// read from, and how findings leave. None of the criteria travel on it.
func TestTheVerifierGoalPointsAtTheBoardAndAtHmail(t *testing.T) {
	goal := VerifierGoal(23)
	for _, want := range []string{"htask task get 23", "go clean -testcache", "hmail send", "htask task note 23"} {
		if !strings.Contains(goal, want) {
			t.Fatalf("the verifier goal does not carry %q: %s", want, goal)
		}
	}
}

// The verifier's line is typed into its pane exactly as a worker's is, so it
// answers to the same budget.
func TestTheVerifierLineFitsTheTypedBudget(t *testing.T) {
	p := config.Profile{Provider: config.ProviderCodex, Agent: "claude", Model: "sonnet", Effort: "high"}
	args := append(p.AgentArgs(), GoalPrefix+VerifierGoal(23))
	args = append([]string{"--settings", "/var/folders/ab/cdefghij0k1lmnop2qrstuvw0000gn/T/hdis-settings-1234567890.json"}, args...)
	line := TypedLine(args)
	t.Logf("verifier line: %d of %d budgeted, on %q", len(line), TypedLineBudget, line)
	if len(line) > TypedLineBudget {
		t.Fatalf("the verifier line is %d characters and the budget is %d: %s", len(line), TypedLineBudget, line)
	}
}
