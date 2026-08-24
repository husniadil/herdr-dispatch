package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A config carrying a global default and a per-project override resolves the
// right profile for each project, and fills in the two documented defaults.
func TestProfileResolvesGlobalDefaultAndProjectOverride(t *testing.T) {
	const doc = `
default = "worker"

[profiles.worker]
provider = "claude"

[profiles.routed]
provider = "codex"
agent = "implementer"
model = "sonnet"
effort = "high"
args = ["--verbose"]

[projects]
"/src/herdr-dispatch" = "routed"
`

	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	got, err := c.ProfileFor("/src/somewhere-else")
	if err != nil {
		t.Fatalf("default profile: %v", err)
	}
	want := Profile{Provider: ProviderClaude, Agent: DefaultAgent, Effort: DefaultEffort}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default profile: got %+v, want %+v", got, want)
	}

	got, err = c.ProfileFor("/src/herdr-dispatch")
	if err != nil {
		t.Fatalf("override profile: %v", err)
	}
	want = Profile{
		Provider: ProviderCodex,
		Agent:    "implementer",
		Model:    "sonnet",
		Effort:   "high",
		Args:     []string{"--verbose"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override profile: got %+v, want %+v", got, want)
	}
}

// A profile becomes the argv herdr forwards after `--`: the agent name always,
// the model only when the profile names one, the effort always, then the
// profile's own extra arguments, in that order.
func TestAgentArgsRendersTheProfile(t *testing.T) {
	p := Profile{Provider: ProviderClaude, Agent: "claude", Effort: "low"}
	if got, want := p.AgentArgs(), []string{"--agent", "claude", "--effort", "low"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bare profile: got %v, want %v", got, want)
	}

	p = Profile{Provider: ProviderClaude, Agent: "reviewer", Model: "opus", Effort: "high", Args: []string{"--add-dir", "/tmp"}}
	want := []string{"--agent", "reviewer", "--model", "opus", "--effort", "high", "--add-dir", "/tmp"}
	if got := p.AgentArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("full profile: got %v, want %v", got, want)
	}
}

// The codex provider splices in its own --settings, so a profile that already
// carries one has to be caught before the two collide.
func TestHasSettingsArgSeesBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--settings", "{}"},
		{"--settings={}"},
		{"--verbose", "--settings", "/tmp/s.json"},
	} {
		if !(Profile{Args: args}).HasSettingsArg() {
			t.Fatalf("%v: want detected", args)
		}
	}
	if (Profile{Args: []string{"--verbose", "--settings-dir", "/tmp"}}).HasSettingsArg() {
		t.Fatal("--settings-dir is not --settings")
	}
}

