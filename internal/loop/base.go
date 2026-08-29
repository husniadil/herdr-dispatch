package loop

import (
	"context"
	"sort"

	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
)

// Base is the pane worker panes are split off, as this daemon believes it
// now. It is read from more than one goroutine — the tick's, and whichever
// door a verb came in on — so it is never read off the field directly.
func (l *Loop) Base() string {
	l.baseMu.Lock()
	defer l.baseMu.Unlock()
	return l.BasePane
}

// EnsureBase is Base when the base is a pane Herdr still holds, and otherwise
// asks Herdr for one to adopt in its place.
//
// A daemon started inside a Herdr pane inherits its base through
// HERDR_PANE_ID. A daemon Herdr's own plugin manager started at boot has
// neither that nor a pane in its config — pane ids are not durable across
// Herdr restarts, so naming one there would be wrong the first time the
// machine came back — and without this it would answer both doors and refuse
// every dispatch with NO_BASE_PANE for as long as it ran.
//
// A base that has GONE is the same problem arriving later, and it is why the
// pane list is read even when a base is already held. Measured on 2026-08-29:
// a worker in bypass mode ran `herdr workspace close w3`, which took this
// daemon's own base pane with it, and hdis went on reporting `base_pane w3:p1`
// while `herdr pane list` had no w3 at all. Nothing refused — the base is only
// read for the workspace a worker's tab opens in, so every spawn after that
// landed wherever Herdr chose and said nothing — which is a placement nobody
// asked for and nobody could see. Re-adopting is what makes the recorded base
// the pane placement will actually use.
//
// It is asked again each time rather than once at start, because the boot
// order is not this daemon's to choose either: the plugin's start script can
// run before Herdr has opened a single pane. Both questions cost the same one
// `pane list` per tick, and the tab list is read only when one is being
// adopted.
//
// A Herdr that cannot be reached changes nothing. "The pane list did not come
// back" is not "the pane is gone", and dropping a live base on that reading
// would refuse every dispatch until Herdr answered again.
//
// Nothing is opened here. Adopting means naming a pane that already exists;
// a dispatcher that made itself a pane would put one on the operator's
// screen that nobody asked for.
func (l *Loop) EnsureBase(ctx context.Context) string {
	held := l.Base()
	panes, err := l.Herdr.PaneList(ctx)
	if err != nil {
		if held == "" {
			l.logf("no base pane, and herdr could not be asked for one: %v", err)
		}
		return held
	}
	if held != "" {
		if livePane(panes, held) {
			return held
		}
		l.logf("the base pane %s is gone from herdr, so this daemon has none until one is adopted: "+
			"every worker tab was opening in that pane's workspace, and nothing would have said otherwise", held)
		l.forgetBase(held)
	}

	tabs, err := l.Herdr.Tabs(ctx)
	if err != nil {
		// The tab labels are what keep a worker pane from being adopted.
		// Without them the safe answer is to adopt nothing and ask again.
		l.logf("no base pane, and herdr could not say which tabs are this dispatcher's own: %v", err)
		return ""
	}
	l.mu.Lock()
	bound := make(map[string]bool, len(l.bindings))
	for _, b := range l.bindings {
		bound[b.Pane] = true
	}
	l.mu.Unlock()

	pane := pickBase(panes, tabs, bound)
	if pane == "" {
		return ""
	}

	l.baseMu.Lock()
	defer l.baseMu.Unlock()
	// Another goroutine may have adopted while this one was asking. The
	// first answer wins: two base panes for one daemon is the bug this
	// guard exists for.
	if l.BasePane != "" {
		return l.BasePane
	}
	l.BasePane = pane
	l.logf("adopted pane %s as this daemon's base", pane)
	return pane
}

// BaseLive reports whether Herdr still holds this daemon's base pane, and
// whether it could be asked at all. Doctor prints the pair: a base nothing
// answers for is a different fact from one Herdr says is gone, and an operator
// reading `base_pane w3:p1` beside an empty `pane list` deserves to be told
// which they are looking at.
func (l *Loop) BaseLive(ctx context.Context) (live, known bool) {
	pane := l.Base()
	if pane == "" || l.Herdr == nil {
		return false, false
	}
	panes, err := l.Herdr.PaneList(ctx)
	if err != nil {
		return false, false
	}
	return livePane(panes, pane), true
}

// forgetBase drops a base pane Herdr no longer holds, and only that one: a
// goroutine that adopted a replacement while this one was reading must not
// have it taken away again.
func (l *Loop) forgetBase(gone string) {
	l.baseMu.Lock()
	defer l.baseMu.Unlock()
	if l.BasePane == gone {
		l.BasePane = ""
	}
}

// livePane reports whether Herdr listed a pane.
func livePane(panes []herdrclient.Agent, id string) bool {
	for _, p := range panes {
		if p.PaneID == id {
			return true
		}
	}
	return false
}

// pickBase is the choice itself, made from Herdr's own facts alone: the
// lowest live pane id that is neither a worker of this dispatcher's nor
// sitting in a tab it opened.
//
// The LOWEST id is the rule for the reason spawn.inProject picks one — it is
// stable, so the same screen resolves to the same base every time it is
// asked, and a daemon that adopts on its fifth tick lands where it would
// have on its first.
func pickBase(panes []herdrclient.Agent, tabs []herdrclient.Tab, bound map[string]bool) string {
	ours := make(map[string]bool, len(tabs))
	for _, t := range tabs {
		if spawn.OwnTab(t.Label) {
			ours[t.TabID] = true
		}
	}
	ids := make([]string, 0, len(panes))
	for _, p := range panes {
		if p.PaneID == "" || bound[p.PaneID] || ours[p.TabID] {
			continue
		}
		ids = append(ids, p.PaneID)
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return ids[0]
}
