package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// The proxy prints its settings document pretty, and herdr refuses a newline
// in an agent argument outright, so it is compacted before it travels.
func TestSettingsAreCompactedToOneLine(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `cat <<'EOF'
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
	f := testenv.New(t)
	f.Bin(t, "proxenos", `cat >&2 <<'EOF'
Error: the daemon is not answering (could not reach the daemon at /tmp/proxenos.sock),
so there is no configuration to start this with. Start it with 'proxenos run'.
EOF
exit 1`)

	_, err := (&Client{}).Settings(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"the daemon is not answering", "proxenos run"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q in the error, got %v", want, err)
		}
	}
}

// A settings document that is not JSON is refused rather than passed on: an
// unparseable one cannot be compacted, and an uncompacted one is a newline
// herdr will refuse later, further from the cause.
func TestUnreadableSettingsAreRefusedHere(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo "not json at all"`)

	if _, err := (&Client{}).Settings(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}

// The environment half is delivered by the shell, exactly as the proxy's own
// README spells it, and it names the configured binary.
func TestEnvCommandIsTheEvalTheProxyDocuments(t *testing.T) {
	if got, want := (&Client{}).EnvCommand(""), `eval "$(proxenos env)"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := (&Client{Bin: "/opt/ccp"}).EnvCommand(""), `eval "$(/opt/ccp env)"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A profile that names an account exports the proxy's own per-session tag
// AFTER the eval, because the eval sets a token of its own and the last
// export is the one the child carries.
func TestEnvCommandExportsTheProfileAccountAfterTheEval(t *testing.T) {
	got := (&Client{}).EnvCommand("work-codex")
	want := `eval "$(proxenos env)"; export ANTHROPIC_AUTH_TOKEN=proxenos-account:work-codex`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Index(got, "ANTHROPIC_AUTH_TOKEN") < strings.Index(got, "proxenos env") {
		t.Fatal("the account tag was exported before the eval that overwrites it")
	}
	if got, want := (&Client{Bin: "/opt/ccp"}).EnvCommand("acct"),
		`eval "$(/opt/ccp env)"; export ANTHROPIC_AUTH_TOKEN=proxenos-account:acct`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// doctor checks a configured account name against the store before a spawn
// fails in a pane, so the client reads the names `accounts list --json`
// holds.
func TestAccountsNamesWhatTheStoreHolds(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `cat <<'EOF'
{"accounts":[
  {"name":"claude","provider":"anthropic","kind":"grant","selected":false},
  {"name":"work-codex","provider":"codex","kind":"grant","selected":true},
  {"name":"openai-api","provider":"codex","kind":"key","selected":false}
],"selected":"work-codex"}
EOF`)

	got, err := (&Client{}).Accounts(context.Background())
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	want := []string{"claude", "work-codex", "openai-api"}
	if len(got) != len(want) {
		t.Fatalf("accounts: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("accounts: got %v, want %v", got, want)
		}
	}
	if c := f.Calls(t)[0]; c != "accounts list --json" {
		t.Fatalf("argv: got %q", c)
	}
}

// A store that cannot be read is not a name that is missing. doctor has to be
// able to tell the two apart, so the error travels rather than reading as an
// empty store.
func TestAccountsOfADownDaemonCarriesItsOwnMessage(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1`)

	if _, err := (&Client{}).Accounts(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "proxenos run") {
		t.Fatalf("want the daemon's own words, got %v", err)
	}
}

// doctor asks the proxy whether it is up. `status --json` is the daemon's own
// report, and the account it names is what an operator checks before blaming
// a spawn on the wrong subscription.
func TestStatusNamesTheActiveAccount(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"auth":{"account":"work-codex","account_id":"ea12"},"catalog":{"reachable":true}}'`)

	st, err := (&Client{}).Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if got, want := st.Account, "work-codex"; got != want {
		t.Fatalf("account: got %q, want %q", got, want)
	}
	if c := f.Calls(t)[0]; c != "status --json" {
		t.Fatalf("argv: got %q", c)
	}
}

// A down daemon is reported in its own words, the same as step zero does:
// they name the socket and say how to start it.
func TestStatusOfADownDaemonCarriesItsOwnMessage(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1`)

	if _, err := (&Client{}).Status(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "proxenos run") {
		t.Fatalf("want the daemon's own words, got %v", err)
	}
}

// A proxy that is not installed at all is a different fact from one that is
// down, and doctor has to be able to tell them apart.
func TestStatusSaysWhenTheProxyIsNotInstalled(t *testing.T) {
	testenv.New(t)

	_, err := (&Client{}).Status(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}
