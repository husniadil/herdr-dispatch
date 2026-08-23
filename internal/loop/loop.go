// Package loop is the dispatcher's tick: collect a snapshot of the facts,
// hand it to the pure decision core, and execute what comes back.
//
// The bindings are the only state the dispatcher owns, and they are written
// to the plugin's own store on every change so a restart does not forget a
// worker it prompted. Everything else it might want to remember is a board
// fact or a Herdr fact, and both are read fresh every tick.
package loop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdr"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// Trees is the checkouts this dispatcher hands out, as the loop needs them.
// worktree.Manager is the one implementation; a test stands in front of it
// to record what it was asked for.
type Trees interface {
	// Worker checks the project out on a branch named for the task and
	// returns the directory and the branch.
	Worker(ctx context.Context, project string, seq int) (string, string, error)
	// Behind reports whether the project's HEAD has moved past the branch,
	// which is the state that refuses a fast-forward merge.
	Behind(ctx context.Context, project, branch string) (bool, error)
	// Remove takes a checkout and git's record of it.
	Remove(ctx context.Context, dir string) error
	// Project is the repository a directory belongs to, with a worktree
	// naming the repository it was cut from. It is how a restart learns
	// which board a pane's task is filed on.
	Project(ctx context.Context, dir string) (string, error)
	// RootDir is the directory the checkouts are made under, which is what
	// bounds the reap.
	RootDir() string
}

// Loop holds the adapters, the policy, and the bindings.
type Loop struct {
	Board  *htask.Client
	Herdr  *herdr.Client
	Spawn  *spawn.Pipeline
	Config config.Config
	Policy decide.Policy
	// Worktrees hands each pane the checkout it works in. Nothing is
	// spawned without one: an agent with nowhere of its own to work would
	// have to work in the tree the operator sits in.
	//
	// It is an interface so a test can see the ARGUMENTS a spawn passes,
	// above all which commit a verifier is sent to read. That choice is made
	// here, and a pin one layer down only proves the manager honours
	// whatever it is handed.
	Worktrees Trees
	// Store is where the bindings outlive the process. A nil Store keeps
	// them in memory only, which is a test that does not care and never a
	// running daemon.
	Store *store.Bindings

	// BasePane is the pane worker panes are split off, normally the
	// dispatcher's own.
	BasePane string
	// Now is time.Now unless a test replaces it.
	Now func() time.Time
	// Log is where the operator hears about anything that went wrong.
	Log *log.Logger

	// mu guards everything below it. The tick runs on the daemon's own
	// goroutine while dispatch and status answer on a door's, so the
	// bindings are read and written from more than one at a time. Nothing
	// slow is done under this lock: a spawn runs to minutes, and it runs
	// with the lock released.
	mu       sync.Mutex
	bindings []decide.Binding
	// pending is the reservations an on-demand dispatch took and no tick has
	// spawned for yet. A reservation is what keeps the watching loop and the
	// dispatch verb from both taking the same task, and it carries the
	// daemon that made it so a restart can tell its own from a peer's.
	pending []store.Reservation
	// readopted is how many persisted bindings the last Adopt kept, for
	// doctor to report.
	readopted int
	// unadopted is set when an Adopt failed, and it is what keeps a
	// dispatcher that never reconciled from acting as though it had.
	//
	// The failure it exists for is Herdr being unreachable at start: Adopt
	// returns early with nothing adopted, so the in-memory set is empty
	// while the store still names every live worker. Left alone, the next
	// tick dispatches on that empty set and the first spawn writes it over
	// the store — a task already prompted and not yet claimed gets a second
	// worker, and the record of the first is gone.
	//
	// So while it is set nothing is saved and no tick dispatches; each tick
	// tries the Adopt again instead, and clears it when one succeeds.
	unadopted bool
	// rows is the last tick's board rows, by task id: the project a worker
	// runs in, the number an operator reads, the title status prints. It is
	// a cache of board facts and never a source of them.
	rows map[string]htask.Task
}

// Tick runs one round. A board that cannot be read fails the tick loudly and
// spawns nothing; a single action that fails is logged and the rest go on.
func (l *Loop) Tick(ctx context.Context) error {
	if l.needsAdopt() {
		if _, err := l.Adopt(ctx); err != nil {
			return fmt.Errorf("nothing is dispatched until a start-up reconciliation succeeds: %w", err)
		}
	}
	snap, err := l.snapshot(ctx)
	if err != nil {
		return err
	}
	l.apply(ctx, decide.Decide(snap, l.Policy))
	return nil
}

