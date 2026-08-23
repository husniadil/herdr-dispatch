package herdr

import (
	"encoding/json"
	"os"
	"testing"
)

// §11.2: a capability is named by the method Herdr lists, not by the CLI
// subcommand this binary types. `herdr pane run` is `pane.send_input` on the
// wire, and a constant that named the subcommand refused every dispatch on a
// real Herdr as UNSUPPORTED while the fake, which listed the same wrong name,
// stayed green. The fixture is the method and event list a released Herdr
// (0.8.2, protocol 20) printed from `herdr api schema --json`, so every
// constant here must be a name that Herdr actually answers to.
func TestEveryCapabilityIsAMethodAReleasedHerdrLists(t *testing.T) {
	raw, err := os.ReadFile("testdata/herdr-0.8.2-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Requests []string `json:"requests"`
		Events   []string `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	s := Schema{Requests: doc.Requests, Events: doc.Events}
	for _, cap := range []string{
		CapTabCreate, CapTabList, CapTabClose, CapPaneSplit, CapPaneRun, CapPaneRead,
		CapPaneList, CapPaneClose, CapAgentStart, CapAgentGet, CapAgentList, CapPrompt,
	} {
		if !s.Has(cap) {
			t.Errorf("capability %q is not a method herdr 0.8.2 lists; `herdr api schema --json` names the real one", cap)
		}
	}
}
