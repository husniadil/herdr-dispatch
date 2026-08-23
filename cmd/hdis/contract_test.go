package main

import (
	"os"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// §7.3: the CLI is the primary agent surface, and the skill is what teaches
// it. The skill is prose, so most of it is free to be rewritten — but a few
// of its claims are load-bearing, and an agent that believes a stale one acts
// wrongly. Those are pinned here, along with the tool list, which is the verb
// registry's own.
//
// Each pin is the PHRASE that carries the claim rather than a keyword the
// claim happens to contain. A single word survives its own sentence being
// rewritten to mean the opposite; "returns at once" does not.
func TestTheSkillTeachesWhatTheRegistrySays(t *testing.T) {
	skill := readSkill(t)
	// A claim is a sentence, and a sentence in this file is wrapped and
	// emphasised. Match the pins against the prose with its line breaks and
	// its bold markers taken out, so a pin is free to be the whole phrase
	// rather than the longest fragment that happens to fit on one line.
	prose := flatten(skill)
	if !strings.HasPrefix(skill, "---\nname: dispatch\n") {
		t.Error("the skill does not open with frontmatter naming it `dispatch`")
	}
	must := map[string]string{
		"that dispatch reserves and returns without waiting":   "reserves the task and returns at once",
		"that a spawn outlives the call, so status is the way": "does not wait for a worker",
		"that the worker claims the task itself":               "the worker claims the task itself",
		"that this binary claims on nobody's behalf":           "nothing here claims on its behalf",
		"that hdis stops at review and never rules on one":     "never runs `task approve` or `task reject`",
		"that a cross-board id is the 26-character one":        "must be the 26-character id",
		"why a number is not one":                              "only unique inside a project",
		"that the profile is config and not an argument":       "not selectable per call",
		"what HDIS_DISPATCHER_PANE is":                         "the address you owe your report at",
		"that the variable never says who the reader is":       "never says who you are",
		"that doctor answers before a dispatch is tried":       "says why a dispatch would refuse",
		"where the rest of the surface lives":                  "hdis --help",
	}
	for what, want := range must {
		if !strings.Contains(prose, want) {
			t.Errorf("the skill does not teach %s (looked for %q)", what, want)
		}
	}
	// Every tool the door publishes is named, and nothing the door withholds
	// is named as a tool. Read off the registry rather than hand-listed, so a
	// verb added to either door fails here until the skill catches up. The
	// claim lives in one sentence, so that is where the parity is checked:
	// looking line by line would let a withheld verb be added on the line
	// after the one saying "MCP tools" and pass.
	list, ok := toolSentence(prose)
	if !ok {
		t.Fatal("the skill has no sentence naming the MCP tools")
	}
	for _, v := range verbs.All {
		named := strings.Contains(list, "`"+v.Name+"`")
		if v.CLIOnly && named {
			t.Errorf("the skill lists %q among the MCP tools; the door withholds it", v.Name)
		}
		if !v.CLIOnly && !named {
			t.Errorf("the skill does not name the MCP tool %q", v.Name)
		}
	}
}

// flatten renders the skill as one line of lowercase prose with its emphasis
// removed: the text an agent reads, rather than the text as it is wrapped. A
// pin is a claim, and where a claim happens to open a sentence is not one of
// the things it claims.
func flatten(skill string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(skill, "**", "")), " "))
}

// toolSentence returns the sentence that names the MCP tools. The tool list
// is one claim, and a claim is checked whole.
func toolSentence(prose string) (string, bool) {
	for _, sentence := range strings.Split(prose, ". ") {
		if strings.Contains(sentence, "mcp tools") {
			return sentence, true
		}
	}
	return "", false
}

// The skill only reaches an agent if it is linked, and nothing in the Herdr
// manifest links it. The README is where that is said.
func TestTheREADMESaysHowTheSkillIsInstalled(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(raw)
	for what, want := range map[string]string{
		"the symlink line for the skill":      "skills/dispatch",
		"that it is a symlink":                "ln -s",
		"that the manifest does not place it": "manifest",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the README does not carry %s (looked for %q)", what, want)
		}
	}
}

func readSkill(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../skills/dispatch/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The operator's worker count survives a restart, and that is decided HERE
// rather than in config: a flag whose default is a number would overwrite the
// config document on every restart that omitted it.
//
// Pinned at the call site because that is where the choice is made. Putting
// max_workers in the config and then still passing the raw flag through would
// leave every config test green and the operator's number still dropped.
func TestTheDaemonTakesItsWorkerCountFromTheConfigUnlessTheFlagIsPassed(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, `fs.Int("max-workers", 0,`) {
		t.Error(`the max-workers flag carries a default of its own, so an unpassed flag overwrites the config's max_workers on every restart`)
	}
	if !strings.Contains(text, "MaxWorkers:   cfg.MaxWorkersOr(*maxWorkers)") {
		t.Error("the daemon's worker count is not resolved through cfg.MaxWorkersOr, so the config document's number never reaches the policy")
	}
}