// Adopt reconciles the dispatcher with reality, once, before the first tick.
//
// It reasons from two facts read now: every live pane this daemon opened,
// and the board row each of those panes is working. Of each pane it asks one
// question — what is this, and what still needs doing — and the answer is
// the whole of the restart policy. A pane whose row is still live is
// adopted, whether or not a binding survived to name it; a pane whose row is
// finished, gone, or no longer its own is retired or let go. The shapes of
// restart debris are consequences of that question, not cases to enumerate.
//
// The persisted bindings are a hint and never the frame. What they carry
// that Herdr cannot — how often a goal was delivered and when, whether
// review was announced, which checkout a verifier was given — is kept on the
// pane it names; a binding whose pane is gone is dropped, because the pane
// was the thing it was about.
//
// Herdr being unreachable is different from a pane being gone, and adopting
// on that guess would hand a live worker's task to a second pane. Nothing is
// adopted, the failure is loud, and the store is left for the next start.
func (l *Loop) Adopt(ctx context.Context) (int, error) {
	var held store.State
	if l.Store != nil {
		var err error
		held, err = l.Store.Load()
		if err != nil {
			// A store that cannot be read is a dispatcher that has forgotten,
			// which is where it was before any of this. It is not a reason to
			// refuse to start.
			l.logf("the bindings could not be read, starting with none: %v", err)
			held = store.State{}
		}
	}

	rowsAlive, err := l.Herdr.PaneList(ctx)
	if err != nil {
		l.markUnadopted()
		return 0, fmt.Errorf("herdr cannot say which panes are alive, so %d persisted binding(s) stay unadopted: %w", len(held.Bindings), err)
	}
	panes := make(map[string]string, len(rowsAlive))
	for _, row := range rowsAlive {
		panes[row.PaneID] = row.Status
	}
	agents, err := l.Herdr.Agents(ctx)
	if err != nil {
		l.markUnadopted()
		return 0, fmt.Errorf("herdr cannot say which agents it has registered, so %d persisted binding(s) stay unadopted: %w", len(held.Bindings), err)
	}
	// The tab labels are the second evidence of ownership, and the one that
	// does not depend on Herdr still holding an agent name. A tab list that
	// cannot be read costs only that evidence: the names and the bindings
	// still answer, so the restart goes on rather than refusing.
	tabs, err := l.Herdr.Tabs(ctx)
	if err != nil {
		l.logf("herdr cannot list its tabs, so a pane is this daemon's only if its agent name or a binding says so: %v", err)
	}

	kept := make([]decide.Binding, 0, len(held.Bindings))
	rows := make(map[string]htask.Task, len(held.Bindings))
	for _, p := range l.ourPanes(ctx, rowsAlive, agents, tabs, held.Bindings) {
		b, row, ok := l.reconcile(ctx, p)
		if !ok {
			continue
		}
		if row.ID != "" {
			rows[row.ID] = row
		}
		kept = append(kept, b)
	}

	// A reservation is this daemon's own intent, and only its own: a record
	// naming another daemon is a peer's and is dropped rather than acted on.
	mine := make([]store.Reservation, 0, len(held.Reservations))
	for _, r := range held.Reservations {
		if r.Owner != "" && r.Owner != l.principal() {
			l.logf("task %s: the reservation a restart found was made by %s, not by this daemon; leaving it", r.TaskID, r.Owner)
			continue
		}
		mine = append(mine, r)
	}

	// The tabs the previous process opened go back to the pipeline before
	// anything else: a retire that comes out of this reconciliation has to
	// know which tab a pane is in, and only the store still says.
	if l.Spawn != nil {
		for _, b := range kept {
			l.Spawn.Adopt(b.Pane, b.Tab)
		}
	}

	l.mu.Lock()
	l.bindings = kept
	l.rows = rows
	l.pending = mine
	l.readopted = len(kept)
	// Cleared before the save, because the save is what a failed Adopt
	// suppresses and this reconciliation is the one that earned it.
	l.unadopted = false
	l.saveLocked()
	l.mu.Unlock()
	if l.Store != nil && (len(held.Bindings) > 0 || len(held.Reservations) > 0) {
		l.logf("re-adopted %d of %d persisted binding(s) and %d of %d reservation(s) from %s",
			len(kept), len(held.Bindings), len(mine), len(held.Reservations), l.Store.Path)
	}

	// The panes were only ever half the restart window. The other half is
	// what this daemon holds on the board and no pane is working: a task
	// reserved and never spawned for, and a checkout under this daemon's own
	// state dir. Both are resolved here, on facts read now.
	l.release(ctx)
	l.reap(ctx)
	return len(kept), nil
}

// ourPane is one live pane this daemon opened, and what is known about it:
// the lane it was opened for, the board row it is working, and the binding
// that survived to name it, if one did.
type ourPane struct {
	pane string
	kind string
	// ref is how the board is asked about it: a task id when a binding
	// carries one, otherwise the task number the agent registered under.
	ref string
	// project is the board the number is unique on, read off the checkout
	// the pane works in. It is empty, and unused, when ref is an id: an id
	// belongs to no project and is read across all of them.
	project string
	// tab is the tab the pane sits in, when it is one this dispatcher
	// opened. A pane in the operator's own tab carries none, so nothing a
	// restart does can close it.
	tab     string
	binding *decide.Binding
}

