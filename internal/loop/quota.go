package loop

import (
	"context"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/htask"
)

// launchesThroughProxy reports whether the worker this row would get routes
// through the proxy. Only those spend the account the quota gate reads; a
// claude worker never touches it.
//
// It asks of the profile the row's PRIORITY routes to, not of the project's
// own: a route may send a high-priority row to a codex profile the project
// would never otherwise have used, and gating that spawn on the project's
// answer would let it past the quota gate entirely.
func (l *Loop) launchesThroughProxy(row htask.Task) bool {
	name := decide.RouteProfile(row.Priority, l.Config.ProfileNameFor(row.Project), l.Policy.Routes)
	p, err := l.Config.ProfileNamed(name)
	if err != nil {
		// A project whose profile is not defined has a spawn that will fail
		// on its own, with a message about the profile. Calling it a codex
		// one here would refuse it for the wrong reason.
		return false
	}
	return p.Provider == config.ProviderCodex
}

// profileFacts is the config's profile table as the core reads it: which
// profiles spend the proxy's quota, which account each spends, and where a
// spawn a quota refuses goes instead. It is resolved here because the core
// reads no config, the same way Routes is.
func (l *Loop) profileFacts() map[string]decide.ProfileFacts {
	if len(l.Config.Profiles) == 0 {
		return nil
	}
	out := make(map[string]decide.ProfileFacts, len(l.Config.Profiles))
	for name, p := range l.Config.Profiles {
		out[name] = decide.ProfileFacts{
			Codex:    p.Provider == config.ProviderCodex,
			Account:  p.Account,
			Fallback: p.Fallback,
		}
	}
	return out
}

// choose is the fallback chain for a row, walked against the quota as it
// stands now: which profile the row's worker would launch through, and what
// the chain had to pass to get there.
func (l *Loop) choose(q decide.Quota, row htask.Task) decide.Choice {
	asked := decide.RouteProfile(row.Priority, l.Config.ProfileNameFor(row.Project), l.Policy.Routes)
	return decide.ChooseProfile(asked, l.launchesThroughProxy(row), l.profileFacts(), q, l.Policy)
}

// anyProfileLaunchesThroughProxy reports whether any configured profile is a
// codex one. It is what keeps a claude-only fleet from shelling out to a
// binary no worker it launches touches, once per tick, forever.
func (l *Loop) anyProfileLaunchesThroughProxy() bool {
	for _, p := range l.Config.Profiles {
		if p.Provider == config.ProviderCodex {
			return true
		}
	}
	return false
}

// quota asks the proxy what the serving account has spent, once per tick.
//
// Fail loud, idle safe: a proxy that cannot be asked leaves the fact unknown
// and says so in the log, and an unknown quota gates nothing. A proxy that is
// really down still fails the spawn at step zero, where the message is the
// proxy's own — this is not the place that discovers it.
func (l *Loop) quota(ctx context.Context) decide.Quota {
	if !l.anyProfileLaunchesThroughProxy() {
		return decide.Quota{}
	}
	if l.Spawn == nil || l.Spawn.Proxy == nil {
		return decide.Quota{}
	}
	u, err := l.Spawn.Proxy.Usage(ctx)
	if err != nil {
		l.logf("cannot ask the proxy what the account has spent, so no spawn is gated on quota this tick: %v", err)
		return decide.Quota{}
	}
	q := decide.Quota{
		Known:        u.Known,
		LimitReached: u.LimitReached,
		UsedPercent:  u.UsedPercent,
		Account:      u.Account,
		Plan:         u.Plan,
	}
	// The per-account figures out of the same one call, for the profiles
	// that name an account of their own. A chain whose steps sit on
	// different accounts is still one process per tick.
	if len(u.Accounts) > 0 {
		q.Accounts = make(map[string]decide.Quota, len(u.Accounts))
		for name, one := range u.Accounts {
			q.Accounts[name] = decide.Quota{
				Known:        one.Known,
				LimitReached: one.LimitReached,
				UsedPercent:  one.UsedPercent,
				Account:      one.Account,
				Plan:         one.Plan,
			}
		}
	}
	return q
}

// quotaRefusal is why a worker for this row must not be brought up now,
// or empty when it may. It is the same gate the tick applies, asked on the
// dispatch verb's path so a caller is told quota rather than left waiting for
// a pane that the next tick will not bring up either.
func (l *Loop) quotaRefusal(ctx context.Context, row htask.Task) string {
	if !l.launchesThroughProxy(row) {
		return ""
	}
	// The whole chain, not the routed profile alone: a spawn the routed
	// profile's account cannot pay for is not refused while a fallback
	// still has room, and the tick that runs it would launch it anyway.
	return l.choose(l.quota(ctx), row).Refusal()
}

// Quota is the proxy's word about the account, as doctor asks for it. It is
// the same read the tick makes, and it answers a zero Quota — an unknown one
// — where no configured profile launches through the proxy, because there is
// then no account this dispatcher spends.
func (l *Loop) Quota(ctx context.Context) decide.Quota { return l.quota(ctx) }

// QuotaRefusal is what a codex spawn would be refused with right now, or
// empty when none would be. doctor prints it so an operator whose fleet has
// stopped reads the reason without having to try a dispatch.
func (l *Loop) QuotaRefusal(q decide.Quota) string { return decide.QuotaRefusal(q, l.Policy) }
