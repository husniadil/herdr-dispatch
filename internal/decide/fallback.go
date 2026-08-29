package decide

import (
	"fmt"
	"strings"
)

// MaxFallbackChain is how many profiles one chain may name, the profile the
// routing chose included. It is the core's own guard rather than a trust in
// the document: config refuses a longer chain at parse, and this is what
// stops a table assembled some other way from being walked forever.
//
// It is the same number config.MaxFallbackDepth carries, and
// TestTheCoreAndTheConfigBoundAFallbackChainTheSame holds the two together.
const MaxFallbackChain = 4

// Attempt is one profile a chain tried and the quota that refused it.
type Attempt struct {
	// Profile is the profile that was refused.
	Profile string
	// Account is whose quota refused it, as an operator reads it: the
	// profile's own account, or the serving one the proxy named.
	Account string
	// Figures is what the quota said, for the parenthetical of a note.
	Figures string
	// Why is the whole refusal sentence QuotaRefusal gave, which is what a
	// chain of one — every fleet that names no fallback — is still refused
	// with, word for word.
	Why string
}

// Choice is which profile of a fallback chain a spawn launches through, and
// what the chain had to pass to get there.
type Choice struct {
	// Asked is the profile the routing chose, which is the head of the
	// chain.
	Asked string
	// Eligible is whether the chain found a profile to launch through at
	// all. It is the flag rather than a non-empty Profile because an EMPTY
	// profile name is a launch too: it is the routing asking for the
	// fleet's default, which is what a fleet with no routes and no
	// per-project profile asks for on every task.
	Eligible bool
	// Profile is the profile to launch through, meaningful only where
	// Eligible. An empty one here is the fleet's default, and the caller
	// resolves it exactly as it did before chains existed.
	Profile string
	// Refused is every profile the chain passed over, in chain order.
	Refused []Attempt
}

// FellBackFrom is the profile that was asked for when a quota moved the spawn
// somewhere else, and empty when nothing moved. It is what a binding records
// beside the profile a worker really launched with.
func (c Choice) FellBackFrom() string {
	if !c.Eligible || c.Profile == c.Asked {
		return ""
	}
	return c.Asked
}

// Note is the line an operator reads when a spawn was moved down a chain:
// which profile it launched through, and whose account was spent out. It is
// empty when nothing moved, because a spawn that launched with the profile it
// was asked for is not a finding.
func (c Choice) Note() string {
	if c.FellBackFrom() == "" {
		return ""
	}
	return fmt.Sprintf("launched through %s because %s", c.Profile, reasons(c.Refused))
}

// Refusal is why nothing in the chain could be launched, naming every profile
// and account it tried. It is what AT_QUOTA carries when a fallback chain is
// spent, and empty when something was eligible.
func (c Choice) Refusal() string {
	if c.Eligible || len(c.Refused) == 0 {
		return ""
	}
	if len(c.Refused) == 1 {
		// A chain of one is a fleet with no fallback at all, and it is
		// refused in exactly the words it was before chains existed.
		return c.Refused[0].Why
	}
	return "every profile it would launch through is at quota: " + reasons(c.Refused)
}

// reasons renders the refused attempts as one clause, in chain order.
func reasons(refused []Attempt) string {
	said := make([]string, 0, len(refused))
	for _, a := range refused {
		said = append(said, fmt.Sprintf("%s's account %s is at quota (%s)", a.Profile, a.Account, a.Figures))
	}
	return strings.Join(said, ", and ")
}

// ChooseProfile walks the fallback chain from the profile the routing chose
// and answers the first one a quota does not refuse.
//
// Each step is evaluated against ITS OWN account, which is the whole point of
// a chain: a second codex profile pinned to a second account is a different
// quota, and a claude profile spends no proxy quota at all and is therefore
// always eligible.
//
// A profiles table that does not name the profile is the fleet as it was
// before chains existed: one candidate, gated on codex and the serving
// account. That is what keeps a caller which resolved no table — every tick
// written before this — deciding exactly as it did.
func ChooseProfile(asked string, codex bool, profiles map[string]ProfileFacts, q Quota, p Policy) Choice {
	out := Choice{Asked: asked}
	seen := map[string]bool{}
	for at, step := asked, 0; step < MaxFallbackChain; step++ {
		if step > 0 && at == "" {
			// The chain ran out: the last profile it tried names no
			// fallback, and every profile it did try is in Refused.
			break
		}
		if seen[at] {
			// A cycle the document should never have carried — Parse
			// refuses one — so stopping is the honest answer: what is left
			// of the chain is whatever has already been refused.
			break
		}
		seen[at] = true

		facts, known := profiles[at]
		if !known {
			// A name the table does not carry, which INCLUDES the empty
			// one: that is the routing asking for the fleet's default, and
			// it is what a caller resolving no table at all — every tick
			// written before this — asks for on every task. Such a spawn is
			// gated exactly as it was before chains existed, on the task's
			// own provider and the serving account, and it names no
			// fallback under it.
			facts = ProfileFacts{Codex: codex && at == asked}
		}
		if !facts.Codex {
			// A claude profile spends no proxy quota and is eligible
			// whatever the proxy says.
			out.Eligible, out.Profile = true, at
			return out
		}
		own := q.For(facts.Account)
		why := QuotaRefusal(own, p)
		if why == "" {
			out.Eligible, out.Profile = true, at
			return out
		}
		out.Refused = append(out.Refused, Attempt{
			Profile: at,
			Account: accountName(facts.Account, own),
			Figures: QuotaFigures(own, p),
			Why:     why,
		})
		at = facts.Fallback
	}
	return out
}

// accountName is whose quota a refusal is about, as an operator reads it: the
// profile's own pinned account, the name the proxy reported for the serving
// one, or a phrase where neither is known.
func accountName(pinned string, q Quota) string {
	if pinned != "" {
		return pinned
	}
	if q.Account != "" {
		return q.Account
	}
	return "the account the proxy serves"
}
