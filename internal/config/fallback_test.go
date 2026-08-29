package config

import (
	"strings"
	"testing"
)

// A chain that can be followed is accepted whole, and FallbackChain answers
// it in the order a spawn walks it, the profile that starts it first.
func TestAFallbackChainThatCanBeFollowedIsAcceptedAndWalkedInOrder(t *testing.T) {
	c, err := Parse([]byte(`default = "routed"

[profiles.routed]
provider = "codex"
account = "work-codex"
fallback = "spare"

[profiles.spare]
provider = "codex"
account = "spare-codex"
fallback = "worker"

[profiles.worker]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("a chain that can be followed was refused: %v", err)
	}
	if got := c.Profiles["routed"].Fallback; got != "spare" {
		t.Fatalf("routed falls back to %q, want spare", got)
	}
	got := strings.Join(c.FallbackChain("routed"), " -> ")
	if got != "routed -> spare -> worker" {
		t.Fatalf("the chain reads %q", got)
	}
	// A profile naming no fallback is a chain of one, which is what every
	// document written before the key had.
	if got := strings.Join(c.FallbackChain("worker"), " -> "); got != "worker" {
		t.Fatalf("a profile with no fallback is a chain of %q", got)
	}
}

// A fallback naming a profile nobody defined is refused HERE, at startup, and
// not hours later on the first task a quota stops.
func TestAFallbackNamingAnUndefinedProfileIsRefusedAtParse(t *testing.T) {
	_, err := Parse([]byte(`default = "routed"

[profiles.routed]
provider = "codex"
fallback = "spair"
`))
	if err == nil {
		t.Fatal("a fallback to a profile that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "spair") || !strings.Contains(err.Error(), "routed") {
		t.Fatalf("the refusal names neither the profile nor the fallback: %v", err)
	}
}

// A chain that comes back to a profile it has already tried never ends, so it
// is refused rather than walked.
func TestAFallbackChainThatCyclesIsRefusedAtParse(t *testing.T) {
	_, err := Parse([]byte(`default = "routed"

[profiles.routed]
provider = "codex"
fallback = "spare"

[profiles.spare]
provider = "codex"
fallback = "routed"
`))
	if err == nil {
		t.Fatal("a fallback chain that loops was accepted")
	}
	if !strings.Contains(err.Error(), "routed") || !strings.Contains(err.Error(), "spare") {
		t.Fatalf("the refusal does not name the profiles in the loop: %v", err)
	}
}

// A profile falling back to itself is the shortest cycle there is, and it is
// refused for the same reason.
func TestAProfileFallingBackToItselfIsRefusedAtParse(t *testing.T) {
	_, err := Parse([]byte(`default = "routed"

[profiles.routed]
provider = "codex"
fallback = "routed"
`))
	if err == nil {
		t.Fatal("a profile falling back to itself was accepted")
	}
}

// MaxFallbackDepth profiles are followed and one more is not: a chain nobody
// bounded is an operator's typo turned into a tick spent deciding.
func TestAFallbackChainDeeperThanTheBoundIsRefusedAtParse(t *testing.T) {
	at := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString("[profiles.p" + string(rune('0'+i)) + "]\nprovider = \"codex\"\n")
			if i < n {
				b.WriteString("fallback = \"p" + string(rune('0'+i+1)) + "\"\n")
			}
			b.WriteString("\n")
		}
		return "default = \"p1\"\n\n" + b.String()
	}
	if _, err := Parse([]byte(at(MaxFallbackDepth))); err != nil {
		t.Fatalf("a chain of exactly %d was refused: %v", MaxFallbackDepth, err)
	}
	_, err := Parse([]byte(at(MaxFallbackDepth + 1)))
	if err == nil {
		t.Fatalf("a chain of %d was accepted", MaxFallbackDepth+1)
	}
	if !strings.Contains(err.Error(), "p1 -> p2") {
		t.Fatalf("the refusal does not print the chain it walked: %v", err)
	}
}

// Empty is what every document written before the key had, and it must go on
// parsing and behaving as it did.
func TestAProfileNamingNoFallbackIsAcceptedUnchanged(t *testing.T) {
	c, err := Parse([]byte(`default = "worker"

[profiles.worker]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("a document naming no fallback was refused: %v", err)
	}
	if got := c.Profiles["worker"].Fallback; got != "" {
		t.Fatalf("a profile naming no fallback carries %q", got)
	}
}
