// Package config is the dispatcher's own execution policy: the worker
// profiles a Spawn action is assembled from, and which profile a project
// gets. None of it belongs on the board — the ledger carries no profile
// field, deliberately, because which agent kind and model a worker gets is
// this binary's business and not a board fact.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Provider names how a worker is launched.
type Provider string

const (
	// ProviderClaude launches the plain binary.
	ProviderClaude Provider = "claude"
	// ProviderCodex launches through the proxy launcher named by Config's
	// Proxy field, which injects the routing environment and the settings
	// half. hdis carries no proxy logic of its own; it only runs the
	// launcher's two documented steps.
	ProviderCodex Provider = "codex"
)

// The defaults a document may leave out.
const (
	DefaultAgent  = "claude"
	DefaultEffort = "low"
	// DefaultProxy is the codex provider's launcher binary. It is a name the
	// config carries rather than a word compiled in here because that binary
	// has been renamed once already.
	DefaultProxy = "proxenos"
)

// Profile is the launch preset for one worker. Model is a tier alias, and an
// empty one means the CLI's own default rather than no model.
type Profile struct {
	Provider Provider `json:"provider"`
	Agent    string   `json:"agent"`
	Model    string   `json:"model"`
	Effort   string   `json:"effort"`
	Args     []string `json:"args"`
}

// Verify is the verification lane's own policy: whether a task that reaches
// review earns a self-review shot in the pane that produced it. The lane is
// execution policy like every other choice here, and the board carries no
// trace of it.
//
// It names no profile. The shot lands in the worker's own pane, which was
// launched from the worker's own profile, so there is no second launch to
// configure.
type Verify struct {
	Enabled bool `json:"enabled"`
}

// MeasuredReadableColumns is the narrowest pane whose detection text this
// repo's own matches still read correctly, measured rather than guessed.
//
// It matters because reading a worker is how the dispatcher knows anything
// about it. Every judgement in the spawn pipeline goes through `herdr pane
// read --source detection` and a substring match — the startup dialog in
// TrustDialogMarkers, the registered goal in GoalMarkers — and a pane narrow
// enough word-wraps the phrase those matches look for, so the match fails
// while the worker is perfectly fine. That is a daemon believing the wrong
// thing about a worker it is driving, which is why this is a correctness
// number and not a taste one.
//
// Measured on 2026-08-23 against herdr 0.8.2 and claude 2.1.239, in a
// throwaway workspace: nine panes of measured width (21, 23, 25, 27, 29, 31,
// 38, 43 and 52 columns, each read back with `stty size`), a real Claude
// worker brought up in every one, and each given the real PointerGoal
// condition. What it showed:
//
//   - At 21 columns only "goal set:" survived; "/goal active" was truncated
//     by the status line, so one of the two markers was already unreadable.
//   - At 23 columns and above both GoalMarkers read whole, every time.
//   - Claude renders its dialog and status body at the pane's columns minus
//     three, word-wrapped: in the 52-column pane the longest body line was
//     49 characters.
//
// The cap follows from that last rule and the LONGEST phrase the detector
// matches, which is the 37-character trust-dialog marker: it needs 37 + 3 =
// 40 columns to land on one line, and one that wraps is one that never
// matches. So 40, the widest requirement of any marker in use, and not the
// 23 the goal markers alone would have allowed.
//
// This number is derived, not restated: spawn's
// TestTheReadableColumnFloorIsDerivedFromTheLongestMarker recomputes it from
// spawn.TrustDialogMarkers and spawn.GoalMarkers and fails if the two drift
// apart. Re-measured on 2026-08-23 when claude 2.1.239's reworded dialog was
// added to the marker set: the new phrase, "yes, i trust this folder", is 24
// characters, so the 37-character older phrase is still the longest one in
// use and the floor did not move.
//
// One thing this measurement is NOT: it is not the reason a narrow pane
// loses a typed line. At 25, 27 and 29 columns the condition arrived whole
// and only the Enter was lost, which an explicit send-keys then delivered.
const MeasuredReadableColumns = 40