// ourPanes is every live pane this daemon opened, in a stable order.
//
// A pane is this daemon's on either of two evidences, and it needs only one.
// Herdr knows the name the agent registered under, and this daemon's names
// carry the task number — that is what finds a worker no binding was ever
// written for. The bindings cover the other window: a pane split seconds ago,
// whose agent has not registered yet and which `agent list` does not mention.
//
// A binding whose pane is gone names nothing to reconcile, and is dropped
// here with a word to the operator.
func (l *Loop) ourPanes(ctx context.Context, alive []herdr.Agent, agents []herdr.Agent, tabs []herdr.Tab, held []decide.Binding) []ourPane {
	panes := make(map[string]bool, len(alive))
	for _, row := range alive {
		panes[row.PaneID] = true
	}
	// Which live panes sit in a tab this dispatcher opened. A tab the
	// operator made is absent, so a binding built here can never name one.
	label := make(map[string]string, len(tabs))
	for _, t := range tabs {
		label[t.TabID] = t.Label
	}
	ourTab := make(map[string]string, len(alive))
	for _, row := range alive {
		if spawn.OwnTab(label[row.TabID]) {
			ourTab[row.PaneID] = row.TabID
		}
	}
	out := make([]ourPane, 0, len(agents))
	at := make(map[string]int, len(agents))
	for _, a := range l.named(alive, agents, tabs) {
		kind, seq, ok := lane(a.Name)
		if !ok || a.PaneID == "" {
			continue
		}
		if !panes[a.PaneID] {
			continue
		}
		// A pane names its task by number, and a number is unique only
		// inside a project — so the project comes with it. It is git's
		// answer about the checkout the pane works in, which holds for a
		// worker's worktree, a verifier's detached one, and a pane opened
		// before either existed and still sitting in the project itself.
		project, err := l.project(ctx, a.Cwd)
		if err != nil {
			l.logf("pane %s: nothing names the project task %d is filed on, so it is left as it is: %v", a.PaneID, seq, err)
			continue
		}
		at[a.PaneID] = len(out)
		out = append(out, ourPane{pane: a.PaneID, kind: kind, ref: strconv.Itoa(seq), project: project, tab: ourTab[a.PaneID]})
	}
	for i := range held {
		b := held[i]
		if !panes[b.Pane] {
			l.logf("task %s: pane %s is gone, dropping the binding a restart found", b.TaskID, b.Pane)
			continue
		}
		if j, seen := at[b.Pane]; seen {
			out[j].binding = &b
			out[j].ref = b.TaskID
			out[j].project = ""
			out[j].kind = bindingKind(b)
			continue
		}
		at[b.Pane] = len(out)
		out = append(out, ourPane{pane: b.Pane, kind: bindingKind(b), ref: b.TaskID, binding: &b})
	}
	return out
}

// named is every live pane carrying one of this daemon's own names, from
// either evidence, with each pane appearing once.
//
// Herdr's agent name is the first evidence and the cheapest one: it says the
// lane and the task number outright. The CHECKOUT is the second, and it
// exists because the first can vanish while the work does not — Herdr drops
// the agent name when the client it registered exits, and a pane whose agent
// is gone but whose task is still live is a pane this daemon opened and still
// owns. See nameFor, which is where the checkout is read.
//
// The tab label is NOT an evidence here. A tab holds several workers and its
// label names only the task it was opened for, so reading a pane's task off
// its tab would give every worker in that tab the first one's number. The
// label decides which TAB is this daemon's to close, never which task a pane
// is on.
//
// A pane covered by both evidences is counted once, from the agent record.
func (l *Loop) named(alive []herdr.Agent, agents []herdr.Agent, tabs []herdr.Tab) []herdr.Agent {
	out := make([]herdr.Agent, 0, len(agents)+len(alive))
	seen := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.PaneID == "" || seen[a.PaneID] {
			continue
		}
		if _, _, ok := lane(a.Name); !ok {
			continue
		}
		seen[a.PaneID] = true
		out = append(out, a)
	}

	label := make(map[string]string, len(tabs))
	for _, t := range tabs {
		label[t.TabID] = t.Label
	}
	for _, row := range alive {
		if row.PaneID == "" || seen[row.PaneID] {
			continue
		}
		name := l.nameFor(row, label[row.TabID])
		if name == "" {
			continue
		}
		seen[row.PaneID] = true
		row.Name = name
		out = append(out, row)
	}
	return out
}

