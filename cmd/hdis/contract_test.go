package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// §7.3: both doors are first-class and carry the same verbs, and the skill is
// what teaches an agent which they are. The skill is prose, so most of it is free to be rewritten — but a few
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
		if !strings.Contains(list, "`"+v.Name+"`") {
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

// max_workers reads two ways: how many panes may exist, and how many agents
// may be spending tokens at once. The code implements the first, and both the
// config and the README have to say which one it is, or an operator raises the
// number expecting throughput they will not get.
func TestTheMaxWorkersReadingIsWrittenDownInTheConfigAndTheREADME(t *testing.T) {
	const sentence = "max_workers bounds how many worker panes may exist at once, and not how many agents may be spending tokens at once"
	for _, path := range []string{"../../README.md", "../../internal/config/config.go"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// A Go comment wraps the sentence across lines with a "//" on each,
		// so the markers come out before the words are compared.
		prose := strings.ReplaceAll(string(raw), "//", " ")
		if !strings.Contains(flatten(prose), flatten(sentence)) {
			t.Errorf("%s does not say which of the two things max_workers bounds (looked for %q)", path, sentence)
		}
	}
}

// A tool list that names every served verb is not enough on its own. The
// commit that put `stop` on the door moved the list, the pinned set and the
// README table, and left a paragraph two lines below the list still saying
// "`stop` is deliberately not a tool" — every name was present, so a guard
// that only asks "is each verb named" stayed green over a document arguing
// with itself.
//
// So the docs are also read for a claim that a served verb is withheld. The
// phrases are the ones this repository actually used, and a document that
// wants to say something like it about a verb the door does not serve has no
// such verb to say it about: there are none.
func TestNoDocumentSaysAServedVerbIsWithheld(t *testing.T) {
	claims := []string{
		"is deliberately not a tool",
		"is not a tool",
		"is the one cli-only verb",
		"none, on purpose",
	}
	for _, name := range []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "skills", "dispatch", "SKILL.md"),
	} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		flat := flatten(string(raw))
		for _, claim := range claims {
			at := strings.Index(flat, claim)
			if at < 0 {
				continue
			}
			// Which verb is it about: the last one named before the claim.
			subject := ""
			for _, v := range verbs.All {
				if i := strings.LastIndex(flat[:at], v.Name); i >= 0 {
					if j := strings.LastIndex(flat[:at], subject); subject == "" || i > j {
						subject = v.Name
					}
				}
			}
			t.Errorf("%s says %q about %q, and the door serves every verb", name, claim, subject)
		}
	}
}
