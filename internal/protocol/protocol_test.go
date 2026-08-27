package protocol

import (
	"encoding/json"
	"testing"
)

// §3.2 with §7.5: the principal is what the call declared with --as, else the
// pane it runs in, else the operator when the DOOR was started with the §7.5
// declaration, else the literal `none` of §3.7. It is never more than the
// daemon knows, and an undeclared caller outside a pane is `none` rather than
// anyone.
func TestTheCallerIsDerivedAndNeverMoreThanTheDaemonKnows(t *testing.T) {
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{}, "none"},
		{Request{Pane: "wM:p1"}, "agent:wM:p1"},
		// A declared principal wins over the pane, which is what makes a
		// plugin's own call name the plugin rather than the pane it fired
		// from.
		{Request{As: "plugin:hdis", Pane: "wM:p1"}, "plugin:hdis"},
		// §7.5: a door started with the declaration is the operator, and the
		// pane is read FIRST — an agent that starts a declared door gains
		// nothing by it, because its calls are still its pane's.
		{Request{Operator: true}, "human"},
		{Request{Operator: true, Pane: "wM:p1"}, "agent:wM:p1"},
		{Request{Operator: true, As: "plugin:hdis"}, "plugin:hdis"},
	} {
		if got := tc.req.Caller(); got != tc.want {
			t.Errorf("Caller(%+v) = %q, want %q", tc.req, got, tc.want)
		}
	}
}

// The wire carries only what was set: every optional field is omitempty, so a
// door that declared nothing sends no declaration rather than sending false.
func TestTheWireCarriesOnlyWhatWasSet(t *testing.T) {
	raw, err := json.Marshal(Request{Verb: "status"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"args", "pane", "door", "follow", "project", "all_projects", "as", "operator"} {
		if _, ok := wire[absent]; ok {
			t.Errorf("an unset %q was sent: %s", absent, raw)
		}
	}
}