// nameFor is this daemon's own name for a live pane Herdr no longer gives a
// name to, from the two evidences that are not Herdr's to drop.
//
// The CHECKOUT is the stronger of the two and it is tried first. Every agent
// this dispatcher brings up is given a directory of its own under this
// daemon's state dir, named for its lane and its task — nothing else on the
// machine writes there, so a pane sitting in one is a pane this daemon opened,
// and the directory's own name says which task and which lane. It is Herdr's
// word about the pane's cwd and never Herdr's word about an agent, which is
// the field that was measured going missing.
//
// The tab LABEL deliberately does not serve here. A tab holds up to
// config.DefaultMaxPanesPerTab workers and its label names only the task it
// was opened for, so reading a pane's task off the tab it happens to sit in
// would give every worker in that tab the first one's number. The label is
// the close guard and the operator's signpost; the checkout is the identity.
func (l *Loop) nameFor(row herdr.Agent, label string) string {
	if l.Worktrees == nil {
		return ""
	}
	root := l.Worktrees.RootDir()
	if root == "" || row.Cwd == "" {
		return ""
	}
	rel, err := filepath.Rel(root, row.Cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	name, ok := nameOfCheckout(rel)
	if !ok {
		return ""
	}
	if !spawn.OwnTab(label) && label != "" {
		// The checkout says the pane is ours and the tab says it is the
		// operator's. That is a worker the operator has moved, which is
		// theirs to arrange; it is still ours to drive.
		l.logf("pane %s works in this daemon's own checkout %s but sits in the operator's tab %q; driving it and leaving the tab alone", row.PaneID, row.Cwd, label)
	}
	return name
}

// nameOfCheckout reads one of this daemon's own checkout directory names —
// `hdis-work-<seq>-<random>` — back into the agent name that task would have
// registered under. It is the mirror of the reap, which already removes a
// checkout under this root that no binding names; this recognises the pane
// sitting in one.
func nameOfCheckout(rel string) (string, bool) {
	dir, _, _ := strings.Cut(rel, string(filepath.Separator))
	rest, ok := strings.CutPrefix(dir, worktree.WorkPrefix)
	if !ok {
		return "", false
	}
	digits, _, _ := strings.Cut(rest, "-")
	seq, err := strconv.Atoi(digits)
	if err != nil || seq <= 0 {
		return "", false
	}
	return workerName(seq), true
}

// reconcile asks the one question of one live pane: what is it, and what
// still needs doing. It returns the binding to keep, the row behind it, and
// whether anything is kept at all.
func (l *Loop) reconcile(ctx context.Context, p ourPane) (decide.Binding, htask.Task, bool) {
	row, err := l.read(ctx, p)
	if err != nil {
		var refusal *htask.Refusal
		if errors.As(err, &refusal) && refusal.Code == string(codes.NotFound) {
			l.logf("pane %s: the board has no task %s, so there is nothing for it to be doing; letting it go", p.pane, p.ref)
			return decide.Binding{}, htask.Task{}, false
		}
		// The board could not answer, which is not an answer that the task
		// moved on. A pane with a binding is held, exactly as a tick holds
		// it; a pane with none cannot be bound to a row nobody can read, and
		// is left alone rather than acted on.
		if p.binding == nil {
			l.logf("pane %s: task %s cannot be read, so it is left as it is: %v", p.pane, p.ref, err)
			return decide.Binding{}, htask.Task{}, false
		}
		l.logf("task %s: cannot be read, holding the binding a restart found: %v", p.ref, err)
		return *p.binding, htask.Task{}, true
	}

	switch {
	case decide.Terminal(row.Status):
		// A finished task takes its pane with it. Nothing else will ever
		// close a pane this daemon opened for work that is over.
		l.logf("task %s is %s, retiring the pane %s this daemon opened for it", row.ID, row.Status, p.pane)
		l.retirePane(ctx, p.pane)
		return decide.Binding{}, htask.Task{}, false
	default:
		if claimed := row.Pane(); claimed != "" && claimed != p.pane {
			// Whose worker the task is now is the board's answer, not a
			// restart's, and the pane is not this daemon's to close on the
			// strength of it.
			l.logf("task %s is held by %s, not by pane %s; letting the pane go unbound", row.ID, claimed, p.pane)
			return decide.Binding{}, htask.Task{}, false
		}
	}

	if p.binding != nil {
		b := *p.binding
		if b.Tab == "" {
			b.Tab = p.tab
		}
		return b, row, true
	}
	// A live pane working a live row that no binding names. The goal reached
	// it — the row it is on says so — so it counts as delivered once, from
	// now: the claim timeout has nothing earlier to measure from.
	l.logf("task %s: pane %s is working it and no binding named it; adopting the worker a restart lost", row.ID, p.pane)
	return decide.Binding{
		TaskID:     row.ID,
		Pane:       p.pane,
		Kind:       p.kind,
		Tab:        p.tab,
		PromptedAt: l.now(),
		Prompts:    1,
	}, row, true
}

// read asks the board about one pane's task, addressed the way that pane can
// name it: by id across every project when a binding carries one, and by the
// number the agent registered under, scoped to the project its checkout
// belongs to, when none does. Widening the by-number read instead would ask
// the board to accept a number across projects, which it refuses by design
// and a sibling plugin's contract rests on.
func (l *Loop) read(ctx context.Context, p ourPane) (htask.Task, error) {
	if p.project == "" {
		return l.Board.Get(ctx, p.ref)
	}
	return l.Board.GetIn(ctx, p.ref, p.project)
}

// project is the repository a pane's checkout belongs to.
func (l *Loop) project(ctx context.Context, cwd string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("herdr reports no directory for the pane")
	}
	if l.Worktrees == nil {
		return "", fmt.Errorf("this dispatcher hands out no checkouts, so nothing can say which repository %s is", cwd)
	}
	return l.Worktrees.Project(ctx, cwd)
}

