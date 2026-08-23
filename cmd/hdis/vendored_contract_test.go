package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/version"
)

// contractFile is the vendored contract, which is what a citation resolves
// against. It is in the repository so that a reader who has only this
// repository can follow every § the code cites. herdr-tasks' copy is the
// source of truth; this one is a byte copy of it.
const contractFile = "docs/contract.md"

// citation matches "§6.1" and bare "§6", the two forms the repository writes.
var citation = regexp.MustCompile(`§[0-9]+(?:\.[0-9]+)?`)

// contractAnchor matches what the contract DEFINES: a section heading
// ("## §6 Verbs, CLI, and error envelope") and a numbered clause at the start
// of its own line ("§6.1 Every verb ...").
var contractAnchor = regexp.MustCompile(`(?m)^(?:#+ )?(§[0-9]+(?:\.[0-9]+)?)`)

// §13.4: a citation nobody can resolve is a citation nobody checks. Before
// the contract was vendored here, every § this repository wrote pointed at a
// document living in another repository — which is to say at nothing a reader
// of this one could open. This reads every tracked file towards the contract,
// whatever its extension: citations are in .go, .md, .sh and .toml alike.
func TestContractCitationsResolve(t *testing.T) {
	defined := contractAnchors(t)
	if len(defined) < 50 {
		t.Fatalf("%s defines %d anchors; the contract is not being read", contractFile, len(defined))
	}
	files := trackedFiles(t)
	if len(files) < 20 {
		t.Fatalf("scanned %d tracked files; the file list is reading nothing", len(files))
	}

	cited := map[string][]string{}
	for _, name := range files {
		if name == contractFile {
			continue
		}
		body, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			if os.IsNotExist(err) {
				// Tracked and deleted in the working tree: nothing to read.
				continue
			}
			t.Fatalf("read %s: %v", name, err)
		}
		for _, c := range citation.FindAllString(string(body), -1) {
			if !contains(cited[c], name) {
				cited[c] = append(cited[c], name)
			}
		}
	}
	if len(cited) == 0 {
		t.Fatal("no citations found in any tracked file; the pattern is reading nothing")
	}

	unresolved := []string{}
	for c, where := range cited {
		if !defined[c] {
			unresolved = append(unresolved, fmt.Sprintf("%s (cited in %s)", c, strings.Join(where, ", ")))
		}
	}
	sort.Strings(unresolved)
	for _, u := range unresolved {
		t.Errorf("%s resolves to nothing in %s", u, contractFile)
	}
	// Logged, not just floored: after a contract revision this number is the
	// difference between a citation set that shrank and one that stopped
	// being read.
	t.Logf("%d distinct citations across %d tracked files, %d unresolved, against %d anchors in %s",
		len(cited), len(files), len(unresolved), len(defined), contractFile)
}

func contractAnchors(t *testing.T) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", contractFile))
	if err != nil {
		t.Fatalf("read %s: %v", contractFile, err)
	}
	out := map[string]bool{}
	for _, m := range contractAnchor.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = true
	}
	return out
}

// trackedFiles is every tracked file. Tracked, because an untracked scratch
// file is not something this repository ships.
func trackedFiles(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	names := []string{}
	for _, b := range strings.Split(string(out), "\x00") {
		if b != "" {
			names = append(names, b)
		}
	}
	return names
}

func contains(all []string, s string) bool {
	for _, v := range all {
		if v == s {
			return true
		}
	}
	return false
}

// anyContractVersion matches the README naming a version OF THE CONTRACT, in
// whatever wording, so a page that declares one revision and mentions another
// is caught. It is anchored on the phrase rather than on a bare semver
// because the same page states herdr's version and claude's, which are
// different numbers that move on their own.
var anyContractVersion = regexp.MustCompile(`(?i)(?:version|revision) \*{0,2}([0-9]+\.[0-9]+\.[0-9]+(?:-[a-z]+)?)\*{0,2} of the (?:herdr plugin )?contract`)

// contractVersion matches the revision the vendored contract states for itself.
var contractVersion = regexp.MustCompile(`(?m)^Status:.*\bVersion: ([0-9][^.\s]*(?:\.[^.\s]*)*?)\. `)

