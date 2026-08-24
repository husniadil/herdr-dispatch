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
		// The socket verb is the CLI path dotted and the tool name is the
		// same path underscored, and both are written down rather than
		// derived at a door. Reading them off the CLI path here is what
		// keeps `parked list`, `parked.list` and `parked_list` the same
		// thing on all three surfaces.
		if want := strings.Join(v.CLI, "."); v.Name != want {
			t.Errorf("verb %q is served over CLI as %q, so the socket verb is %q", v.Name, strings.Join(v.CLI, " "), want)
		}
		if want := strings.Join(v.CLI, "_"); v.MCP != want {
			t.Errorf("verb %q publishes the tool name %q, want %q", v.Name, v.MCP, want)
		}
		for _, a := range v.Args {
			if a.Name == "" || a.Desc == "" {
				t.Errorf("verb %q has an unnamed or undescribed argument", v.Name)
			}
			if a.Type != String && a.Type != Bool && a.Type != Int {
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

// The table is pinned so a verb is added on purpose. It has moved twice: stop
// joined it because a daemon a door autostarted has no terminal to answer
// SIGINT from and could only be killed by pid, and the two parked verbs
// joined it with the §9 policy gate, which has nowhere to put a deferral an
// operator cannot then read and resolve; `dump` came with §5.8, which asks
// that a plugin's data be readable without the plugin.
// §9.1 with §9.2: a verb that changes the world passes the gate, and one that
// changes the world without passing it says why. Neither half is inferable
// from the code — a verb added with Mutates false is simply never checked, and
// that reads exactly like a verb nobody has gated yet.
func TestEveryWritingVerbEitherPassesTheGateOrSaysWhyNot(t *testing.T) {
	for _, v := range All {
		switch {
		case !v.Mutates && (v.Gated != "" || v.Ungated != ""):
			t.Errorf("verb %q reads only and still answers to the policy gate", v.Name)
		case v.Mutates && v.Gated == "" && v.Ungated == "":
			t.Errorf("verb %q changes the world, passes no name to the policy gate, and says nothing about why (§9.1)", v.Name)
		case v.Mutates && v.Gated != "" && v.Ungated != "":
			t.Errorf("verb %q is gated as %q and also excuses itself; one or the other", v.Name, v.Gated)
		}
		// §9.4 fixes the shape of the name a policy plugin matches on.
		if v.Gated != "" && v.Gated != "dispatch."+v.Name {
			t.Errorf("verb %q is gated as %q; §9.4 names it dispatch.%s", v.Name, v.Gated, v.Name)
		}
	}
}

// §9.4: the gated set is a list a future policy plugin names, so it moves on
// purpose and not as a side effect of a verb gaining Mutates.
func TestTheGatedSetIsTheTwoWorldChangingVerbs(t *testing.T) {
	got := GatedVerbs()
	want := []string{"dispatch.dispatch", "dispatch.stop"}
	if len(got) != len(want) {
		t.Fatalf("gated verbs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gated verbs = %v, want %v", got, want)
		}
	}
}

func TestTheTableIsTheEightVerbsAndNoMore(t *testing.T) {
	var got []string
	for _, v := range All {
		got = append(got, v.Name)
	}
	want := []string{"doctor", "dispatch", "stop", "status", "dump", "events", "parked.list", "parked.resolve"}
	if len(got) != len(want) {
		t.Fatalf("verbs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("verbs = %v, want %v", got, want)
		}
	}
}
