package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/fake"
)

// The proxy prints its settings document pretty, and herdr refuses a newline
// in an agent argument outright, so it is compacted before it travels.
func TestSettingsAreCompactedToOneLine(t *testing.T) {
	f := fake.New(t)
	f.Bin(t, "codex-cc-proxy", `cat <<'EOF'
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:8787"
  },
  "permissions": {
    "deny": ["Bash(rm -rf *)"]
  }
}
EOF`)

	got, err := (&Client{}).Settings(context.Background())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("settings carry a line break: %q", got)
	}
	want := `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"},"permissions":{"deny":["Bash(rm -rf *)"]}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if c := f.Calls(t)[0]; c != "settings" {
		t.Fatalf("argv: got %q", c)
	}
}

// A down daemon is step zero's failure, and the operator gets the daemon's
// own words: they name the socket and say how to start it.
func TestADownDaemonFailsWithItsOwnMessage(t *testing.T) {
	f := fake.New(t)
	f.Bin(t, "codex-cc-proxy", `cat >&2 <<'EOF'
Error: the daemon is not answering (could not reach the daemon at /tmp/codex-cc-proxy.sock),
so there is no configuration to start this with. Start it with 'codex-cc-proxy run'.
EOF
exit 1`)

	_, err := (&Client{}).Settings(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"the daemon is not answering", "codex-cc-proxy run"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q in the error, got %v", want, err)
		}
	}
}

// A settings document that is not JSON is refused rather than passed on: an
// unparseable one cannot be compacted, and an uncompacted one is a newline
// herdr will refuse later, further from the cause.
func TestUnreadableSettingsAreRefusedHere(t *testing.T) {
	f := fake.New(t)
	f.Bin(t, "codex-cc-proxy", `echo "not json at all"`)

	if _, err := (&Client{}).Settings(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

// The environment half is delivered by the shell, exactly as the proxy's own
// README spells it, and it names the configured binary.
func TestEnvCommandIsTheEvalTheProxyDocuments(t *testing.T) {
	if got, want := (&Client{}).EnvCommand(), `eval "$(codex-cc-proxy env)"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := (&Client{Bin: "/opt/ccp"}).EnvCommand(), `eval "$(/opt/ccp env)"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
