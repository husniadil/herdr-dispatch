package spawn

import (
	"context"
	"errors"
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
	h.Bin(t, "codex-cc-proxy", "printf '{\\n  \"env\": {\\n    \"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8787\"\\n  }\\n}\\n'")

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
	if run == nil || run[3] != `eval "$(codex-cc-proxy env)"` {
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
	if got, want := start[sep+2], `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}`; got != want {
		t.Fatalf("settings: got %q, want %q", got, want)
	}
	if strings.ContainsAny(start[sep+2], "\n\r") {
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
	h.Bin(t, "codex-cc-proxy", `echo '{}'`)

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
	h.Bin(t, "codex-cc-proxy", `echo "Error: the daemon is not answering. Start it with 'codex-cc-proxy run'." >&2; exit 1`)

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
	h.Bin(t, "codex-cc-proxy", `echo '{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"}}'`)

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
	h.Bin(t, "codex-cc-proxy", `echo '{}'`)

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
