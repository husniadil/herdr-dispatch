package loop

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// The two bounds are one rule written in two places — config refuses a longer
// chain at parse, and the core stops walking one it was handed anyway — so
// they have to be the same number.
func TestTheCoreAndTheConfigBoundAFallbackChainTheSame(t *testing.T) {
	if config.MaxFallbackDepth != decide.MaxFallbackChain {
		t.Fatalf("config bounds a chain at %d and the core at %d",
			config.MaxFallbackDepth, decide.MaxFallbackChain)
	}
}

// chainLoop is the quota harness with a chain rather than one profile: a
// routed profile pinned to one account, a spare pinned to another, and a
// claude profile at the end.
func chainLoop(t *testing.T, doc, usage string) (*Loop, *testenv.Fake) {
	t.Helper()
	l, f := newLoop(t)
	cfg, err := config.Parse([]byte(doc))
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
  accounts) echo '{"accounts":[{"account":"work-codex"},{"account":"spare-codex"}]}' ;;
esac`)
	return l, f
}

const chainDoc = `default = "routed"
[proxy]
max_used_percent = 90
[profiles.routed]
provider = "codex"
account = "work-codex"
fallback = "spare"
[profiles.spare]
provider = "codex"
account = "spare-codex"
fallback = "plain"
[profiles.plain]
provider = "claude"
`

// The proxy's own document: a serving rollup, and the per-account figures the
// chain's steps are gated on beside it.
func usageWith(work, spare string) string {
	return `{"known":true,"limit_reached":false,
  "serving":{"account":"work-codex","plan":"team"},
  "windows":[{"used_percent":4.0}],
  "accounts":[` + work + `,` + spare + `]}`
}

const (
	workSpent = `{"account":"work-codex","known":true,"limit_reached":true,"plan":"team","windows":[{"used_percent":100.0}]}`
	workFree  = `{"account":"work-codex","known":true,"limit_reached":false,"plan":"team","windows":[{"used_percent":4.0}]}`
	spareFree = `{"account":"spare-codex","known":true,"limit_reached":false,"plan":"team","windows":[{"used_percent":11.0}]}`
	spareFull = `{"account":"spare-codex","known":true,"limit_reached":false,"plan":"team","windows":[{"used_percent":95.0}]}`
)

// The chain as the tick walks it: each step gated on its OWN account, so a
// spent routed account moves the spawn to the spare rather than refusing it.
//
// The decision is asserted rather than the pane, because a codex worker's
// typed line is close enough to spawn.TypedLineBudget that whether one
// completes depends on the length of the machine's TMPDIR. What this dispatcher
// CHOOSES does not, and it is the thing this key changed.
func TestTheTickChoosesTheFallbackWhenTheRoutedAccountIsSpent(t *testing.T) {
	l, _ := chainLoop(t, chainDoc, usageWith(workSpent, spareFree))
	c := l.choose(l.Quota(context.Background()), htask.Task{Project: "/src/p"})
	if !c.Eligible || c.Profile != "spare" || c.FellBackFrom() != "routed" {
		t.Fatalf("the chain chose %+v, want spare, fallen back from routed", c)
	}
	for _, want := range []string{"launched through spare", "routed's account work-codex", "at its limit"} {
		if !strings.Contains(c.Note(), want) {
			t.Fatalf("the note does not carry %q: %q", want, c.Note())
		}
	}
}

// A spawn that launched with the profile it was asked for is not a finding,
// and it says nothing.
func TestTheTickChoosesTheRoutedProfileWhenItsOwnAccountHasRoom(t *testing.T) {
	l, _ := chainLoop(t, chainDoc, usageWith(workFree, spareFree))
	c := l.choose(l.Quota(context.Background()), htask.Task{Project: "/src/p"})
	if !c.Eligible || c.Profile != "routed" {
		t.Fatalf("the chain chose %+v, want routed", c)
	}
	if c.FellBackFrom() != "" || c.Note() != "" {
		t.Fatalf("a spawn that moved nowhere recorded %q and said %q", c.FellBackFrom(), c.Note())
	}
}

// A claude profile spends no proxy quota and is eligible whatever the proxy
// says, which is what makes it an end a chain can always reach. It is also
// the whole way through: the pane comes up, the binding records both names,
// and `hdis status` reports them.
func TestTheClaudeEndOfAChainIsReachedWithEveryCodexAccountSpent(t *testing.T) {
	l, f := chainLoop(t, chainDoc, usageWith(workSpent,
		`{"account":"spare-codex","known":true,"limit_reached":true,"windows":[{"used_percent":100.0}]}`))
	var logged bytes.Buffer
	l.Log = log.New(&logged, "", 0)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := calls(t, f, "tab create"); len(got) != 1 {
		t.Fatalf("the spawn did not fall back, it was refused: %v", got)
	}
	b := l.Bindings()
	if len(b) != 1 {
		t.Fatalf("bindings: %+v", b)
	}
	// Both names, because they answer different questions: what is running
	// in this pane, and what the fleet meant to run.
	if b[0].Profile != "plain" || b[0].AskedProfile != "routed" {
		t.Fatalf("the binding records profile %q asked %q, want plain asked routed",
			b[0].Profile, b[0].AskedProfile)
	}
	// The operator is told, on the channel every other finding of this
	// dispatcher's reaches them on.
	for _, want := range []string{"launched through plain", "routed's account work-codex", "spare's account spare-codex"} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("the daemon did not say %q: %q", want, logged.String())
		}
	}
	// The same two names on the verb that lists workers.
	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].Profile != "plain" || st.Workers[0].AskedProfile != "routed" {
		t.Fatalf("status reports %+v", st.Workers)
	}
}

// A worker that launched with the profile it was asked for records one name,
// not two, all the way to the verb that lists it.
func TestAWorkerThatDidNotFallBackRecordsOneProfile(t *testing.T) {
	l, _ := chainLoop(t, `default = "plain"