// bindingKind is the lane a binding names. There is one, and a binding
// written before the kind existed reads as it.
func bindingKind(decide.Binding) string { return decide.KindWorker }

// lane reads one of this daemon's own agent names: which lane the pane was
// opened for and which task number it was opened on. A name this daemon does
// not write is not this daemon's pane.
func lane(name string) (string, int, bool) {
	rest, ok := strings.CutPrefix(name, "hdis-")
	if !ok {
		return "", 0, false
	}
	seq, err := strconv.Atoi(rest)
	if err != nil || seq <= 0 {
		return "", 0, false
	}
	return decide.KindWorker, seq, true
}

// retirePane closes a pane a dropped binding named, through the same pipeline
// a live daemon retires through, so the settings file the spawn wrote goes
// with it. It is only ever reached with a pane this daemon opened and whose
// binding still names it at the moment of the drop — the same standing a
// live retire has. The task's lease is not touched: that is the board's.
func (l *Loop) retirePane(ctx context.Context, pane string) {
	if l.Spawn == nil {
		return
	}
	if err := l.Spawn.Retire(ctx, pane); err != nil {
		l.logf("pane %s could not be retired: %v", pane, err)
	}
}

// principal is the board principal this daemon writes with, which carries
// its own pane.
func (l *Loop) principal() string {
	if l.Board != nil && l.Board.Principal != "" {
		return l.Board.Principal
	}
	return htask.PrincipalFor(l.BasePane)
}

// release hands back every task the board says this dispatcher is holding
// that no adopted pane is working. It is the reservation window seen from
// the board: a daemon that went down between reserving a task and bringing a
// pane up leaves a hold nothing alive owns.
//
// It runs after the panes are reconciled, and that order is the whole of its
// safety: a task a live pane is working is already bound by then, so the
// only holds left are ones with no worker behind them. `--mine` is scoped to
// the principal, and the principal carries this daemon's pane, so a peer
// daemon's hold is never in the answer to begin with.
func (l *Loop) release(ctx context.Context) {
	if l.Board == nil {
		return
	}
	held, err := l.Board.Held(ctx)
	if err != nil {
		l.logf("the board cannot say what this dispatcher is holding, so nothing is handed back: %v", err)
		return
	}

	bound := make(map[string]bool)
	l.mu.Lock()
	for _, b := range l.bindings {
		bound[b.TaskID] = true
	}
	l.mu.Unlock()

	for _, row := range held {
		if bound[row.ID] || row.ClaimedBy != l.principal() {
			continue
		}
		l.logf("task %s: the board still holds it for this daemon and no pane is working it; handing it back", row.ID)
		if err := l.Board.Release(ctx, row.ID, "the dispatcher that reserved this task went down before a worker came up"); err != nil {
			l.logf("task %s: the stale hold could not be handed back: %v", row.ID, err)
		}
	}
}

// reap removes the checkouts under this daemon's own worktree root that no
// binding names. A binding is the only record of where a checkout is, so a
// restart that loses one — a pane retired while the daemon was down, a store
// written before the create — leaves the directory with nothing left to
// remove it. Both lanes are covered: a worker's checkout is as reapable as a
// verifier's, because a worker's commits are on a branch and the branch
// outlives the directory.
//
// It is bounded twice over, and deliberately: only inside the root this
// daemon creates its own checkouts in, and only entries carrying the prefix
// this daemon names them with. A directory under that root that hdis did not
// create is not hdis's to remove.
func (l *Loop) reap(ctx context.Context) {
	if l.Worktrees == nil || l.Worktrees.RootDir() == "" {
		return
	}
	entries, err := os.ReadDir(l.Worktrees.RootDir())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			l.logf("the worktree root %s could not be read, so nothing is reaped: %v", l.Worktrees.RootDir(), err)
		}
		return
	}

	named := make(map[string]bool)
	l.mu.Lock()
	for _, b := range l.bindings {
		if b.Worktree != "" {
			named[b.Worktree] = true
		}
	}
	l.mu.Unlock()

	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), worktree.Prefix) {
			continue
		}
		dir := filepath.Join(l.Worktrees.RootDir(), e.Name())
		if named[dir] {
			continue
		}
		l.logf("worktree %s: no binding names it, so the agent it belonged to is gone; removing it", dir)
		if err := l.Worktrees.Remove(ctx, dir); err != nil {
			l.logf("worktree %s could not be removed: %v", dir, err)
		}
	}
}

