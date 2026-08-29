package decide

import (
	"strings"
	"testing"
	"time"
)

// chained is the smallest snapshot that offers one codex task routed to a
// profile which falls back, with a proxy that has answered per-account.
func chained(profiles map[string]ProfileFacts, q Quota) Snapshot {
	return Snapshot{
		Ready: []string{"t1"},
		Tasks: map[string]Task{"t1": {
			ID: "t1", Status: "todo", Project: "/repo", Codex: true, Profile: "routed",
		}},
		Profiles: profiles,
		Quota:    q,
		Now:      time.Unix(0, 0),
	}
}

// The shape this exists for: a pinned account, a spare account, and a claude
// profile at the end.
func threeStepChain() map[string]ProfileFacts {
	return map[string]ProfileFacts{
		"routed": {Codex: true, Account: "work-codex", Fallback: "spare"},
		"spare":  {Codex: true, Account: "spare-codex", Fallback: "worker"},
		"worker": {},
	}
}

func onlySpawn(t *testing.T, as []Action) Action {
	t.Helper()
	var out []Action
	for _, a := range as {
		if a.Kind == Spawn {
			out = append(out, a)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want exactly one spawn, got %d in %v", len(out), as)
	}
	return out[0]
}

// The account each step spends is its OWN, which is the whole point of a
// chain: the routed profile's is spent, the spare's is not, and the spawn
// launches through the spare.
func TestASpawnFallsBackToTheNextProfileWhenTheRoutedAccountIsAtQuota(t *testing.T) {
	s := chained(threeStepChain(), Quota{
		Known: true, Account: "work-codex", LimitReached: true,
		Accounts: map[string]Quota{
			"work-codex":  {Known: true, Account: "work-codex", LimitReached: true},
			"spare-codex": {Known: true, Account: "spare-codex", UsedPercent: 12},
		},
	})
	got := onlySpawn(t, Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90}))
	if got.Profile != "spare" {
		t.Fatalf("launched through %q, want spare", got.Profile)
	}
}

// A worker records the profile it really ran under AND the one it was asked
// for: they answer different questions, and one name cannot answer both.
func TestASpawnThatFellBackRecordsBothProfilesAndSaysWhy(t *testing.T) {
	s := chained(threeStepChain(), Quota{
		Known: true, Account: "work-codex", LimitReached: true,
		Accounts: map[string]Quota{
			"work-codex":  {Known: true, Account: "work-codex", LimitReached: true},
			"spare-codex": {Known: true, Account: "spare-codex", UsedPercent: 12},
		},
	})
	got := onlySpawn(t, Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90}))
	if got.Profile != "spare" || got.AskedFor != "routed" {
		t.Fatalf("ran under %q asked for %q, want spare asked for routed", got.Profile, got.AskedFor)
	}
	// The operator's own sentence: which profile it launched through, whose
	// account was spent out, and what that account read.
	for _, want := range []string{"launched through spare", "routed's account work-codex", "at its limit"} {
		if !strings.Contains(got.QuotaNote, want) {
			t.Fatalf("the note does not carry %q: %q", want, got.QuotaNote)
		}
	}
}

// A spawn that launched with the profile it was asked for is not a finding,
// and it records one name rather than two.
func TestASpawnThatDidNotFallBackRecordsOneProfileAndNoNote(t *testing.T) {
	s := chained(threeStepChain(), Quota{
		Known: true, Account: "work-codex", UsedPercent: 3,
		Accounts: map[string]Quota{"work-codex": {Known: true, Account: "work-codex", UsedPercent: 3}},
	})
	got := onlySpawn(t, Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90}))
	if got.Profile != "routed" {
		t.Fatalf("launched through %q, want routed", got.Profile)
	}
	if got.AskedFor != "" || got.QuotaNote != "" {
		t.Fatalf("a spawn that moved nowhere recorded %q and %q", got.AskedFor, got.QuotaNote)
	}
}

// A claude profile spends no proxy quota at all, so it is eligible whatever
// the proxy says. That is what makes it an end a chain can always reach.
func TestAClaudeFallbackIsEligibleWithEveryCodexAccountSpent(t *testing.T) {
	spent := Quota{Known: true, LimitReached: true}
	s := chained(threeStepChain(), Quota{
		Known: true, Account: "work-codex", LimitReached: true,
		Accounts: map[string]Quota{"work-codex": spent, "spare-codex": spent},
	})
	got := onlySpawn(t, Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90}))
	if got.Profile != "worker" {
		t.Fatalf("launched through %q, want the claude profile worker", got.Profile)
	}
	if got.AskedFor != "routed" {
		t.Fatalf("the asked-for profile reads %q", got.AskedFor)
	}
}