// Every way a config can be wrong is refused at parse time, named.
func TestParseRefusesAConfigItCannotResolve(t *testing.T) {
	cases := map[string]string{
		"unknown provider": `default = "a"
[profiles.a]
provider = "gemini"
`,
		"missing provider": `default = "a"
[profiles.a]
`,
		"default names no one": `default = "b"
[profiles.a]
provider = "claude"
`,
		"project names no one": `default = "a"
[profiles.a]
provider = "claude"
[projects]
"/p" = "b"
`,
		"no default": `{"profiles":{"a":{"provider":"claude"}}}`,
		"no profiles": `default = "a"
`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

// Load reads the same document off disk and says which file it could not read.
func TestLoadReportsThePathItFailedOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dispatch.toml")
	const doc = `default = "a"
[profiles.a]
provider = "claude"
`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := c.ProfileFor("/anywhere"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	missing := filepath.Join(dir, "absent.json")
	_, err = Load(missing)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("want the path in the error, got %v", err)
	}
}

// The codex provider's launcher is a name the config carries, not a word
// compiled into this binary. It has been renamed once already.
func TestTheProxyLauncherDefaultsToProxenosAndCanBeOverridden(t *testing.T) {
	if DefaultProxy != "proxenos" {
		t.Fatalf("the default launcher is %q", DefaultProxy)
	}

	c, err := Parse([]byte(`default = "a"
[profiles.a]
provider = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Proxy; got != DefaultProxy {
		t.Fatalf("a config that names no launcher must resolve to %q, got %q", DefaultProxy, got)
	}

	c, err = Parse([]byte(`default = "a"
proxy = "/opt/bin/proxenos-next"
[profiles.a]
provider = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Proxy, "/opt/bin/proxenos-next"; got != want {
		t.Fatalf("the override was dropped: got %q, want %q", got, want)
	}
}

// The verification lane is off in a document that says nothing about it.
func TestTheVerificationLaneIsOffUnlessTheDocumentTurnsItOn(t *testing.T) {
	c, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Verify.Enabled {
		t.Fatal("the lane came on without being asked for")
	}
}

// On, it needs nothing else. The shot goes into the pane that did the work,
// which was launched from the worker's own profile, so there is no second
// launch for a document to configure.
func TestTheVerificationLaneNeedsNoProfileOfItsOwn(t *testing.T) {
	c, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
[verify]
enabled = true
`))
	if err != nil {
		t.Fatalf("a lane with nothing to configure was refused: %v", err)
	}
	if !c.Verify.Enabled {
		t.Fatal("the lane did not come on")
	}
}

// CRITERION 6. A document written for the verifier pane still names the
// profile that pane launched from. Nothing launches now, so the field has
// nothing left to name, and it is refused rather than accepted as a no-op:
// an operator who set it believes a verifier is running.
func TestAConfigStillNamingTheOldVerifierProfileIsRefusedByField(t *testing.T) {
	for _, doc := range []string{
		`default = "w"
[profiles.w]
provider = "claude"
[verify]
enabled = true
profile = "v"
`,
		`default = "w"
[profiles.w]
provider = "claude"
[verify]
enabled = false
profile = "v"
`,
		`default = "w"
[profiles.w]
provider = "claude"
[verify]
profile = ""
`,
	} {
		_, err := Parse([]byte(doc))
		if err == nil {
			t.Fatalf("accepted %s", doc)
		}
		if !strings.Contains(err.Error(), "verify.profile") {
			t.Fatalf("the refusal does not name verify.profile: %v", err)
		}
	}
}

// CRITERION 6. The measured pane-width cap lives in the config document, and
// the number the code falls back to is the number that was measured.
//
// It is a correctness bound rather than a preference, so it may be raised and
// never lowered: below MeasuredReadableColumns the dispatcher cannot trust
// what it reads off a worker's screen, and every judgement it makes about a
// worker comes from reading that screen.
func TestTheMeasuredPaneWidthCapIsInTheConfigAndCannotBeLowered(t *testing.T) {
	const profiles = "default = \"w\"\n[profiles.w]\nprovider = \"claude\"\n"

	t.Run("a document that says nothing takes the measured numbers", func(t *testing.T) {
		c, err := Parse([]byte(profiles))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if c.Layout.MinPaneColumns != MeasuredReadableColumns {
			t.Errorf("min_pane_columns defaulted to %d, and %d is what was measured",
				c.Layout.MinPaneColumns, MeasuredReadableColumns)
		}
		if c.Layout.MaxPanesPerTab != DefaultMaxPanesPerTab {
			t.Errorf("max_panes_per_tab defaulted to %d, want %d",
				c.Layout.MaxPanesPerTab, DefaultMaxPanesPerTab)
		}
	})

	t.Run("an operator may ask for wider", func(t *testing.T) {
		c, err := Parse([]byte(profiles + "[layout]\nmin_pane_columns = 80\n"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if c.Layout.MinPaneColumns != 80 {
			t.Errorf("min_pane_columns: got %d, want 80", c.Layout.MinPaneColumns)
		}
	})

	t.Run("narrower than what was measured is refused", func(t *testing.T) {
		_, err := Parse([]byte(profiles + "[layout]\nmin_pane_columns = 20\n"))
		if err == nil {
			t.Fatal("a pane narrower than the measured floor was accepted, and the dispatcher cannot read one")
		}
		if !strings.Contains(err.Error(), "min_pane_columns") {
			t.Errorf("the refusal does not name the key an operator has to change: %v", err)
		}
	})

	t.Run("a tab that may hold no worker is refused", func(t *testing.T) {
		if _, err := Parse([]byte(profiles + "[layout]\nmax_panes_per_tab = -1\n")); err == nil {
			t.Fatal("a tab that can never be spawned into was accepted")
		}
	})

}

// The grid rule: a new pane splits off the pane one GENERATION back, and the
// direction alternates by generation. Four panes are a 2x2.
func TestTheGridRulePutsFourPanesInATwoByTwo(t *testing.T) {
	for _, c := range []struct {
		held      int
		target    int
		direction string
	}{
		{1, 1, "right"},
		{2, 1, "down"},
		{3, 2, "down"},
		{4, 1, "right"},
		{5, 2, "right"},
		{8, 1, "down"},
	} {
		target, direction := GridSplit(c.held)
		if target != c.target || direction != c.direction {
			t.Errorf("GridSplit(%d) = pane %d %s, want pane %d %s",
				c.held, target, direction, c.target, c.direction)
		}
	}
}

// The cap is derived from the grid rule and BOTH measured floors rather than
// chosen: the largest pane count whose smallest pane still clears the column
// floor a marker needs to land on one line AND the row floor it needs to stay
// inside the detection buffer.
//
// Both halves are load-bearing and this test says so twice over. The pinned
// single-floor caps are what catch a derivation that drops one: with the row
// clause gone MaxPanesClearing(1, MeasuredReadableRows) no longer stops at
// the row-only cap, and with the column clause gone
// MaxPanesClearing(MeasuredReadableColumns, 1) no longer stops at the
// column-only cap. The combined assertion alone would not catch either,
// because only the tighter of the two floors decides the answer.
func TestTheMaxPanesPerTabDefaultIsTheMostBothMeasuredFloorsAllow(t *testing.T) {
	columnOnly := MaxPanesClearing(MeasuredReadableColumns, 1)
	if columnOnly != 16 {
		t.Errorf("the column floor alone allows %d panes, want 16", columnOnly)
	}
	rowOnly := MaxPanesClearing(1, MeasuredReadableRows)
	if rowOnly != 128 {
		t.Errorf("the row floor alone allows %d panes, want 128", rowOnly)
	}
	if columnOnly >= SearchCeiling || rowOnly >= SearchCeiling {
		t.Fatalf("the walk ran out of room rather than finding a floor: columns %d, rows %d, ceiling %d",
			columnOnly, rowOnly, SearchCeiling)
	}
	want := columnOnly
	if rowOnly < want {
		want = rowOnly
	}
	if DefaultMaxPanesPerTab != want {
		t.Errorf("the cap is %d, but the tighter of the two floors allows %d",
			DefaultMaxPanesPerTab, want)
	}
	if got := NarrowestColumns(DefaultMaxPanesPerTab); got < MeasuredReadableColumns {
		t.Errorf("%d panes leaves the narrowest at %d columns, under the %d floor",
			DefaultMaxPanesPerTab, got, MeasuredReadableColumns)
	}
	if got := ShortestRows(DefaultMaxPanesPerTab); got < MeasuredReadableRows {
		t.Errorf("%d panes leaves the shortest at %d rows, under the %d floor",
			DefaultMaxPanesPerTab, got, MeasuredReadableRows)
	}
	cols, rows := NarrowestColumns(DefaultMaxPanesPerTab+1), ShortestRows(DefaultMaxPanesPerTab+1)
	if cols >= MeasuredReadableColumns && rows >= MeasuredReadableRows {
		t.Errorf("%d panes still clears both floors at %d columns and %d rows, so the cap is set lower than the rule allows",
			DefaultMaxPanesPerTab+1, cols, rows)
	}
}

// The row ladder is the mirror of the column one: only the ODD generations
// split downwards, and a split costs measured chrome before it halves what is
// left. The numbers are what a 69-row window was measured to give.
func TestShortestRowsFollowsTheGridRuleAndTheMeasuredSplitCost(t *testing.T) {
	for _, c := range []struct{ panes, rows int }{
		{1, MeasuredWindowRows},
		{2, MeasuredWindowRows},
		{3, 32},
		{8, 32},
		{9, 14},
		{16, 14},
	} {
		if got := ShortestRows(c.panes); got != c.rows {
			t.Errorf("ShortestRows(%d) = %d, want %d", c.panes, got, c.rows)
		}
	}
}

// max-workers is the operator's number and it survives a restart: the config
// carries it, and the daemon flag wins only when it is passed.
func TestMaxWorkersLivesInTheConfigAndTheFlagOverridesIt(t *testing.T) {
	c, err := Parse([]byte(`default = "w"
max_workers = 4
[profiles.w]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.MaxWorkers != 4 {
		t.Errorf("max_workers: got %d, want 4", c.MaxWorkers)
	}
	if got := c.MaxWorkersOr(0); got != 4 {
		t.Errorf("with no flag the config's number did not survive: got %d, want 4", got)
	}
	if got := c.MaxWorkersOr(7); got != 7 {
		t.Errorf("the flag did not win: got %d, want 7", got)
	}

	d, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if d.MaxWorkers != DefaultMaxWorkers {
		t.Errorf("an absent max_workers defaulted to %d, want %d", d.MaxWorkers, DefaultMaxWorkers)
	}
	if _, err := Parse([]byte(`default = "w"
max_workers = -1
[profiles.w]
provider = "claude"
`)); err == nil {
		t.Error("a negative max_workers was accepted")
	}
}

// layout.min_pane_columns was validated, printed by doctor, and consulted by
// nothing: the pane count came from the measured constant whatever the
// document asked for. An operator who raises the floor is asking for wider
// panes, and the count is the only thing that delivers them.
func TestRaisingTheColumnFloorLowersThePaneCount(t *testing.T) {
	wide, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
[layout]
min_pane_columns = 120
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := MaxPanesClearing(120, MeasuredReadableRows)
	if wide.Layout.MaxPanesPerTab != want {
		t.Errorf("max_panes_per_tab = %d at a 120-column floor, want %d",
			wide.Layout.MaxPanesPerTab, want)
	}
	if wide.Layout.MaxPanesPerTab >= DefaultMaxPanesPerTab {
		t.Errorf("a wider floor did not lower the count: %d against the measured default %d",
			wide.Layout.MaxPanesPerTab, DefaultMaxPanesPerTab)
	}

	// An unraised floor still lands on the measured default, and an explicit
	// count is still the operator's own.
	plain, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plain.Layout.MaxPanesPerTab != DefaultMaxPanesPerTab {
		t.Errorf("max_panes_per_tab = %d, want the measured default %d",
			plain.Layout.MaxPanesPerTab, DefaultMaxPanesPerTab)
	}
	named, err := Parse([]byte(`default = "w"
[profiles.w]
provider = "claude"
[layout]
min_pane_columns = 120
max_panes_per_tab = 3
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if named.Layout.MaxPanesPerTab != 3 {
		t.Errorf("an explicit count was overwritten: %d", named.Layout.MaxPanesPerTab)
	}
}

// The policy gate key is `gate_command` in herdr-tasks and herdr-mail, and
// docs/repo-standard.md makes that the spelling all three plugins share. This
// repo spelled it `gate`, and an operator who learned the key on one board had
// it silently do nothing on this one.
func TestThePolicyGateKeyIsGateCommand(t *testing.T) {
	c, err := Parse([]byte(`
default = "w"
gate_command = ["/bin/true"]

[profiles.w]
provider = "claude"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := strings.Join(c.GateCommand, " "), "/bin/true"; got != want {
		t.Errorf("gate_command = %q, want %q", got, want)
	}
}

// A document carrying the old spelling is refused by name. DisallowUnknownFields
// would refuse it anyway, but "unknown field \"gate\"" does not tell an operator
// what to write instead, and a gate that quietly stops running is the failure
// §9.2 exists to prevent.
func TestTheOldGateKeyIsRefusedByName(t *testing.T) {
	_, err := Parse([]byte(`
default = "w"
gate = ["/bin/true"]

[profiles.w]
provider = "claude"
`))
	if err == nil {
		t.Fatal("a document naming the old `gate` key was accepted")
	}
	if !strings.Contains(err.Error(), "gate_command") {
		t.Errorf("the refusal does not name the key to use: %v", err)
	}
}