// markUnadopted records that the start-up reconciliation did not happen, so
// no tick dispatches and no save lands until one does.
func (l *Loop) markUnadopted() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unadopted = true
}

// needsAdopt reports whether an Adopt has failed and none has succeeded since.
func (l *Loop) needsAdopt() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unadopted
}

// Readopted is how many persisted bindings the last Adopt kept.
func (l *Loop) Readopted() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.readopted
}

// BindingsPath is where the bindings are kept, for doctor to name.
func (l *Loop) BindingsPath() string {
	if l.Store == nil {
		return ""
	}
	return l.Store.Path
}

// saveLocked writes the bindings out. The caller holds mu, so the document
// on disk can never be a set no process ever held. A store that cannot be
// written is reported and the daemon carries on: the bindings in memory are
// still right, and refusing to dispatch because a file is unwritable helps
// nobody.
func (l *Loop) saveLocked() {
	if l.Store == nil {
		return
	}
	// A dispatcher that never reconciled holds an empty set it did not earn.
	// Writing it out would destroy the one record of the workers a previous
	// process left running.
	if l.unadopted {
		return
	}
	if err := l.Store.Save(store.State{Bindings: l.bindings, Reservations: l.pending}); err != nil {
		l.logf("the bindings could not be written: %v", err)
	}
}

// Bindings reports what the dispatcher currently believes it is driving.
func (l *Loop) Bindings() []decide.Binding {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]decide.Binding(nil), l.bindings...)
}

func (l *Loop) snapshot(ctx context.Context) (decide.Snapshot, error) {
	l.mu.Lock()
	bindings := append([]decide.Binding(nil), l.bindings...)
	pending := append([]store.Reservation(nil), l.pending...)
	l.mu.Unlock()

	snap := decide.Snapshot{
		Tasks: make(map[string]decide.Task),
		Now:   l.now(),
	}
	rows := make(map[string]htask.Task)

	bound := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		bound[b.TaskID] = true
		row, err := l.Board.Get(ctx, b.TaskID)
		if err != nil {
			// The core holds a binding it knows nothing about, which is the
			// safe reading of a row that cannot be read.
			l.logf("task %s: cannot be read, holding its binding: %v", b.TaskID, err)
			continue
		}
		rows[row.ID] = row
		snap.Tasks[row.ID] = readyTask(row)
	}

	ready, err := l.Board.Ready(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	offered := make(map[string]htask.Task, len(ready))
	for _, row := range ready {
		offered[row.ID] = row
	}

	// Reservations go first: an on-demand dispatch is a caller asking for
	// this task now, ahead of whatever order the board lists.
	reserved := make(map[string]bool, len(pending))
	for _, held := range pending {
		id := held.TaskID
		row, ok := offered[id]
		if bound[id] || !ok {
			// A worker is already on it, or the board has taken it back.
			// Either way the reservation has nothing left to buy.
			if !bound[id] {
				l.logf("task %s: was reserved for dispatch, but the board no longer offers it; dropping the reservation", id)
			}
			l.unreserve(id)
			continue
		}
		reserved[row.ID] = true
		rows[row.ID] = row
		snap.Tasks[row.ID] = readyTask(row)
		snap.Ready = append(snap.Ready, row.ID)
	}

	for _, row := range ready {
		// A task stays ready until its worker claims, so a task already
		// bound is a task already dispatched, not a task to dispatch again.
		if bound[row.ID] || reserved[row.ID] {
			continue
		}
		rows[row.ID] = row
		snap.Tasks[row.ID] = readyTask(row)
		snap.Ready = append(snap.Ready, row.ID)
	}

	panes, err := l.Herdr.Panes(ctx)
	if err != nil {
		return decide.Snapshot{}, err
	}
	snap.Agents = panes
	snap.Bindings = bindings

	l.mu.Lock()
	l.rows = rows
	l.mu.Unlock()
	return snap, nil
}

// readyTask is a row the board is offering, as the core reads it. The project
// is what the core shares the ready list out by, so one board offering more
// work than there are slots cannot take them all.
func readyTask(row htask.Task) decide.Task {
	return decide.Task{ID: row.ID, Status: row.Status, ClaimedBy: row.Pane(), Feedback: row.Feedback, Project: row.Project}
}