// MeasuredWindowColumns is how wide one whole tab was in the same session:
// the root pane of a fresh tab reported 226 columns to `stty size`, and the
// six panes split across it summed to the same.
const MeasuredWindowColumns = 226

// GridSplit is the placement rule inside one tab: given how many panes it
// already holds, which pane the next one is split off and which way.
//
// A tab fills as a GRID, and a grid needs the split TARGET to move rather
// than only the direction. Panes are added in generations, each generation
// twice the size of the one before, and every pane in a generation splits the
// pane one generation back:
//
//	held  target  direction     shape
//	1     p1      right         p1 | p2
//	2     p1      down          (p1 over p3) | p2
//	3     p2      down          (p1 over p3) | (p2 over p4)
//	4..7  p1..p4  right         each of the four halved sideways
//	8..15 p1..p8  down          each of the eight halved downwards
//
// Four panes are then four equal rectangles, each half the window wide, which
// is what an operator watching two tasks at once needs to read. Splitting off
// the LAST pane instead gives a column beside a stack and a fourth pane a
// quarter of the width.
//
// The returned target is 1-based, in the order the tab lists its panes.
func GridSplit(held int) (target int, direction string) {
	if held < 1 {
		return 0, ""
	}
	// generation is how many doublings deep the incoming pane is: the
	// largest g with 2^g <= held.
	generation, size := 0, 1
	for size*2 <= held {
		generation++
		size *= 2
	}
	direction = "down"
	if generation%2 == 0 {
		direction = "right"
	}
	return held + 1 - size, direction
}

// NarrowestColumns is how wide the narrowest pane in a tab of the given size
// is, under GridSplit and starting from MeasuredWindowColumns.
//
// Only the even generations split sideways, so a pane's width halves once per
// two generations and the odd ones spend themselves on height instead.
func NarrowestColumns(panes int) int {
	width := MeasuredWindowColumns
	for n := 1; n < panes; n++ {
		if _, direction := GridSplit(n); direction == "right" {
			// The pane being halved is the widest one left, so the
			// narrowest width only moves on the first split of a
			// generation.
			if n&(n-1) == 0 {
				width /= 2
			}
		}
	}
	return width
}

// RowsNotDependable is the row cost of a marker that cannot be what keeps a
// read working, whatever the pane's height.
//
// Two kinds of marker earn it, and both are real entries in MarkerRows. One
// is a phrase the claude build in front of us never renders, so no height
// makes it appear. The other is a phrase that DOES render but scrolls with
// the transcript rather than sitting at a fixed place on screen, so what it
// costs in rows is a function of how much the worker has printed since, not
// of how tall the pane is. Neither can lower a floor, and the derivation
// treats both the same way: they are carried so that every marker in use has
// an entry, and they are never the cheapest of a read.
const RowsNotDependable = 1 << 30

