package htask

import (
	"context"
	"errors"
	"testing"
)

// §11.7: the board releases the leases of a pane HERDR no longer lists when a
// plugin principal asks. The dispatcher is the caller — it holds no leases of
// its own — so what is pinned here is the call it makes and the answer it
// reads.
func TestSweepingAGonePaneAsksTheBoardForThatPaneAlone(t *testing.T) {
	c, f := client(t)
	c.Principal = PrincipalFor("wM:p1")
	f.Bin(t, "htask", `echo '{"released":["01AAA","01BBB"],"count":2}'`)

	released, err := c.SweepPane(context.Background(), "wM:p9")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(released) != 2 || released[0] != "01AAA" || released[1] != "01BBB" {
		t.Fatalf("released %v", released)
	}
	want := "sweep --pane wM:p9 --json --as plugin:hdis@wM:p1"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// The `--as` a plugin principal declares is CLI-only and is refused from a
// process carrying a pane, and the daemon carries one whenever it was started
// from a door inside a Herdr pane. The scrub every board call already makes is
// what makes this call possible at all, so it is pinned on THIS call rather
// than trusted to a neighbour's test.
func TestTheSweepCarriesNoPaneIntoTheSubprocess(t *testing.T) {
	c, f := client(t)
	c.Principal = PrincipalFor("wM:p1")
	t.Setenv("HERDR_PANE_ID", "wM:p1")
	t.Setenv("HERDR_TAB_ID", "wM:t1")
	t.Setenv("HERDR_WORKSPACE_ID", "wM")
	f.Bin(t, "htask", `env > "$HDIS_FAKE_DIR/env.txt"
echo '{"released":[],"count":0}'`)

	if _, err := c.SweepPane(context.Background(), "wM:p9"); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	child := childEnv(t, f)
	for _, name := range []string{"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID"} {
		if v, ok := child[name]; ok {
			t.Fatalf("%s reached the sweep as %q; the board refuses --as from a pane", name, v)
		}
	}
}

// A refusal comes back as the board's own code, because FORBIDDEN — the pane
// is alive after all — and UNAVAILABLE — Herdr could not be asked — are
// different answers and the caller acts on them differently.
func TestASweptPaneTheBoardRefusesCarriesItsCode(t *testing.T) {
	for name, tc := range map[string]struct{ body, code string }{
		"the pane is alive": {`{"error":{"code":"FORBIDDEN","message":"herdr still lists pane wM:p9"}}`, "FORBIDDEN"},
		"herdr is down":     {`{"error":{"code":"UNAVAILABLE","message":"herdr could not be asked"}}`, "UNAVAILABLE"},
		"herdr is too old":  {`{"error":{"code":"UNSUPPORTED","message":"this herdr cannot list panes"}}`, "UNSUPPORTED"},
	} {
		t.Run(name, func(t *testing.T) {
			c, f := client(t)
			f.Bin(t, "htask", `echo '`+tc.body+`'; exit 4`)
			_, err := c.SweepPane(context.Background(), "wM:p9")
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("sweep answered %v, want a refusal", err)
			}
			if refusal.Code != tc.code {
				t.Fatalf("refusal code %q, want %q", refusal.Code, tc.code)
			}
		})
	}
}