func (l *Loop) apply(ctx context.Context, actions []decide.Action) {
	for _, a := range actions {
		var err error
		switch a.Kind {
		case decide.Spawn:
			err = l.spawn(ctx, a)
		case decide.Rearm:
			// The submission the announcement and the verification belonged
			// to has left review. If it comes back, both are due again.
			l.rearm(a.TaskID)
		case decide.Prompt:
			err = l.prompt(ctx, a)
		case decide.Notify:
			err = l.notify(ctx, a)
		case decide.Retire:
			err = l.retire(ctx, a)
		case decide.Unbind:
			// The pane is gone. Dropping the binding and the settings file
			// its spawn wrote is all there is to do: releasing the lease is
			// the board's own sweep.
			l.logf("task %s: pane %s is gone, dropping its binding", a.TaskID, a.Pane)
			l.Spawn.Discard(a.Pane)
			l.drop(ctx, a.Pane)
		case decide.GiveUp:
			l.logf("task %s: %s after %d prompts, retiring pane %s",
				a.TaskID, a.Reason, l.promptsFor(a.Pane), a.Pane)
			err = l.retire(ctx, a)
		default:
			err = fmt.Errorf("unknown action %q", a.Kind)
		}
		if err != nil {
			l.logf("%s task %s: %v", a.Kind, a.TaskID, err)
		}
	}
}

func (l *Loop) spawn(ctx context.Context, a decide.Action) error {
	row, ok := l.row(a.TaskID)
	if !ok {
		return fmt.Errorf("no board row to spawn from")
	}
	profile, err := l.Config.ProfileFor(row.Project)
	if err != nil {
		return err
	}
	// The checkout comes first, and no checkout means no worker. A worker
	// edits, stages and commits, so the project directory — the one the
	// operator sits in and every other worker would otherwise hold — is the
	// one place it must never run: two workers in that tree is how one
	// task's commit swept up another task's uncommitted work. Not
	// dispatching is the better failure, and the next tick may try again.
	if l.Worktrees == nil {
		return fmt.Errorf("no worktree manager, so nothing works on task %s in a tree of its own", row.Project)
	}
	tree, branch, err := l.Worktrees.Worker(ctx, row.Project, row.Seq)
	if err != nil {
		return err
	}
	// The condition is composed here, not rendered from the board: it travels
	// on a line herdr TYPES into the pane, and the board's own goal document
	// is far too long to type intact. The worker reads the criteria itself.
	pane, err := l.Spawn.Run(ctx, spawn.Request{
		Name:       workerName(row.Seq),
		Label:      spawn.TabLabel(row.Seq),
		BasePane:   l.BasePane,
		OriginPane: row.PaneID,
		Project:    row.Project,
		Cwd:        tree,
		Profile:    profile,
		Goal:       spawn.PointerGoal(row.Seq),
	})
	if pane == "" {
		// Nothing came up, so the task has not had its worker and the next
		// tick may try again. The checkout it would have worked in goes with
		// it; the branch stays, because a branch costs nothing and a second
		// attempt continues the one it already has.
		if rmErr := l.Worktrees.Remove(ctx, tree); rmErr != nil {
			l.logf("task %s: %v", row.ID, rmErr)
		}
		return err
	}
	// A pane came back, so a worker is alive in it — including when the
	// spawn failed loud because it could not be read. The binding is the
	// only record of which pane was prompted for which task until the worker
	// claims; drop it and review is never announced for a task that may
	// already be under way. The error still reaches the operator's log.
	l.mu.Lock()
	l.bindings = append(l.bindings, decide.Binding{
		TaskID:     row.ID,
		Pane:       pane,
		Kind:       decide.KindWorker,
		Worktree:   tree,
		Tab:        l.Spawn.TabOf(pane),
		Branch:     branch,
		PromptedAt: l.now(),
		Prompts:    1,
	})
	l.saveLocked()
	l.mu.Unlock()
	// The reservation is spent the moment the binding exists.
	l.unreserve(row.ID)
	return err
}

// rearm forgets the announcement and the verification on a task's binding,
// because the submission they belonged to is no longer in review.
func (l *Loop) rearm(taskID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == taskID {
			l.bindings[i].Notified = false
			l.bindings[i].Verified = false
		}
	}
	l.saveLocked()
}

// prompt is the backstop. Most of what it carries is a nudge — one
// instruction for one turn, against a goal already armed in the worker.
//
// The self-review shot is the exception, and it is armed as a /goal of its
// own. A plain prompt fires exactly once, so a shallow pass at the mutations
// ends the check: nothing asks again. A /goal is evaluated after every turn,
// so the worker's own loop is what refuses to stop on a half-done pass.
//
// This once said a slash command that long could not arrive through a prompt
// anyway. Nothing had measured that: TypedLineBudget bounds the SPAWN line,
// and spawn.go says so explicitly for this very condition. The measured
// ceiling for a prompted /goal is the operator's, 1024 with 1023 safe, and it
// lives beside its measurement in spawn.PromptedGoalBudget.
func (l *Loop) prompt(ctx context.Context, a decide.Action) error {
	if err := l.Herdr.AgentPrompt(ctx, a.Pane, l.nudge(a)); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Prompts++
			l.bindings[i].PromptedAt = l.now()
			// The binding is where "a shot has been SENT for this
			// submission" is remembered. It is not a receipt — §11.4 forbids
			// reading this call's success as delivery — so decide.selfReviewDue
			// asks again while herdr still calls the worker idle.
			if a.Reason == decide.ReasonSelfReview {
				l.bindings[i].Verified = true
			}
		}
	}
	l.saveLocked()
	return nil
}