[proxy]
max_used_percent = 90
[profiles.plain]
provider = "claude"
`, usageWith(workFree, spareFree))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	b := l.Bindings()
	if len(b) != 1 || b[0].Profile != "plain" {
		t.Fatalf("bindings: %+v", b)
	}
	if b[0].AskedProfile != "" {
		t.Fatalf("a worker that moved nowhere recorded an asked profile %q", b[0].AskedProfile)
	}
}

// With the whole chain spent the verb still refuses AT_QUOTA, and it names
// every profile and account it tried: an operator who has to free one needs
// to know which.
func TestDispatchRefusesAtQuotaNamingEveryProfileTheChainTried(t *testing.T) {
	l, _ := chainLoop(t, `default = "routed"
[proxy]
max_used_percent = 90
[profiles.routed]
provider = "codex"
account = "work-codex"
fallback = "spare"
[profiles.spare]
provider = "codex"
account = "spare-codex"
`, usageWith(workSpent, spareFull))

	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.AtQuota; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
	for _, want := range []string{"routed", "work-codex", "spare", "spare-codex", "90"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A fleet whose profiles name no fallback is refused in exactly the sentence
// it was before chains existed.
func TestAFleetWithNoFallbackIsStillRefusedInTheOldWords(t *testing.T) {
	l, _ := quotaLoop(t, `{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}]}`)
	_, err := l.Dispatch(context.Background(), "7", "")
	if got, want := codes.ReasonOf(err), codes.AtQuota; got != want {
		t.Fatalf("dispatch = %v (%q), want %q", err, got, want)
	}
	want := "task 7 launches through the proxy and the proxy reports work-codex at its limit"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal reads %v, want it to carry %q", err, want)
	}
}

// The chain's steps are gated on their OWN accounts, read out of the same one
// `usage --json` call the gate already made.
func TestThePerAccountFiguresComeOutOfTheOneUsageCall(t *testing.T) {
	l, f := chainLoop(t, chainDoc, usageWith(workSpent, spareFree))
	q := l.Quota(context.Background())
	if !q.For("work-codex").LimitReached {
		t.Fatalf("work-codex reads %+v, want it at its limit", q.For("work-codex"))
	}
	if got := q.For("spare-codex").UsedPercent; got != 11 {
		t.Fatalf("spare-codex reads %v%% of its window, want 11", got)
	}
	// A name the proxy did not report is a quota nobody read, and an unread
	// quota gates nothing.
	if q.For("nobody").Known {
		t.Fatalf("an account the proxy never named came back known: %+v", q.For("nobody"))
	}
	if got := calls(t, f, "usage"); len(got) != 1 {
		t.Fatalf("the proxy was asked %d times for one tick's figures", len(got))
	}
}
