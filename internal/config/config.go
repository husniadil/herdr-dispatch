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
// review earns a VERIFIER worker, and which profile that worker launches
// with. The lane is execution policy like every other profile choice here,
// and the board carries no trace of it.
type Verify struct {
	Enabled bool `json:"enabled"`
	// Profile is the name of one of Profiles. It is the verifier's agent
	// kind, model and effort, in the same shape a worker's is.
	Profile string `json:"profile"`
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
// Two things this measurement is NOT. It is not a claim that the trust
// marker currently matches anything — it does not, at any width, because
// claude 2.1.239 rewrote that dialog; that is a separate defect and this
// number is what the marker WOULD need once it is fixed. And it is not the
// reason a narrow pane loses a typed line: at 25, 27 and 29 columns the
// condition arrived whole and only the Enter was lost, which an explicit
// send-keys then delivered.
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

// DefaultMaxPanesPerTab is how many panes may share one of this dispatcher's
// tabs, and it follows from GridSplit and MeasuredReadableColumns rather than
// from taste.
//
// Under the grid rule the widths run 226 for one pane, 113 for two through
// four, and 113/2 = 56 for five through sixteen — the generation that fills
// 9..16 spends itself on height, so the width holds. The seventeenth starts
// the generation that halves 56 to 28, which is under the 40-column floor a
// worker's detection text still reads at. So sixteen.
//
// This is a larger number than the 5 that stood before, and it is larger
// because the rule changed: the old placement always split the LAST pane, so
// a pane narrowed every second split forever and 56 was reached at five panes
// and 28 at six. A real grid halves the width far more slowly.
//
// The cap is a FLOOR GUARD and nothing more. Grouping is by TASK: a tab holds
// one task, so this bounds the panes ONE task may have — a worker and its
// verifier, two today — and it is not what keeps two tasks apart. That is the
// tab label, compared in the spawn pipeline.
const DefaultMaxPanesPerTab = 16

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
	// Nothing splits a worker into an existing tab, and this is the whole
	// reason: Herdr reports no column count for a pane, so the width a
	// worker will be read at cannot be checked after the fact. It can only
	// be GUARANTEED beforehand, by giving the worker a tab nothing else is
	// in.
	MinPaneColumns int `json:"min_pane_columns"`
	// MaxPanesPerTab is how many panes may share one of this dispatcher's
	// tabs before the next opens a tab of its own. Zero means
	// DefaultMaxPanesPerTab, which is what MinPaneColumns and the measured
	// window width work out to under config.GridSplit.
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
	MaxWorkers int `json:"max_workers"`
	// Pane is the pane worker panes are split off, for a daemon that was
	// not started inside one and was given no -pane. Without it, and
	// without either of those, nothing can be spawned at all.
	Pane string `json:"pane"`
}

// Parse reads a config document and refuses one it could not resolve later.
func Parse(b []byte) (Config, error) {
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
	if c.Verify.Enabled {
		if c.Verify.Profile == "" {
			return Config{}, fmt.Errorf("hdis config: the verification lane is on and names no profile")
		}
		if _, ok := c.Profiles[c.Verify.Profile]; !ok {
			return Config{}, fmt.Errorf("hdis config: the verification lane names profile %q, which is not defined", c.Verify.Profile)
		}
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
		c.Layout.MaxPanesPerTab = DefaultMaxPanesPerTab
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

// VerifyProfile resolves the profile a verifier launches with. A lane that is
// off has none, and asking for one is the caller's mistake rather than a
// default to guess at.
func (c Config) VerifyProfile() (Profile, error) {
	if !c.Verify.Enabled {
		return Profile{}, fmt.Errorf("hdis config: the verification lane is off")
	}
	p, ok := c.Profiles[c.Verify.Profile]
	if !ok {
		return Profile{}, fmt.Errorf("hdis config: the verifier profile %q is not defined", c.Verify.Profile)
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