// When the whole chain is spent the refusal stays AT_QUOTA and names every
// profile and account it tried, because an operator who has to free one needs
// to know which.
func TestAChainWithEveryProfileAtQuotaIsRefusedNamingEveryOne(t *testing.T) {
	profiles := map[string]ProfileFacts{
		"routed": {Codex: true, Account: "work-codex", Fallback: "spare"},
		"spare":  {Codex: true, Account: "spare-codex"},
	}
	q := Quota{
		Known: true, Account: "work-codex", LimitReached: true,
		Accounts: map[string]Quota{
			"work-codex":  {Known: true, Account: "work-codex", LimitReached: true},
			"spare-codex": {Known: true, Account: "spare-codex", UsedPercent: 95},
		},
	}
	p := Policy{MaxWorkers: 2, MaxUsedPercent: 90}
	if got := spawned(Decide(chained(profiles, q), p)); len(got) != 0 {
		t.Fatalf("a spawn ran with every profile in its chain at quota: %v", got)
	}
	why := ChooseProfile("routed", true, profiles, q, p).Refusal()
	for _, want := range []string{
		"every profile it would launch through is at quota",
		"routed's account work-codex",
		"spare's account spare-codex",
		"max_used_percent 90",
	} {
		if !strings.Contains(why, want) {
			t.Fatalf("the refusal does not carry %q: %q", want, why)
		}
	}
}

// A fleet whose profiles name no fallback is refused in exactly the words it
// was before chains existed. A chain of one is not a new sentence.
func TestAChainOfOneIsRefusedInTheOldWords(t *testing.T) {
	profiles := map[string]ProfileFacts{"routed": {Codex: true, Account: "work-codex"}}
	q := Quota{
		Known: true, Account: "work-codex",
		Accounts: map[string]Quota{"work-codex": {Known: true, Account: "work-codex", LimitReached: true}},
	}
	p := Policy{MaxWorkers: 2, MaxUsedPercent: 90}
	got := ChooseProfile("routed", true, profiles, q, p).Refusal()
	want := QuotaRefusal(q.For("work-codex"), p)
	if got != want {
		t.Fatalf("a chain of one is refused with %q, want %q", got, want)
	}
}

// An account the proxy did not report is a quota nobody read, and an unread
// quota gates nothing — the same as a rollup that came back unknown. Refusing
// there would stop a fleet on a fact nobody has.
func TestAnAccountTheProxyDidNotReportGatesNothing(t *testing.T) {
	profiles := map[string]ProfileFacts{"routed": {Codex: true, Account: "work-codex", Fallback: "spare"}}
	q := Quota{Known: true, Account: "serving", LimitReached: true}
	c := ChooseProfile("routed", true, profiles, q, Policy{MaxUsedPercent: 90})
	if !c.Eligible || c.Profile != "routed" {
		t.Fatalf("an unread account stopped a spawn: %+v", c)
	}
}

// A caller that resolved no profile table at all — every tick written before
// chains existed — decides exactly as it did: one candidate, gated on the
// task's own provider and the serving account.
func TestACallerWithNoProfileTableDecidesAsItDidBefore(t *testing.T) {
	q := Quota{Known: true, Account: "work-codex", LimitReached: true}
	p := Policy{MaxWorkers: 2}
	if c := ChooseProfile("", true, nil, q, p); c.Eligible {
		t.Fatalf("a codex spawn with the serving account at its limit was allowed: %+v", c)
	}
	c := ChooseProfile("", false, nil, q, p)
	if !c.Eligible || c.Profile != "" {
		t.Fatalf("a claude spawn was gated on a proxy it never touches: %+v", c)
	}
}

// A table assembled some other way may carry a cycle config would have
// refused. The core stops on it rather than walking it forever, and what it
// has already refused is what the refusal names.
func TestTheCoreStopsOnACycleRatherThanWalkingItForever(t *testing.T) {
	profiles := map[string]ProfileFacts{
		"routed": {Codex: true, Account: "work-codex", Fallback: "spare"},
		"spare":  {Codex: true, Account: "spare-codex", Fallback: "routed"},
	}
	spent := Quota{Known: true, LimitReached: true}
	q := Quota{Known: true, LimitReached: true, Accounts: map[string]Quota{
		"work-codex": spent, "spare-codex": spent,
	}}
	c := ChooseProfile("routed", true, profiles, q, Policy{})
	if c.Eligible {
		t.Fatalf("a cycle of spent accounts found something to launch: %+v", c)
	}
	if len(c.Refused) != 2 {
		t.Fatalf("the cycle was walked %d times, want each profile once", len(c.Refused))
	}
}

// The bound is walked and no further, whatever a table carries past it.
func TestNoMoreThanMaxFallbackChainProfilesAreTried(t *testing.T) {
	profiles := map[string]ProfileFacts{}
	names := []string{"p1", "p2", "p3", "p4", "p5", "p6"}
	accounts := map[string]Quota{}
	for i, name := range names {
		next := ""
		if i+1 < len(names) {
			next = names[i+1]
		}
		profiles[name] = ProfileFacts{Codex: true, Account: name, Fallback: next}
		accounts[name] = Quota{Known: true, Account: name, LimitReached: true}
	}
	c := ChooseProfile("p1", true, profiles, Quota{Accounts: accounts}, Policy{})
	if len(c.Refused) != MaxFallbackChain {
		t.Fatalf("%d profiles were tried, want %d", len(c.Refused), MaxFallbackChain)
	}
}
