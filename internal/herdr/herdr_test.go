package herdr

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/fake"
)

func client(t *testing.T) (*Client, *fake.Fake) {
	t.Helper()
	return &Client{}, fake.New(t)
}

// A worker pane is a split of the dispatcher's own, and its id comes back in
// the response rather than being predicted.
func TestPaneSplitReturnsTheNewPane(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"cli:pane:split","result":{"type":"pane_info","pane":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","terminal_id":"x","focused":false,"agent_status":"unknown","revision":1}}}'`)

	pane, err := c.PaneSplit(context.Background(), "wM:p1", "right", "0.5", "/src/p")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if pane != "wM:p9" {
		t.Fatalf("pane: got %q", pane)
	}
	want := "pane split --pane wM:p1 --direction right --cwd /src/p --no-focus --ratio 0.5"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// Running a command, reading the screen and closing the pane are the three
// raw-terminal verbs the spawn pipeline needs.
func TestPaneRunReadAndClose(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `case "$2" in
read) printf '%s\n' "Do you trust the files in this folder?" "  1. Yes, proceed" ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	ctx := context.Background()
	if err := c.PaneRun(ctx, "wM:p9", `eval "$(proxenos env)"`); err != nil {
		t.Fatalf("run: %v", err)
	}
	text, err := c.PaneRead(ctx, "wM:p9", 200)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(text, "trust the files") {
		t.Fatalf("text: got %q", text)
	}
	if err := c.PaneSendKeys(ctx, "wM:p9", "enter"); err != nil {
		t.Fatalf("send-keys: %v", err)
	}
	if err := c.PaneClose(ctx, "wM:p9"); err != nil {
		t.Fatalf("close: %v", err)
	}

	argv := f.Argv(t)
	if got, want := argv[0], []string{"pane", "run", "wM:p9", `eval "$(proxenos env)"`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("run argv: got %v, want %v", got, want)
	}
	rest := []string{
		"pane read wM:p9 --source detection --lines 200",
		"pane send-keys wM:p9 enter",
		"pane close wM:p9",
	}
	for i, want := range rest {
		if got := f.Calls(t)[i+1]; got != want {
			t.Fatalf("call %d: got %q, want %q", i+1, got, want)
		}
	}
}

// The profile's argv reaches the worker after the separator, whole.
func TestAgentStartForwardsAgentArgsAfterTheSeparator(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"agent_started","argv":["claude"],"agent":{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":2,"screen_detection_skipped":false}}}'`)

	a, err := c.AgentStart(context.Background(), StartRequest{
		Name:      "hdis-7",
		Kind:      "claude",
		Pane:      "wM:p9",
		Timeout:   45 * time.Second,
		AgentArgs: []string{"--agent", "claude", "--effort", "low", "/goal do the thing · Done when: ..."},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if a.Status != "idle" || a.PaneID != "wM:p9" {
		t.Fatalf("agent: %+v", a)
	}
	want := []string{
		"agent", "start", "hdis-7", "--kind", "claude", "--pane", "wM:p9", "--timeout", "45000",
		"--", "--agent", "claude", "--effort", "low", "/goal do the thing · Done when: ...",
	}
	if got := f.Argv(t)[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv:\n got %v\nwant %v", got, want)
	}
}

// herdr's own error code survives to the caller, because the spawn pipeline
// decides what a timeout means and the adapter must not decide for it.
func TestAgentStartHandsBackHerdrsOwnErrorCode(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","error":{"code":"timeout","message":"timed out waiting for agent startup"}}' >&2; exit 1`)

	_, err := c.AgentStart(context.Background(), StartRequest{Name: "hdis-7", Kind: "claude", Pane: "wM:p9", Timeout: time.Second})
	var herr *Error
	if !errors.As(err, &herr) {
		t.Fatalf("want a *herdr.Error, got %v", err)
	}
	if herr.Code != "timeout" || !strings.Contains(herr.Message, "timed out") {
		t.Fatalf("got %+v", herr)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("error text drops the code: %v", err)
	}
}

// A refusal that is not JSON still reaches the operator in herdr's words.
func TestANonJSONFailureIsStillReported(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo "herdr: no such pane" >&2; exit 2`)

	err := c.PaneClose(context.Background(), "wM:p9")
	if err == nil || !strings.Contains(err.Error(), "no such pane") {
		t.Fatalf("got %v", err)
	}
}

// Pane liveness and worker state both come from `pane list`, and the reason
// is measured: a pane herdr has not attached an agent to yet is listed there
// with agent_status "unknown", and is absent from `agent list` entirely.
// Reading that absence as "the pane is gone" is what unbound a live worker
// and let its task be dispatched a second time.
func TestPanesMapsEveryLivePaneToItsAgentStatus(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"pane_list","panes":[
{"pane_id":"wM:p9","agent":"claude","agent_status":"working","focused":false,"revision":1},
{"pane_id":"wM:p8","agent":"claude","agent_status":"idle","focused":false,"revision":1},
{"pane_id":"wM:p7","agent_status":"unknown","focused":false,"revision":0}]}}'`)

	byPane, err := c.Panes(context.Background())
	if err != nil {
		t.Fatalf("panes: %v", err)
	}
	want := map[string]string{"wM:p9": "working", "wM:p8": "idle", "wM:p7": "unknown"}
	if !reflect.DeepEqual(byPane, want) {
		t.Fatalf("got %v, want %v", byPane, want)
	}
	if got, want := f.Calls(t)[0], "pane list"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// The dispatcher's one message to the operator is a notification.
func TestNotifyCarriesATitleAndABody(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"notification_show","shown":true,"reason":"shown"}}'`)

	if err := c.Notify(context.Background(), "task 7 is in review", "worker hdis-7 submitted"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	want := []string{"notification", "show", "task 7 is in review", "--body", "worker hdis-7 submitted"}
	if got := f.Argv(t)[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv: got %v, want %v", got, want)
	}
}

// A re-prompt is a plain prompt through the agent surface.
func TestAgentPromptGoesThroughTheAgentSurface(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"agent_prompted","agent":{"pane_id":"wM:p9","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}}}'`)

	if err := c.AgentPrompt(context.Background(), "wM:p9", "claim task 7 and get to work"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	want := []string{"agent", "prompt", "wM:p9", "claim task 7 and get to work"}
	if got := f.Argv(t)[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv: got %v, want %v", got, want)
	}
}

// One worker is read back by its pane id, which is how the spawn pipeline
// confirms a goal took when the pane's own text has already scrolled away.
func TestAgentGetReadsOneWorker(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"agent_info","agent":{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":4,"screen_detection_skipped":false}}}'`)

	a, err := c.AgentGet(context.Background(), "wM:p9")
	if err != nil {
		t.Fatalf("agent get: %v", err)
	}
	if a.Name != "hdis-7" || a.Status != StatusWorking || a.PaneID != "wM:p9" {
		t.Fatalf("got %+v", a)
	}
	if got, want := f.Calls(t)[0], "agent get wM:p9"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// `herdr pane read` is the one verb that answers with the terminal's own
// text and no JSON envelope at all. Reading it as JSON is what killed a live
// worker: the parse failed, the confirm failed, and the pane was retired
// while its worker was already claiming. The bytes below are a capture of
// the real CLI, box drawing and all.
func TestPaneReadTakesTheCliPlainTextAndNotJson(t *testing.T) {
	c, f := client(t)
	const screen = "─────────────────────────────────\n" +
		"  MacBookPro · ~/github.com/husniadil/herdr-dispatch\n" +
		"  ⎿  Goal set: do the thing · Done when: ...\n" +
		"  ◎ /goal active\n"
	f.Bin(t, "herdr", `printf '%s' '`+screen+`'`)

	text, err := c.PaneRead(context.Background(), "wM:p9", 200)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if text != screen {
		t.Fatalf("the screen came back changed:\ngot  %q\nwant %q", text, screen)
	}
}

// A refusal still arrives the way every other verb's does — a JSON error
// body on stderr with a non-zero exit — and its code survives the trip.
func TestPaneReadCarriesTheHerdrErrorCode(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"error":{"code":"pane_not_found","message":"pane wM:p9 not found"},"id":"cli:pane:read"}' >&2; exit 1`)

	_, err := c.PaneRead(context.Background(), "wM:p9", 200)
	var herr *Error
	if !errors.As(err, &herr) || herr.Code != "pane_not_found" {
		t.Fatalf("want a pane_not_found refusal, got %v", err)
	}
}

// Every agent herdr has registered, with the name it registered under. This
// is how a restart enumerates the panes this daemon opened: the name carries
// the task number, and nothing else in Herdr does.
func TestAgentsListEveryRegisteredWorkerWithItsName(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"agent_list","agents":[
{"name":"hdis-7","pane_id":"wM:p9","agent":"claude","agent_status":"working"},
{"pane_id":"wM:p1","agent":"claude","agent_status":"idle"}]}}'`)

	agents, err := c.Agents(context.Background())
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	want := []Agent{
		{Name: "hdis-7", PaneID: "wM:p9", Agent: "claude", Status: "working"},
		{PaneID: "wM:p1", Agent: "claude", Status: "idle"},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("got %v, want %v", agents, want)
	}
	if got, want := f.Calls(t)[0], "agent list"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// §11.1: Herdr is called through HERDR_BIN_PATH, falling back to `herdr` on
// PATH. Nothing here read the variable at all, so a host that installs Herdr
// somewhere PATH does not carry — which is exactly what the variable is for —
// had every call fail on a binary that was not missing.
func TestTheHerdrBinaryComesFromTheVariableTheContractNames(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/opt/herdr/bin/herdr")
	if got := New().bin(); got != "/opt/herdr/bin/herdr" {
		t.Errorf("bin() = %q, want the path HERDR_BIN_PATH names", got)
	}
	t.Setenv("HERDR_BIN_PATH", "")
	if got := New().bin(); got != "herdr" {
		t.Errorf("bin() = %q, want the PATH fallback", got)
	}
	// The variable is read at construction and never inside bin(): a test's
	// fake on PATH must not be bypassed by the operator's own environment.
	c := &Client{}
	t.Setenv("HERDR_BIN_PATH", "/opt/herdr/bin/herdr")
	if got := c.bin(); got != "herdr" {
		t.Errorf("a client built without the override read it anyway: %q", got)
	}
}
