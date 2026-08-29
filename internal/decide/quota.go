package decide

import (
	"fmt"
	"strconv"
)

// Quota is what the proxy launcher says about the account a codex worker
// would spend, as the core reads it. It is a FACT collected before a tick,
// like a board row or a pane state: nothing here asks the proxy anything.
//
// It is the proxy's TOP-LEVEL rollup for the serving account and not one
// entry of its accounts array. The rollup answers exactly the question the
// gate asks — can the account this dispatcher's codex workers route through
// pay for another one — and asking it skips picking an account.
type Quota struct {
	// Known is whether there is a ceiling to read at all. A metered key has
	// none, and a proxy that could not be reached left the fact unread.
	// Both arrive here as false, and neither gates anything: an unknown
	// quota is not a reason to stop dispatching.
	Known bool
	// LimitReached is the rollup's own flag: the account cannot pay now.
	LimitReached bool
	// UsedPercent is the HIGHEST used_percent across the rollup's windows.
	// The rollup can carry more than one window and gives no per-window
	// threshold semantics, so the fullest window is the one the account is
	// nearest to being stopped by.
	UsedPercent float64
	// Account is the stored account the rollup is about, so a refusal names
	// whose quota it read.
	Account string
	// Plan is the serving account's plan, carried for doctor to print. It
	// gates nothing.
	Plan string
	// Accounts is the same facts for every account the proxy holds, keyed
	// by stored name. A profile that names no account spends the serving
	// one and is gated on the rollup above; a profile pinned with
	// `account = "..."` spends that account, and its figures are in here.
	//
	// A name that is not in the map is a quota nothing could read, and an
	// unread quota gates nothing — same as a rollup that came back
	// unknown. doctor is where a name the proxy does not hold is reported.
	Accounts map[string]Quota
}

// For is the quota the gate reads for a profile pinned to `account`: that
// account's own figures, or the serving rollup where the profile names none.
//
// An account the proxy did not report answers an unknown quota, which gates
// nothing. Refusing there would stop a fleet on a fact nobody read, and the
// name is already a doctor finding under missing_accounts.
func (q Quota) For(account string) Quota {
	if account == "" {
		return q
	}
	one, ok := q.Accounts[account]
	if !ok {
		return Quota{}
	}
	return one
}

// QuotaRefusal is why a codex spawn must not run against this quota now, or
// empty when it may. It is a sentence rather than a flag because it is what
// `hdis dispatch` refuses with and what the daemon writes to its log, and
// "quota" alone tells an operator nothing they can act on.
//
// Only the codex provider is gated, and that is the caller's part: this
// answers about the quota alone. A claude worker never touches the proxy.
func QuotaRefusal(q Quota, p Policy) string {
	if !q.Known {
		return ""
	}
	who := q.Account
	if who == "" {
		who = "the account the proxy serves"
	}
	if q.LimitReached {
		return fmt.Sprintf("the proxy reports %s at its limit", who)
	}
	// Unset is no threshold, which is what an operator who never wrote the
	// key has: limit_reached still stops a spawn and a percentage never
	// does.
	if p.MaxUsedPercent <= 0 {
		return ""
	}
	if q.UsedPercent < float64(p.MaxUsedPercent) {
		return ""
	}
	return fmt.Sprintf("the proxy reports %s at %s%% of its window, and max_used_percent is %d",
		who, strconv.FormatFloat(q.UsedPercent, 'g', -1, 64), p.MaxUsedPercent)
}

// QuotaFigures is what a quota reads as in one parenthetical, for a sentence
// that has already named the account: the proxy's own flag, or the share
// spent against the threshold. Empty where there is nothing measured to say.
//
// It is deliberately not QuotaRefusal: that is a whole sentence naming the
// account, and a note that has just named the account twice reads as two
// findings rather than one.
func QuotaFigures(q Quota, p Policy) string {
	if !q.Known {
		return "no quota could be read"
	}
	if q.LimitReached {
		return "at its limit"
	}
	spent := strconv.FormatFloat(q.UsedPercent, 'g', -1, 64) + "% of its window"
	if p.MaxUsedPercent > 0 {
		spent += fmt.Sprintf(", max_used_percent %d", p.MaxUsedPercent)
	}
	return spent
}