// §13.4: the declared revision is a conformance claim, so it does not move
// without the vendored text moving with it. The constant and the document
// were never read together before the contract lived here at all; this reads
// them together. Bumping the constant without checking the deltas is still
// possible — a test cannot verify conformance — but declaring a revision the
// vendored text does not carry is not.
func TestTheDeclaredRevisionIsTheVendoredOne(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", contractFile))
	if err != nil {
		t.Fatalf("read %s: %v", contractFile, err)
	}
	m := contractVersion.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s states no Version on its Status line", contractFile)
	}
	stated := string(m[1])
	// The declaration may LAG the vendored document, and only that way: an
	// amendment lands in the text before any plugin has been brought to it,
	// and a plugin that declared the new revision on the day it was written
	// would be claiming conformance it has not done the work for. Declaring a
	// revision HIGHER than the vendored one is conformance to a text that is
	// not in this repository at all, which is worse than the lag it looks
	// like. And the gap must be a RECORDED one: the two revisions named
	// together in a single entry of docs/contract-notes.md.
	if stated != version.Contract {
		if !revisionLower(version.Contract, stated) {
			t.Fatalf("this binary declares revision %s against a contract vendored at %s; a "+
				"declaration may only lag the document, never lead it, and %s is not in this "+
				"repository for anyone to read", version.Contract, stated, version.Contract)
		}
		if !gapRecorded(t, version.Contract, stated) {
			t.Fatalf("this binary declares revision %s against a contract vendored at %s and "+
				"docs/contract-notes.md has no single entry naming both; a lag nobody wrote "+
				"down is the silent drift this test exists to catch", version.Contract, stated)
		}
		t.Logf("declared revision %s lags the vendored %s; docs/contract-notes.md records the gap",
			version.Contract, stated)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	// The declaration itself, not merely the string somewhere on the page: a
	// sentence ABOUT another revision contains the same token.
	declared := "**version " + version.Contract + "** of the Herdr plugin contract"
	if !strings.Contains(string(readme), declared) {
		t.Errorf("README does not declare %q, which §13.4 requires it to state "+
			"alongside doctor output", declared)
	}
	// And no second revision is named anywhere, because a page that declares
	// one revision and mentions another is the drift this guard closes.
	for _, m := range anyContractVersion.FindAllStringSubmatch(string(readme), -1) {
		if other := m[1]; other != version.Contract {
			t.Errorf("README names contract version %s as well as the declared %s", other, version.Contract)
		}
	}
}

// revisionNumbers turns "0.7.0" or "0.4.0-draft" into its numeric fields. A
// pre-release suffix orders BELOW the release it leads to.
func revisionNumbers(v string) ([]int, bool, bool) {
	pre := false
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], true
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil, false, false
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false, false
		}
		out[i] = n
	}
	return out, pre, true
}

func revisionLower(a, b string) bool {
	an, apre, aok := revisionNumbers(a)
	bn, bpre, bok := revisionNumbers(b)
	if !aok || !bok {
		return false
	}
	for i := range an {
		if an[i] != bn[i] {
			return an[i] < bn[i]
		}
	}
	// Same numbers: a pre-release is below the release, nothing else is.
	return apre && !bpre
}

// gapRecorded reports whether docs/contract-notes.md names both revisions in
// ONE entry. An entry is a paragraph — the unit a reader takes in as a single
// statement — because "both strings appear in the file" is satisfied by a
// document that has accumulated every revision the project ever had. Table
// rows are dropped before the split: a markdown table carries no blank line,
// so a table naming several revisions would read as one paragraph and let any
// lag through.
func gapRecorded(t *testing.T, declared, vendored string) bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "contract-notes.md"))
	if err != nil {
		t.Fatalf("read docs/contract-notes.md: %v", err)
	}
	prose := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		prose = append(prose, line)
	}
	for _, para := range strings.Split(strings.Join(prose, "\n"), "\n\n") {
		if strings.Contains(para, declared) && strings.Contains(para, vendored) {
			return true
		}
	}
	return false
}
