package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// §7.5: `hdis mcp` MUST accept a flag spelled --operator, and MUST NOT accept
// that declaration by any other route. The flag half is here because only this
// package holds the whole command tree — the verbs come from internal/cli and
// the three commands that are not verbs are added beside them here. The door
// half — no tool argument, no per-call declaration — is pinned in
// internal/mcpdoor.
func TestTheOperatorDeclarationIsAFlagOnTheMCPCommandAndNowhereElse(t *testing.T) {
	root := newRootCmd()
	var mcp *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			mcp = c
		}
	}
	if mcp == nil {
		t.Fatal("`hdis mcp` is not a subcommand")
	}
	f := mcp.Flags().Lookup("operator")
	if f == nil {
		t.Fatal("`hdis mcp` takes no --operator flag (§7.5)")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--operator is a %s; the declaration is made or not made", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--operator defaults to %q; a declaration nobody wrote is not one (§3.7)", f.DefValue)
	}

	// And nowhere else. A persistent flag would put the declaration on every
	// verb, which is the per-call declaration §7.5 exists instead of, and no
	// other command may carry it: the door's identity comes from the command
	// that STARTS the door. `hdis daemon --pane` is a different thing — it
	// names the pane workers are split off, not who a caller is.
	if root.PersistentFlags().Lookup("operator") != nil {
		t.Error("--operator is a persistent flag; §7.5 reads it from the server command alone")
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub != mcp && sub.Flags().Lookup("operator") != nil {
				t.Errorf("`hdis %s` also takes --operator; there is one route and it is the door's", sub.Name())
			}
			walk(sub)
		}
	}
	walk(root)
}
