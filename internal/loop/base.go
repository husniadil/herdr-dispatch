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

// EnsureBase is Base, and when there is none it asks Herdr for a pane to
// adopt as one.
//
// A daemon started inside a Herdr pane inherits its base through
// HERDR_PANE_ID and never reaches here. A daemon Herdr's own plugin manager
// started at boot has neither that nor a pane in its config — pane ids are
// not durable across Herdr restarts, so naming one there would be wrong the
// first time the machine came back — and without this it would answer both
// doors and refuse every dispatch with NO_BASE_PANE for as long as it ran.
//
// It is asked again each time rather than once at start, because the boot
// order is not this daemon's to choose: the plugin's start script can run
// before Herdr has opened a single pane, and the pane the operator is about
// to sit in does not exist yet. An adoption that finds nothing costs one
// `pane list` per tick and is reported once.
//
// Nothing is opened here. Adopting means naming a pane that already exists;
// a dispatcher that made itself a pane would put one on the operator's
// screen that nobody asked for.
func (l *Loop) EnsureBase(ctx context.Context) string {
	if pane := l.Base(); pane != "" {
		return pane
	}
	panes, err := l.Herdr.PaneList(ctx)
	if err != nil {
		l.logf("no base pane, and herdr could not be asked for one: %v", err)
		return ""
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
	l.logf("adopted pane %s as this daemon's base: it was started without one", pane)
	return pane
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
