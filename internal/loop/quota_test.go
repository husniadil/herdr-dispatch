package loop

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// quotaLoop is the standard harness with a codex default profile and a
// threshold set, so the one lane the quota gate applies to is the lane every
// spawn takes. usage is the document the fake proxy answers `usage --json`
// with.
func quotaLoop(t *testing.T, usage string) (*Loop, *testenv.Fake) {
	t.Helper()
	l, f := newLoop(t)
	cfg, err := config.Parse([]byte(`default = "worker"
[proxy]
max_used_percent = 90
[profiles.worker]
provider = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}
	l.Config = cfg
	l.Policy.MaxUsedPercent = cfg.Proxy.MaxUsedPercent
	l.Spawn.SettingsDir = t.TempDir()
	f.Bin(t, "proxenos", `case "$1" in
  usage) cat <<'JSON'
`+usage+`
JSON
  ;;
  settings) echo '{"model":"gpt"}' ;;
  env) echo 'export PROXENOS=1' ;;
esac`)
	return l, f
}

const roomToSpend = `{"known":true,"limit_reached":false,"serving":{"account":"work-codex","plan":"team"},"windows":[{"used_percent":4.0}]}`

// The gate at work: the account cannot pay, so the tick brings no pane up at
// all. A worker that starts here dies on a quota error with a pane already
// spent on it.
func TestATickSpawnsNoCodexWorkerWhenTheAccountIsAtItsLimit(t *testing.T) {
	l, f := quotaLoop(t, `{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}]}`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 0 {
		t.Fatalf("a codex worker was brought up against a spent account: %v", got)
	}
	if got := l.Bindings(); len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
}

func TestATickSpawnsACodexWorkerWhenTheAccountHasRoom(t *testing.T) {
	l, f := quotaLoop(t, roomToSpend)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("a spawn with room to spend was refused: %v", got)
	}
}

// The cheap read, once per tick: a gate that asked the proxy once per ready
// task would spend a process per row, and one that passed --refresh would
// spend an upstream request per tick.
func TestTheTickAsksTheProxyForUsageWithoutRefreshing(t *testing.T) {
	l, f := quotaLoop(t, roomToSpend)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	asked := calls(t, f, "usage")
	if len(asked) != 1 {
		t.Fatalf("the tick asked for usage %d times: %v", len(asked), asked)
	}
	if got, want := asked[0], "usage --json"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// A claude fleet never routes through the proxy, so a tick must not shell out
// to a binary no worker it launches touches.
func TestATickWithNoCodexProfileNeverAsksTheProxyAnything(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "proxenos", `echo '{"known":true,"limit_reached":true}'; exit 0`)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "usage"); len(got) != 0 {
		t.Fatalf("a claude-only fleet asked the proxy about quota: %v", got)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("a claude spawn was gated on a proxy it never touches: %v", got)
	}
}

// Fail loud, idle safe: a proxy that cannot be asked leaves the quota unknown,
// the spawn goes ahead, and the daemon says why it could not ask. A dead proxy
// still fails the spawn at step zero, where the message is the proxy's own.
func TestAnUnreachableProxyLetsTheSpawnProceedAndIsLogged(t *testing.T) {
	l, f := quotaLoop(t, "")
	f.Bin(t, "proxenos", `case "$1" in
  usage) echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1 ;;
  settings) echo '{"model":"gpt"}' ;;
  env) echo 'export PROXENOS=1' ;;
esac`)
	var logged bytes.Buffer
	l.Log = log.New(&logged, "", 0)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("an unknown quota stopped a spawn: %v", got)
	}
	if !strings.Contains(logged.String(), "proxenos run") {
		t.Fatalf("the daemon did not say why it could not ask: %q", logged.String())
	}
}

// The refusal reaches the caller of the verb, under a name of its own, so an
// operator reads quota rather than waiting for a pane that never comes up.
func TestDispatchRefusesACodexTaskWhenTheAccountIsSpent(t *testing.T) {
	l, _ := quotaLoop(t, `{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}]}`)

	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.AtQuota; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "work-codex") {
		t.Fatalf("the refusal does not name the account: %v", err)
	}
}

// Past the configured share of the window, with the proxy's own flag still
// clear: this is the threshold's whole reason to exist, leaving room for the
// worker's own turns rather than stopping at empty.
func TestDispatchRefusesACodexTaskPastTheConfiguredThreshold(t *testing.T) {
	l, _ := quotaLoop(t, `{"known":true,"limit_reached":false,"serving":{"account":"work-codex"},"windows":[{"used_percent":95.0}]}`)

	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.AtQuota; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
	if !strings.Contains(err.Error(), "95") || !strings.Contains(err.Error(), "90") {
		t.Fatalf("the refusal says neither figure: %v", err)
	}
}

func TestDispatchAcceptsACodexTaskWhenTheAccountHasRoom(t *testing.T) {
	l, _ := quotaLoop(t, roomToSpend)

	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

// A claude task is never refused on the proxy's quota, whatever the proxy
// says: it does not spend that account.
func TestDispatchOfAClaudeTaskIsNotGatedOnTheProxy(t *testing.T) {
	l, f := newLoop(t)
	f.Bin(t, "proxenos", `echo '{"known":true,"limit_reached":true,"serving":{"account":"work-codex"}}'`)

	if _, err := l.Dispatch(context.Background(), "7", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}
