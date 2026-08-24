package loop

import (
	"context"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
)

// launchesThroughProxy reports whether the workers a project's profile
// launches route through the proxy. Only those spend the account the quota
// gate reads; a claude worker never touches it.
func (l *Loop) launchesThroughProxy(project string) bool {
	p, err := l.Config.ProfileFor(project)
	if err != nil {
		// A project whose profile is not defined has a spawn that will fail
		// on its own, with a message about the profile. Calling it a codex
		// one here would refuse it for the wrong reason.
		return false
	}
	return p.Provider == config.ProviderCodex
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
	return decide.Quota{
		Known:        u.Known,
		LimitReached: u.LimitReached,
		UsedPercent:  u.UsedPercent,
		Account:      u.Account,
		Plan:         u.Plan,
	}
}

// quotaRefusal is why a worker for this project must not be brought up now,
// or empty when it may. It is the same gate the tick applies, asked on the
// dispatch verb's path so a caller is told quota rather than left waiting for
// a pane that the next tick will not bring up either.
func (l *Loop) quotaRefusal(ctx context.Context, project string) string {
	if !l.launchesThroughProxy(project) {
		return ""
	}
	return decide.QuotaRefusal(l.quota(ctx), l.Policy)
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