// MarkerRows is how short a pane may be while each marker the dispatcher
// matches on still reads back whole through `herdr pane read --source
// detection`, measured at MeasuredReadableColumns.
//
// It is keyed by the marker phrase itself so that the marker sets in
// internal/spawn and this table are checked against each other rather than
// kept in step by hand:
// TestTheReadableRowFloorIsDerivedFromTheMarkerSets fails when a marker is
// added, removed or reworded without its row cost being measured, and fails
// when an entry here names a phrase nothing matches on any more. That test is
// the whole point of this table. The row floor cannot be derived from a
// phrase's LENGTH the way MeasuredReadableColumns is, because what a marker
// costs in rows is where it sits in the block the dialog renders, not how
// wide it is — so the coupling is pinned instead of computed.
//
// Measured on 2026-08-23 against herdr 0.8.2 and claude 2.1.239, in a
// throwaway workspace: one tab per height, a git repo of its own per tab so
// the trust dialog is raised rather than remembered, every pane pinned to
// exactly 40 columns (MeasuredReadableColumns, where the dialog wraps hardest
// of any width a worker is allowed) and to a measured height, a real Claude
// brought up in each with the real PointerGoal condition, the pane read back
// through `herdr pane read --source detection`, then answered with an Enter
// and read again. Heights probed at 40 columns: 67, 24, 22, 20, 18, 17, 16,
// 14, 12, 10, 8, 7, 6, 5, 4, 3 and 2 rows.
var MarkerRows = map[string]int{
	// The new dialog's selectable option, "❯ 1. Yes, I trust this folder",
	// sits two lines above "Enter to confirm · Esc to cancel" and is the
	// last thing in the block to go. It read whole at 4 rows; at 3 the
	// snapshot held only "Enter to confirm · Esc to cancel". The Enter
	// that answers the dialog landed at 4 rows too — the same pane came
	// back with the goal registered — so this is the height at which the
	// whole answer-the-dialog step still works, not only the match.
	"yes, i trust this folder": 4,
	// The older build's phrase. claude 2.1.239 does not render it at any
	// height: it was absent from every one of the eighteen snapshots, and
	// the dialog it was read from is gone. It costs nothing to keep for an
	// operator on that older build, and it can never be the marker that
	// makes a read work here.
	"do you trust the files in this folder": RowsNotDependable,
	// The echo of the condition, which scrolls up as the worker starts
	// working. It survived at 30 and 67 rows and was already gone at 24,
	// but that boundary moves with how much the worker printed rather than
	// with the pane, so it is not a height this repo may lean on.
	"goal set:": RowsNotDependable,
	// The status line, pinned to the bottom row of the pane. It read at
	// every height probed, down to the 2 rows that were the shortest pane
	// herdr would give.
	"/goal active": 2,
}

// ReadableRowsFor is what ONE read needs: the cheapest of the markers that
// satisfy it.
//
// A read matches an OR over its set, so the shortest pane it still works in
// is decided by the marker that survives lowest, not by the whole set. It
// returns RowsNotDependable when no marker in the set has a dependable
// measurement, which is a set no height can be trusted to satisfy.
func ReadableRowsFor(markers []string) int {
	cheapest := RowsNotDependable
	for _, m := range markers {
		rows, ok := MarkerRows[m]
		if !ok {
			// An unmeasured marker cannot be counted on either. The
			// spawn-side test is what turns this into a failure
			// rather than a silent one.
			continue
		}
		if rows < cheapest {
			cheapest = rows
		}
	}
	return cheapest
}

// MeasuredReadableRows is the shortest pane every read the dispatcher makes
// still works in: the TALLEST requirement across the reads, each of which
// costs only its own cheapest marker.
//
// It matters for the same reason MeasuredReadableColumns does, one axis over.
// `herdr pane read --source detection` returns the BOTTOM of the pane's
// buffer, so a pane too short does not wrap a marker, it scrolls the marker
// off the top and hands back a snapshot that no longer holds it. The
// dispatcher then reads a worker sitting on a dialog and sees no dialog.
//
// Four, and both halves of that are in MarkerRows: the trust read costs 4
// rows because its cheapest dependable marker is the dialog's option line,
// and the goal read costs 2 because the status line never leaves the bottom.
// The number is a constant rather than a call because it bounds a default
// below, and TestTheReadableRowFloorIsDerivedFromTheMarkerSets is what keeps
// it equal to what the shipped marker sets work out to.
//
// It was 17 while TrustDialogMarkers held only the older build's phrase and
// the floor had to be measured against the dialog's own top sentence, which
// is the earliest thing in the block and the most expensive. Matching the
// option line instead moved the read to the bottom of the block, and the
// floor with it.
const MeasuredReadableRows = 4

// MeasuredWindowRows is how tall one whole tab was in the same session: the
// root pane of a fresh tab reported 69 rows.
//
// It is NOT the same window MeasuredWindowColumns came from. That 226 was
// measured in another session's window; this one is 50 columns wide and 69
// rows tall, and the two constants are each honest about the window they were
// taken in. The cap below is unchanged by the difference: the 62-row window
// the finding describes gives 29 rows at eight panes and 12 at sixteen, which
// lands on the same answer as 69 does.
const MeasuredWindowRows = 69

