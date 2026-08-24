package proxy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// measuredRollup is the document `proxenos usage --json` answers with, probed
// against proxenos 0.13.0. The rollup this reads is TOP-LEVEL; the accounts
// array beside it is every account the proxy knows, and the gate asks about
// one — the one it is serving.
const measuredRollup = `{
  "accounts": [
    {"account":"claude","known":true,"limit_reached":false,"measured_at":1787536142,"plan":null,
     "provider":"anthropic","serving":false,"source":"fetch",
     "windows":[{"label":null,"representative":false,"resets_at":1787976000,"status":"normal","used_percent":36.0,"window_minutes":10080}]},
    {"account":"openai-api","known":false,"reason":"metered","provider":"openai-api","serving":false}
  ],
  "known": true,
  "limit_reached": false,
  "models": ["gpt-5.6-luna"],
  "plan": "team",
  "serving": {"account":"work-codex","account_id":"ea12","email":"a@b.c","plan":"team","provider":"codex"},
  "windows": [
    {"label":null,"representative":false,"resets_at":1788142225,"status":null,"used_percent":1.0,"window_minutes":10080}
  ]
}`

func TestUsageReadsTheServingRollupAndNotTheAccountsArray(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `cat <<'JSON'
`+measuredRollup+`
JSON`)

	u, err := (&Client{}).Usage(context.Background())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !u.Known || u.LimitReached {
		t.Fatalf("rollup: %+v", u)
	}
	if got, want := u.Account, "work-codex"; got != want {
		t.Fatalf("account: got %q, want %q — the rollup's serving account, not one out of accounts[]", got, want)
	}
	if got, want := u.Plan, "team"; got != want {
		t.Fatalf("plan: got %q, want %q", got, want)
	}
	// The claude account's 36% is in the same document and is not this
	// account's figure.
	if got, want := u.UsedPercent, 1.0; got != want {
		t.Fatalf("used_percent: got %v, want %v", got, want)
	}
}

// The cheap default, and the point of it: `usage` without --refresh reports
// what the daemon already holds. A tick that spent an upstream request per
// spawn decision would cost the quota it exists to protect.
func TestUsageNeverAsksTheProxyToRefresh(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"known":false}'`)

	if _, err := (&Client{}).Usage(context.Background()); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if got, want := f.Calls(t)[0], "usage --json"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// With no per-window threshold semantics, the window nearest to stopping the
// account is the one the gate reads.
func TestUsageTakesTheFullestOfSeveralWindows(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"known":true,"limit_reached":false,"serving":{"account":"work-codex"},
	  "windows":[{"used_percent":12.5,"window_minutes":300,"status":null},
	             {"used_percent":88.25,"window_minutes":10080,"status":"normal"},
	             {"used_percent":40.0,"window_minutes":43200,"status":null}]}'`)

	u, err := (&Client{}).Usage(context.Background())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if got, want := u.UsedPercent, 88.25; got != want {
		t.Fatalf("used_percent: got %v, want the fullest window's %v", got, want)
	}
}

// A null status is a normal window. Requiring the string would read every
// codex window as abnormal, which is most of them.
func TestUsageDoesNotRequireAWindowToCarryAStatus(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"known":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":50.0,"status":null,"label":null,"representative":false}]}'`)

	u, err := (&Client{}).Usage(context.Background())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !u.Known || u.UsedPercent != 50 {
		t.Fatalf("usage: %+v", u)
	}
}

// A metered key has no ceiling: known is false and there is no windows key at
// all. Nothing here invents a zero for it — the caller reads Known.
func TestUsageOfAMeteredAccountIsNotKnown(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"known":false,"reason":"metered","serving":{"account":"openai-api","provider":"openai-api"}}'`)

	u, err := (&Client{}).Usage(context.Background())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Known {
		t.Fatalf("a metered account read as gaugeable: %+v", u)
	}
	if got, want := u.Account, "openai-api"; got != want {
		t.Fatalf("account: got %q, want %q", got, want)
	}
}

func TestUsageCarriesTheLimitReachedFlag(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo '{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}]}'`)

	u, err := (&Client{}).Usage(context.Background())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !u.LimitReached {
		t.Fatalf("usage: %+v", u)
	}
}

// A down daemon answers in its own words and those are what the caller gets,
// the same as Status and step zero of a spawn.
func TestUsageOfADownDaemonCarriesItsOwnMessage(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo "Error: the daemon is not answering. Start it with 'proxenos run'." >&2; exit 1`)

	if _, err := (&Client{}).Usage(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "proxenos run") {
		t.Fatalf("want the daemon's own words, got %v", err)
	}
}

func TestUsageSaysWhenTheProxyIsNotInstalled(t *testing.T) {
	testenv.New(t)

	if _, err := (&Client{}).Usage(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
}

// Unparseable is a failure and never a quiet zero: the gate proceeds on an
// unknown quota, and it must be the caller that decides that, having been
// told.
func TestUsageRefusesADocumentItCannotRead(t *testing.T) {
	f := testenv.New(t)
	f.Bin(t, "proxenos", `echo 'not json at all'`)

	if _, err := (&Client{}).Usage(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if !strings.Contains(err.Error(), "unreadable json") {
		t.Fatalf("got %v", err)
	}
}
