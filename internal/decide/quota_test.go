package decide

import (
	"strings"
	"testing"
	"time"
)

// offering is the smallest snapshot that offers one task to spawn for.
func offering(id string, codex bool) Snapshot {
	return Snapshot{
		Ready: []string{id},
		Tasks: map[string]Task{id: {ID: id, Status: "todo", Project: "/repo", Codex: codex}},
		Now:   time.Unix(0, 0),
	}
}

func TestALimitReachedAccountStopsACodexSpawn(t *testing.T) {
	s := offering("t1", true)
	s.Quota = Quota{Known: true, LimitReached: true, Account: "work-codex"}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2})); len(got) != 0 {
		t.Fatalf("a codex spawn ran with the account at its limit: %v", got)
	}
}

// The gate is the codex provider's alone: a claude worker never touches the
// proxy, and refusing it on a proxy it never reaches would stop the fleet for
// a quota it does not spend.
func TestALimitReachedAccountDoesNotStopAClaudeSpawn(t *testing.T) {
	s := offering("t1", false)
	s.Quota = Quota{Known: true, LimitReached: true, Account: "work-codex"}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2})); len(got) != 1 {
		t.Fatalf("a claude spawn was gated on the proxy's quota: %v", got)
	}
}

func TestUsageOverTheThresholdStopsACodexSpawn(t *testing.T) {
	s := offering("t1", true)
	s.Quota = Quota{Known: true, UsedPercent: 91, Account: "work-codex"}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90})); len(got) != 0 {
		t.Fatalf("a codex spawn ran past the threshold: %v", got)
	}
}

func TestUsageUnderTheThresholdSpawns(t *testing.T) {
	s := offering("t1", true)
	s.Quota = Quota{Known: true, UsedPercent: 89, Account: "work-codex"}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 90})); len(got) != 1 {
		t.Fatalf("a codex spawn under the threshold was refused: %v", got)
	}
}

// Unset means no threshold, which is the default an operator who never wrote
// the key gets: the limit_reached flag still gates, and a percentage never
// does.
func TestWithNoThresholdUsageAloneNeverStopsASpawn(t *testing.T) {
	s := offering("t1", true)
	s.Quota = Quota{Known: true, UsedPercent: 99.9, Account: "work-codex"}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2})); len(got) != 1 {
		t.Fatalf("a spawn was gated on a threshold nobody configured: %v", got)
	}
}

// A metered key has no ceiling to be near, and an unreachable proxy left the
// fact unread. Both arrive as an unknown rollup, and neither is a reason to
// stop dispatching.
func TestAnUnknownQuotaNeverStopsASpawn(t *testing.T) {
	s := offering("t1", true)
	s.Quota = Quota{Known: false, LimitReached: true, UsedPercent: 100}
	if got := spawned(Decide(s, Policy{MaxWorkers: 2, MaxUsedPercent: 50})); len(got) != 1 {
		t.Fatalf("an unknown quota stopped a spawn: %v", got)
	}
}

// The refusal is per-task, so a claude task behind a gated codex one still
// gets its slot rather than waiting on a quota it does not spend.
func TestAGatedCodexTaskDoesNotHoldUpTheClaudeTaskBehindIt(t *testing.T) {
	s := Snapshot{
		Ready: []string{"t1", "t2"},
		Tasks: map[string]Task{
			"t1": {ID: "t1", Status: "todo", Project: "/repo", Codex: true},
			"t2": {ID: "t2", Status: "todo", Project: "/repo"},
		},
		Now:   time.Unix(0, 0),
		Quota: Quota{Known: true, LimitReached: true},
	}
	got := spawned(Decide(s, Policy{MaxWorkers: 2}))
	if len(got) != 1 || got[0] != "t2" {
		t.Fatalf("spawned %v, want the claude task alone", got)
	}
}

// The refusal is a sentence an operator reads in `hdis dispatch` and in the
// daemon log, so it has to name what it read rather than saying "quota".
func TestTheRefusalNamesTheAccountAndWhatItRead(t *testing.T) {
	limit := QuotaRefusal(Quota{Known: true, LimitReached: true, Account: "work-codex"}, Policy{})
	if !strings.Contains(limit, "work-codex") || !strings.Contains(limit, "limit") {
		t.Errorf("limit refusal = %q", limit)
	}
	over := QuotaRefusal(Quota{Known: true, UsedPercent: 93.5, Account: "work-codex"}, Policy{MaxUsedPercent: 90})
	if !strings.Contains(over, "93.5") || !strings.Contains(over, "90") {
		t.Errorf("threshold refusal = %q", over)
	}
	if got := QuotaRefusal(Quota{Known: true, UsedPercent: 10}, Policy{MaxUsedPercent: 90}); got != "" {
		t.Errorf("a quota with room refused: %q", got)
	}
}

// Exactly at the threshold is over it: an operator writing 90 means "stop at
// 90", and a spawn that starts there spends past it.
func TestTheThresholdIsReachedNotOnlyPassed(t *testing.T) {
	if QuotaRefusal(Quota{Known: true, UsedPercent: 90}, Policy{MaxUsedPercent: 90}) == "" {
		t.Fatal("a spawn ran at exactly the configured threshold")
	}
}