// SplitRowCost is how many rows a down split spends on chrome before it
// halves what is left, measured rather than assumed.
//
// Columns lose nothing to a split — the panes across a tab sum to the whole
// window — and NarrowestColumns is right to halve cleanly. Rows do not. A
// fresh 69-row pane split downwards was measured at 33 and 32, and splitting
// the 33 again gave 16 and 15, so a split costs between 2 and 5 rows. The
// larger of the two is taken because this number guards a floor: it makes the
// derived pane count the smaller, never the larger, and at four panes deep it
// is one row more pessimistic than what was measured.
const SplitRowCost = 4

// ShortestRows is how tall the shortest pane in a tab of the given size is,
// under GridSplit and starting from MeasuredWindowRows.
//
// It is the mirror of NarrowestColumns: only the ODD generations split down,
// so a pane's height halves once per two generations and the even ones spend
// themselves on width instead.
func ShortestRows(panes int) int {
	rows := MeasuredWindowRows
	for n := 1; n < panes; n++ {
		if _, direction := GridSplit(n); direction == "down" {
			// The pane being halved is the tallest one left, so the
			// shortest height only moves on the first split of a
			// generation.
			if n&(n-1) == 0 {
				rows = (rows - SplitRowCost) / 2
			}
		}
	}
	return rows
}

// MaxPanesClearing is the largest pane count whose smallest pane still clears
// both floors under GridSplit: at least minColumns wide and minRows tall.
//
// Both floors are arguments rather than constants read from the package so
// that either one can be neutralised in a test and the answer the other one
// alone gives can be pinned. A derivation that consults only one of them
// stops matching those pins.
func MaxPanesClearing(minColumns, minRows int) int {
	// A runaway guard and nothing more. Both ladders fall monotonically,
	// so the answer is found long before this; it is deliberately far
	// above where either floor gives out — the row floor alone reaches 128
	// panes — so that the search never returns the bound itself and calls
	// it an answer. MaxPanesPerTabIsBelowTheSearchCeiling is what says so
	// if a future floor ever pushes past it.
	const ceiling = SearchCeiling
	best := 1
	for n := 1; n <= ceiling; n++ {
		if NarrowestColumns(n) >= minColumns && ShortestRows(n) >= minRows {
			best = n
		}
	}
	return best
}

// SearchCeiling bounds MaxPanesClearing's walk. It is exported so a test can
// tell a real answer from the walk running out of room.
const SearchCeiling = 4096

// DefaultMaxPanesPerTab is how many panes may share one of this dispatcher's
// tabs, and it follows from GridSplit and BOTH measured floors rather than
// from taste.
//
// Under the grid rule the widths run 226 for one pane, 113 for two through
// four, and 56 for five through sixteen; the seventeenth starts the
// generation that halves 56 to 28, under the 40-column floor. So sixteen on
// that axis.
//
// The heights were the axis nothing had measured, because only the ODD
// generations split down and the derivation had never looked at them. They
// cost chrome as well as halving: 69 rows for one and two panes, 32 for three
// through eight, 14 for nine through sixteen, and 5 from thirty-three. Against
// the 4-row floor the shipped marker set works out to, rows do not give out
// until 128 panes — four whole generations past where the width does.
//
// So sixteen, the tighter of the two, and the same number the column axis
// alone gave. What changed is that it is now the ANSWER to both floors rather
// than to one of them with the other unexamined, and the derivation says
// which floor decided it. It reached 8 for a day, against a marker set that
// had to match the dialog's top sentence; matching its option line instead
// moved the trust read to the bottom of the block and the floor from 17 rows
// to 4.
//
// The cap is a FLOOR GUARD and nothing more. Grouping is by TASK: a tab holds
// one task, so this bounds the panes ONE task may have — one today, since a
// task gets a worker and nothing else — and it is not what keeps two tasks
// apart. That is the tab label, compared in the spawn pipeline.
var DefaultMaxPanesPerTab = MaxPanesClearing(MeasuredReadableColumns, MeasuredReadableRows)

// DefaultMaxWorkers is how many workers may be live at once when neither the
// config nor the daemon flag names a number.
const DefaultMaxWorkers = 2