func (l *Loop) nudge(a decide.Action) string {
	name := l.taskName(a.TaskID)
	if a.Reason == decide.ReasonSelfReview {
		// Not a nudge at all: the worker submitted and is owed a second
		// condition, composed beside the one it booted with — and armed the
		// same way, so its evaluator loops instead of firing once.
		return spawn.GoalPrefix + spawn.SelfReviewCondition(l.seqFor(a.TaskID))
	}
	if a.Reason == decide.ReasonRejected {
		// Naming the rejection is the whole point: a worker told only to
		// carry on carries on with work it believes finished, instead of
		// reading why the review gate sent it back.
		return fmt.Sprintf("%s came back from review: it was rejected, and the board's row carries the feedback. "+
			"Read it with `htask task get %s`, address it, then submit again.", name, l.taskNumber(a.TaskID))
	}
	if a.Reason == decide.ReasonStalled {
		// Two facts, and no more than the two: the dispatcher cannot see
		// whether this worker stopped, so it says what it saw.
		return fmt.Sprintf("The board still has %s in doing and your pane is idle, with nothing back from review. "+
			"Carry on, or release it with a note saying what is left.", name)
	}
	return fmt.Sprintf("Your goal is still unclaimed on the board. Run `htask task claim %s` and start on it.", l.taskNumber(a.TaskID))
}

// notify hands the task to the operator and stops there: verification and
// approval belong to the board's review gate, not to this binary.
func (l *Loop) notify(ctx context.Context, a decide.Action) error {
	name := l.taskName(a.TaskID)
	err := l.Herdr.Notify(ctx,
		fmt.Sprintf("%s is in review", name),
		fmt.Sprintf("Worker %s submitted it. Review it on the board.", workerName(l.seqFor(a.TaskID))))
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.bindings {
		if l.bindings[i].TaskID == a.TaskID {
			l.bindings[i].Notified = true
		}
	}
	l.saveLocked()
	return nil
}

// retire closes the worker's pane through the spawn pipeline, which takes the
// settings file the spawn wrote with it, and drops the binding. It never
// releases the task's lease: pane-gone sweeps and the lease timer are the
// board's own, and a second writer racing them is the bug, not a safety net.
func (l *Loop) retire(ctx context.Context, a decide.Action) error {
	err := l.Spawn.Retire(ctx, a.Pane)
	l.drop(ctx, a.Pane)
	return err
}

// drop forgets one binding, by the pane it names, and takes the worktree the
// binding owned with it. The pane is what a binding is unique by.
//
// The binding is the only record of where a checkout is, so removing it here
// is the one place that record is spent. It is written to
// the store, so a restart inherits the removal rather than leaking it.
func (l *Loop) drop(ctx context.Context, pane string) {
	l.mu.Lock()
	var tree string
	kept := l.bindings[:0]
	for _, b := range l.bindings {
		if b.Pane != pane {
			kept = append(kept, b)
			continue
		}
		tree = b.Worktree
	}
	l.bindings = kept
	l.saveLocked()
	l.mu.Unlock()

	if tree != "" && l.Worktrees != nil {
		if err := l.Worktrees.Remove(ctx, tree); err != nil {
			l.logf("pane %s: %v", pane, err)
		}
	}
}

func (l *Loop) promptsFor(pane string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.bindings {
		if b.Pane == pane {
			return b.Prompts
		}
	}
	return 0
}

// row reads the last tick's board row for a task.
func (l *Loop) row(taskID string) (htask.Task, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok := l.rows[taskID]
	return row, ok
}

func (l *Loop) seqFor(taskID string) int {
	row, _ := l.row(taskID)
	return row.Seq
}

// taskName is what an operator would call the task: its number and title
// when the row is at hand, its id when it is not.
func (l *Loop) taskName(taskID string) string {
	row, ok := l.row(taskID)
	if !ok {
		return "task " + taskID
	}
	return fmt.Sprintf("task #%d %q", row.Seq, row.Title)
}

func (l *Loop) taskNumber(taskID string) string {
	if row, ok := l.row(taskID); ok {
		return fmt.Sprintf("%d", row.Seq)
	}
	return taskID
}

// workerName is the agent name herdr registers. Herdr requires it to match
// [a-z][a-z0-9_-]{0,31} and to be unique among live agents; one worker per
// task number is both.
func workerName(seq int) string { return fmt.Sprintf("hdis-%d", seq) }

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) logf(format string, args ...any) {
	if l.Log != nil {
		l.Log.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
