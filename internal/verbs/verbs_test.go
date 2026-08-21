package verbs

import (
	"strings"
	"testing"
)

func TestEveryVerbIsWholeOnBothDoors(t *testing.T) {
	for _, v := range All {
		if v.Name == "" || v.Short == "" {
			t.Errorf("verb %+v has no name or no help", v)
		}
		if len(v.CLI) == 0 {
			t.Errorf("verb %q has no CLI subcommand", v.Name)
		}
		if strings.ContainsAny(v.Name, "_ ") {
			t.Errorf("verb %q is not a bare word, and the MCP tool name is the verb", v.Name)
		}
		for _, a := range v.Args {
			if a.Name == "" || a.Desc == "" {
				t.Errorf("verb %q has an unnamed or undescribed argument", v.Name)
			}
			if a.Type != String {
				t.Errorf("verb %q argument %q has type %q, which no door can render", v.Name, a.Name, a.Type)
			}
		}
	}
}

func TestNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range All {
		if seen[v.Name] {
			t.Errorf("verb %q is declared twice", v.Name)
		}
		seen[v.Name] = true
	}
}

func TestByNameFindsAVerbAndRefusesAnUnknownOne(t *testing.T) {
	v, ok := ByName("dispatch")
	if !ok {
		t.Fatal("ByName(dispatch) found nothing")
	}
	if len(v.Args) != 1 || v.Args[0].Name != "task" || !v.Args[0].Required || !v.Args[0].Positional {
		t.Errorf("dispatch takes %+v, want one required positional task", v.Args)
	}
	if _, ok := ByName("promote"); ok {
		t.Error("ByName(promote) found a verb this binary does not have")
	}
}

func TestByCLIMatchesTheSubcommandPath(t *testing.T) {
	v, ok := ByCLI([]string{"status"})
	if !ok || v.Name != "status" {
		t.Fatalf("ByCLI(status) = %+v, %v", v, ok)
	}
	if _, ok := ByCLI([]string{"run"}); ok {
		t.Error("ByCLI(run) matched a verb; run is not one")
	}
}

// The table is pinned so a verb is added on purpose. It moved once, from
// three to four: stop joined it because a daemon a door autostarted has no
// terminal to answer SIGINT from and could only be killed by pid. stop is
// CLI-only, so the MCP tool list stayed at three.
func TestTheTableIsTheFourVerbsAndNoMore(t *testing.T) {
	var got []string
	for _, v := range All {
		got = append(got, v.Name)
	}
	want := []string{"doctor", "dispatch", "stop", "status"}
	if len(got) != len(want) {
		t.Fatalf("verbs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("verbs = %v, want %v", got, want)
		}
	}
}