// Layout is where a worker is placed. There is one placement — a tab of its
// own — and this is the number that says why.
type Layout struct {
	// MinPaneColumns is the width a worker's pane must be readable at.
	// Zero means MeasuredReadableColumns; it may be raised and never
	// lowered past what was measured, because below it the dispatcher
	// cannot trust its own reading of the pane.
	//
	// A worker is only ever split into a tab THIS dispatcher opened for
	// THAT task, and never into anyone else's, and this is the whole
	// reason: Herdr reports no column count for a pane, so the width a
	// worker will be read at cannot be checked after the fact. It can only
	// be GUARANTEED beforehand, by bounding how many panes a tab of this
	// dispatcher's own may hold — which is what max_panes_per_tab is.
	MinPaneColumns int `json:"min_pane_columns"`
	// MaxPanesPerTab is how many panes may share one of this dispatcher's
	// tabs before the next opens a tab of its own. Zero means
	// DefaultMaxPanesPerTab, which is what MinPaneColumns,
	// MeasuredReadableRows and the measured window work out to under
	// config.GridSplit. Raising it past that default puts panes below a
	// width the dispatcher was measured to still read them at, and far
	// enough past it below a readable height too.
	//
	// It bounds panes per TASK, because a tab holds one task: nothing here
	// keeps two tasks apart, and raising it never puts a second task in a
	// tab. It is the readability floor guard and only that.
	MaxPanesPerTab int `json:"max_panes_per_tab"`
}

// Config is the whole document: named profiles, the one every project gets
// unless it says otherwise, and the projects that say otherwise.
type Config struct {
	Default  string             `json:"default"`
	Profiles map[string]Profile `json:"profiles"`
	Projects map[string]string  `json:"projects"`
	// Proxy is the codex provider's launcher binary, resolved off PATH
	// unless it is a path. Empty means DefaultProxy.
	Proxy string `json:"proxy"`
	// Verify is the verification lane: off unless the document turns it on.
	Verify Verify `json:"verify"`
	// Layout is where a worker is placed and the width it must be readable
	// at.
	Layout Layout `json:"layout"`
	// MaxWorkers is how many workers may be live at once. Zero means
	// DefaultMaxWorkers. It lives here rather than only on the daemon's
	// flag because a number that exists only in the shell line that
	// started the daemon is a number a restart drops without saying so.
	//
	// The number reads two ways and this is the one the code implements:
	// max_workers bounds how many worker panes may exist at once, and not
	// how many agents may be spending tokens at once. A pane that has
	// submitted and is awaiting review spends nothing and still holds its
	// slot, on purpose, because a rejection carries on in that same pane.
	// So it is a screen and memory bound, and raising it buys panes rather
	// than buying throughput on a board whose slots are held by panes
	// waiting for a human. `hdis status` and `hdis doctor` say how many
	// slots are held that way.
	MaxWorkers int `json:"max_workers"`
	// Pane is the pane worker panes are split off, for a daemon that was
	// not started inside one and was given no -pane. Without it, and
	// without either of those, nothing can be spawned at all.
	Pane string `json:"pane"`
}

