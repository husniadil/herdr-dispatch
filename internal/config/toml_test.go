package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestATopLevelDocumentTypesEveryScalarItReads(t *testing.T) {
	got, err := parseTOML(`
# the launch preset every project gets
default = "worker"      # trailing comments are dropped
max_workers = 3
enabled = true
off = false
gate = ["policy", "check"]
empty = []
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"default":     "worker",
		"max_workers": 3,
		"enabled":     true,
		"off":         false,
		"gate":        []any{"policy", "check"},
		"empty":       []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %#v, want %#v", got, want)
	}
}

func TestATableHeaderNestsAndADottedOneNestsTwice(t *testing.T) {
	got, err := parseTOML(`
[layout]
min_pane_columns = 40

[profiles.routed]
provider = "codex"
args = ["--add-dir", "/srv/shared"]
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"layout": map[string]any{"min_pane_columns": 40},
		"profiles": map[string]any{
			"routed": map[string]any{"provider": "codex", "args": []any{"--add-dir", "/srv/shared"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsed %#v, want %#v", got, want)
	}
}

// A project path is a key, and a path is not a bare word. Quoting it is the
// TOML answer, and a parser that did not accept one would leave the
// per-project override unwritable.
func TestAQuotedKeyIsAPath(t *testing.T) {
	got, err := parseTOML("[projects]\n\"/Users/me/some repo\" = \"routed\"\n")
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := got["projects"].(map[string]any)
	if projects["/Users/me/some repo"] != "routed" {
		t.Errorf("parsed %#v", got)
	}
}

// A hash inside a quoted value is part of the value. Stripping it would
// silently truncate a project path or an argv word.
func TestAHashInsideQuotesIsNotAComment(t *testing.T) {
	got, err := parseTOML(`model = "sonnet#4"`)
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "sonnet#4" {
		t.Errorf("parsed %#v", got)
	}
}

// The whole reason this parser refuses rather than shrugs: a setting an
// operator wrote and the binary silently dropped is worse than no parser.
// Every shape this subset does not cover names its line.
func TestWhatThisSubsetDoesNotCoverIsRefusedByLine(t *testing.T) {
	for name, src := range map[string]string{
		"an inline table":                 "layout = { min_pane_columns = 40 }",
		"an array of tables over a value": "route = 1\n[[route]]\nprofile = \"heavy\"",
		"a multi-line array":              "args = [\n  \"--add-dir\",\n]",
		"an unquoted string":              "default = worker",
		"a bare word as a key":            "min pane columns = 40",
		"a line that is not one":          "default",
		"an unclosed header":              "[layout",
		"a key set twice":                 "default = \"a\"\ndefault = \"b\"",
		"a table over a value":            "layout = 1\n[layout]\nmin_pane_columns = 40",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseTOML(src)
			if err == nil {
				t.Fatalf("%s parsed without complaint", name)
			}
			if !strings.Contains(err.Error(), "line ") {
				t.Errorf("the refusal does not name the line: %v", err)
			}
		})
	}
}

// The routing table is written as an array of tables, which is the shape TOML
// gives a repeated `[[route]]`. The parser refused every one of them until
// routing needed one, and a refusal here would have been a config an operator
// wrote and this binary would not read.
func TestAnArrayOfTablesReadsBackAsAList(t *testing.T) {
	doc, err := parseTOML(`[[route]]
min_priority = 3
profile = "medium"

[[route]]
min_priority = 5
profile = "heavy"
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rows, ok := doc["route"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("route: %#v", doc["route"])
	}
	first, _ := rows[0].(map[string]any)
	second, _ := rows[1].(map[string]any)
	if first["min_priority"] != 3 || first["profile"] != "medium" {
		t.Fatalf("first route: %#v", first)
	}
	if second["min_priority"] != 5 || second["profile"] != "heavy" {
		t.Fatalf("second route: %#v", second)
	}
}

// A header that opens as an array of tables and closes as a plain one is a
// typo, and a parser that read it as either would put the operator's keys
// somewhere they never asked for.
func TestAnArrayOfTablesHeaderMustCloseAsOne(t *testing.T) {
	if _, err := parseTOML("[[route]\nprofile = \"heavy\"\n"); err == nil {
		t.Fatal("a half-closed array-of-tables header was accepted")
	}
}

// A name already holding a table cannot also be a list of them.
func TestATableCannotAlsoBeAnArrayOfTables(t *testing.T) {
	if _, err := parseTOML("[route]\nprofile = \"heavy\"\n\n[[route]]\nprofile = \"medium\"\n"); err == nil {
		t.Fatal("a table was silently turned into a list")
	}
}
