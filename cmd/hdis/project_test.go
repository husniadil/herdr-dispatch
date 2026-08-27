package main

import (
	"context"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/mcpdoor"
)

// §4.2: `--project` is a persistent flag on the root, so `hdis mcp --project
// <path>` parses whether or not anything reads it. This is what reads it: the
// board the door was started on becomes the default scope of every tool call
// that names none, rather than a flag that took effect nowhere.
func TestTheMCPDoorIsStartedOnTheBoardTheProjectFlagNames(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want mcpdoor.Options
	}{
		"no scope named":       {[]string{"mcp"}, mcpdoor.Options{}},
		"a board named":        {[]string{"mcp", "--project", "/src/p"}, mcpdoor.Options{Project: "/src/p"}},
		"a board and operator": {[]string{"mcp", "--operator", "--project", "/src/p"}, mcpdoor.Options{Operator: true, Project: "/src/p"}},
	} {
		var got mcpdoor.Options
		restore := serveMCP
		serveMCP = func(_ context.Context, _ string, _ mcpdoor.Caller, opt mcpdoor.Options) error {
			got = opt
			return nil
		}
		root := newRootCmd()
		root.SetArgs(tc.argv)
		err := root.Execute()
		serveMCP = restore
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != tc.want {
			t.Errorf("%s: the door was started with %+v, want %+v", name, got, tc.want)
		}
	}
}

// And the flag the door reads is the root's own, shared with every verb,
// rather than a second one declared on `hdis mcp` beside it: two spelled the
// same way would drift.
func TestTheDoorsProjectFlagIsTheRootsOwn(t *testing.T) {
	root := newRootCmd()
	if root.PersistentFlags().Lookup("project") == nil {
		t.Fatal("--project is not a persistent flag on the root (§4.2)")
	}
	for _, c := range root.Commands() {
		if c.Name() != "mcp" {
			continue
		}
		if c.InheritedFlags().Lookup("project") == nil {
			t.Error("`hdis mcp` does not inherit --project, so the flag it parses takes effect nowhere")
		}
		if c.LocalNonPersistentFlags().Lookup("project") != nil {
			t.Error("`hdis mcp` declares a --project of its own beside the root's")
		}
	}
}