// Parse reads a config document and refuses one it could not resolve later.
func Parse(b []byte) (Config, error) {
	// A document written for the verifier pane still names the profile that
	// pane launched from. Nothing launches separately now, so the field has
	// nothing left to name, and an operator who set it believes a verifier
	// is running. DisallowUnknownFields would refuse it as a bare "profile",
	// which does not say which one, so it is named here before the decode.
	var probe struct {
		Verify struct {
			Profile *string `json:"profile"`
		} `json:"verify"`
	}
	if err := json.Unmarshal(b, &probe); err == nil && probe.Verify.Profile != nil {
		return Config{}, fmt.Errorf("hdis config: verify.profile names a verifier pane that no longer launches; the verification lane is a self-review shot in the worker's own pane, so remove the field")
	}

	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("hdis config: %w", err)
	}

	if c.Proxy == "" {
		c.Proxy = DefaultProxy
	}
	if len(c.Profiles) == 0 {
		return Config{}, fmt.Errorf("hdis config: no profiles")
	}
	for name, p := range c.Profiles {
		switch p.Provider {
		case ProviderClaude, ProviderCodex:
		case "":
			return Config{}, fmt.Errorf("hdis config: profile %q names no provider", name)
		default:
			return Config{}, fmt.Errorf("hdis config: profile %q has provider %q, want %q or %q",
				name, p.Provider, ProviderClaude, ProviderCodex)
		}
		if p.Agent == "" {
			p.Agent = DefaultAgent
		}
		if p.Effort == "" {
			p.Effort = DefaultEffort
		}
		c.Profiles[name] = p
	}

	if c.Default == "" {
		return Config{}, fmt.Errorf("hdis config: no default profile")
	}
	if _, ok := c.Profiles[c.Default]; !ok {
		return Config{}, fmt.Errorf("hdis config: default profile %q is not defined", c.Default)
	}
	if c.Layout.MinPaneColumns == 0 {
		c.Layout.MinPaneColumns = MeasuredReadableColumns
	}
	if c.Layout.MinPaneColumns < MeasuredReadableColumns {
		return Config{}, fmt.Errorf("hdis config: layout.min_pane_columns is %d, and %d is the narrowest pane the detection text was measured to read correctly at; below it the dispatcher cannot trust what it reads off a worker",
			c.Layout.MinPaneColumns, MeasuredReadableColumns)
	}
	if c.MaxWorkers == 0 {
		c.MaxWorkers = DefaultMaxWorkers
	}
	if c.MaxWorkers < 1 {
		return Config{}, fmt.Errorf("hdis config: max_workers is %d, and a dispatcher that may run no worker at all can never dispatch", c.MaxWorkers)
	}
	if c.Layout.MaxPanesPerTab == 0 {
		// Derived from the width THIS document asks for, not from the
		// measured constant. An operator who raises min_pane_columns is
		// asking for wider panes, and a count still computed from the
		// measured 40 would have kept placing panes at the old width — the
		// key was validated, shown in doctor, and consulted by nothing.
		c.Layout.MaxPanesPerTab = MaxPanesClearing(c.Layout.MinPaneColumns, MeasuredReadableRows)
	}
	if c.Layout.MaxPanesPerTab < 1 {
		return Config{}, fmt.Errorf("hdis config: layout.max_panes_per_tab is %d, and a tab that may hold no worker at all can never be spawned into", c.Layout.MaxPanesPerTab)
	}
	for project, name := range c.Projects {
		if _, ok := c.Profiles[name]; !ok {
			return Config{}, fmt.Errorf("hdis config: project %q names profile %q, which is not defined", project, name)
		}
	}
	return c, nil
}

// Load reads a config document from disk.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("hdis config %s: %w", path, err)
	}
	c, err := Parse(b)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ProfileFor resolves the profile a project's workers launch with: its own
// override if it has one, the global default otherwise.
func (c Config) ProfileFor(project string) (Profile, error) {
	name := c.Default
	if over, ok := c.Projects[project]; ok {
		name = over
	}
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("hdis config: profile %q is not defined", name)
	}
	return p, nil
}

// AgentArgs renders the profile as the argv herdr forwards to the worker
// after `--`. The goal is not in it: Claude Code takes its initial prompt
// positionally, so the caller appends it last.
func (p Profile) AgentArgs() []string {
	args := []string{"--agent", p.Agent}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	args = append(args, "--effort", p.Effort)
	return append(args, p.Args...)
}

// HasSettingsArg reports whether the profile already carries a --settings of
// its own. The codex provider splices one in, and the client keeps only the
// last of two and drops the first without saying so.
func (p Profile) HasSettingsArg() bool {
	for _, a := range p.Args {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}

// MaxWorkersOr resolves how many workers may be live at once: what the daemon
// flag was passed, else what the config names. The flag defaults to zero
// precisely so an unpassed flag cannot silently overwrite the config.
func (c Config) MaxWorkersOr(given int) int {
	if given > 0 {
		return given
	}
	return c.MaxWorkers
}
