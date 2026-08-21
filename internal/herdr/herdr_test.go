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

	pane, err := c.PaneSplit(context.Background(), "wM:p1", "right", "/src/p")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if pane != "wM:p9" {
		t.Fatalf("pane: got %q", pane)
	}
	want := "pane split --pane wM:p1 --direction right --cwd /src/p --no-focus"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// Running a command, reading the screen and closing the pane are the three
// raw-terminal verbs the spawn pipeline needs.
func TestPaneRunReadAndClose(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `case "$2" in
read) echo '{"id":"x","result":{"type":"pane_read","read":{"pane_id":"wM:p9","workspace_id":"wM","tab_id":"wM:t1","source":"detection","format":"text","text":"Do you trust the files in this folder?","revision":3,"truncated":false}}}' ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`)

	ctx := context.Background()
	if err := c.PaneRun(ctx, "wM:p9", `eval "$(codex-cc-proxy env)"`); err != nil {
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
	if got, want := argv[0], []string{"pane", "run", "wM:p9", `eval "$(codex-cc-proxy env)"`}; !reflect.DeepEqual(got, want) {
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

// Pane liveness and worker state both come from agent_status, which is the
// only truth about a worker this repo accepts.
func TestAgentsMapsPanesToStatus(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"agent_list","agents":[
{"pane_id":"wM:p9","name":"hdis-7","agent":"claude","agent_status":"working","interactive_ready":false,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false},
{"pane_id":"wM:p8","name":"hdis-4","agent":"claude","agent_status":"idle","interactive_ready":true,"focused":false,"launch_pending":false,"revision":1,"screen_detection_skipped":false}]}}'`)

	byPane, err := c.Agents(context.Background())
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	if got, want := byPane, map[string]string{"wM:p9": "working", "wM:p8": "idle"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := f.Calls(t)[0], "agent list"; got != want {
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
