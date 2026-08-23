package herdr

import (
	"context"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/fake"
)

// §11.2 fixes the document's shape: request methods are the `const` values
// under schemas.request.oneOf[].properties.method, and event kinds are the
// enum at schemas.event.$defs.EventKind. This is the shape Herdr actually
// prints, so it is the one that matters.
func TestTheJSONSchemaShapeIsRead(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"ok"}}'`)
	f.Write(t, fake.HerdrSchemaFile, `{"protocol":7,"schemas":{
	  "request":{"oneOf":[
	    {"properties":{"method":{"const":"pane.send_input"}}},
	    {"properties":{"method":{"const":"agent.get"}}}]},
	  "event":{"$defs":{"EventKind":{"enum":["pane_exited"]}}}}}`)

	s, err := c.Schema(context.Background())
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if s.Protocol != 7 {
		t.Errorf("protocol = %d, want 7", s.Protocol)
	}
	for _, want := range []string{"pane.send_input", "agent.get", "pane_exited"} {
		if !s.Has(want) {
			t.Errorf("the schema does not report %s: %+v", want, s)
		}
	}
	// Herdr spells its event kinds with underscores where the contract
	// writes dots, so both spellings answer.
	if !s.Has("pane.exited") {
		t.Error("pane.exited does not match herdr's own pane_exited")
	}
	if s.Has("tab.create") {
		t.Error("a request this Herdr did not list was reported as offered")
	}
}

// §11.2 also requires the flat `{"requests":[...],"events":[...]}` form to be
// accepted, so a future Herdr that simplifies the document keeps working.
func TestTheFlatShapeIsAlsoRead(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"ok"}}'`)
	f.Write(t, fake.HerdrSchemaFile, `{"protocol":2,"requests":["tab.create"],"events":["pane_exited"]}`)
	s, err := c.Schema(context.Background())
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !s.Has("tab.create") || !s.Has("pane_exited") {
		t.Errorf("the flat document was not read: %+v", s)
	}
}

// §11.2: read it ONCE at daemon start. A client that asked per verb would
// spend a process on every call and could see two different answers in one
// tick.
func TestTheSchemaIsReadOnce(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"ok"}}'`)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := c.Require(ctx, CapPaneRun); err != nil {
			t.Fatalf("require: %v", err)
		}
	}
	reads := 0
	for _, call := range f.Calls(t) {
		if strings.HasPrefix(call, "api schema") {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("herdr api schema was run %d times; §11.2 reads it once", reads)
	}
}

// §11.2: a missing capability is UNSUPPORTED at the verb that needs it, with
// the capability NAMED. An operator whose tab create refuses has to be able to
// tell "this Herdr cannot" from "this plugin is broken".
func TestAMissingCapabilityIsUnsupportedAtTheVerbThatNeedsIt(t *testing.T) {
	c, f := client(t)
	// A Herdr with everything but tab.create.
	f.Write(t, fake.HerdrSchemaFile, `{"protocol":1,"requests":["pane.send_input","pane.read","agent.get"]}`)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"ok"}}'`)

	_, _, err := c.TabCreate(context.Background(), "wM", "/src/p", "hdis-7")
	if got := codes.Of(err); got != codes.Unsupported {
		t.Fatalf("tab create on a Herdr without it = %v (%s), want UNSUPPORTED", err, got)
	}
	if !strings.Contains(err.Error(), "tab.create") {
		t.Errorf("the refusal does not name the capability: %v", err)
	}
	// And it refused BEFORE calling: a verb that ran anyway would have the
	// capability check for decoration.
	for _, call := range f.Calls(t) {
		if strings.HasPrefix(call, "tab create") {
			t.Errorf("tab create ran on a Herdr that does not offer it: %q", call)
		}
	}
}

// The other three verbs the same way, each naming its own capability. They
// are here as a table because the failure this guards against is a verb that
// was gated and then quietly ungated in a refactor.
func TestEveryVerbThatNeedsACapabilityRefusesWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		capability string
		call       func(*Client) error
	}{
		{CapPaneRun, func(c *Client) error { return c.PaneRun(context.Background(), "wM:p9", "ls") }},
		{CapPaneRead, func(c *Client) error {
			_, err := c.PaneRead(context.Background(), "wM:p9", 10)
			return err
		}},
		{CapAgentGet, func(c *Client) error {
			_, err := c.AgentGet(context.Background(), "wM:p9")
			return err
		}},
		{CapTabCreate, func(c *Client) error {
			_, _, err := c.TabCreate(context.Background(), "", "/src/p", "l")
			return err
		}},
	} {
		t.Run(tc.capability, func(t *testing.T) {
			c, f := client(t)
			// Everything this binary needs EXCEPT the one under test.
			offered := []string{}
			for _, cap := range []string{CapTabCreate, CapPaneRun, CapPaneRead, CapAgentGet} {
				if cap != tc.capability {
					offered = append(offered, `"`+cap+`"`)
				}
			}
			f.Write(t, fake.HerdrSchemaFile, `{"requests":[`+strings.Join(offered, ",")+`]}`)
			f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"ok"}}'`)

			err := tc.call(c)
			if got := codes.Of(err); got != codes.Unsupported {
				t.Fatalf("%s = %v (%s), want UNSUPPORTED", tc.capability, err, got)
			}
			if !strings.Contains(err.Error(), tc.capability) {
				t.Errorf("the refusal does not name %s: %v", tc.capability, err)
			}
		})
	}
}

// A Herdr that could not be asked is not a Herdr that offers nothing. The
// answer is not cached, so the next verb asks again rather than refusing
// forever over one unreachable call.
func TestAnUnreadableSchemaIsNotRememberedAsAnAnswer(t *testing.T) {
	c, f := client(t)
	fake.NoHerdrSchema(t)
	f.Bin(t, "herdr", `echo "herdr: no server" >&2; exit 1`)
	ctx := context.Background()
	if _, err := c.Schema(ctx); err == nil {
		t.Fatal("an unreachable herdr answered a schema")
	}
	if c.Seen() != nil {
		t.Fatal("a failed read was cached as the answer")
	}
	f.Bin(t, "herdr", `if [ "$1 $2" = "api schema" ]; then echo '`+fake.DefaultHerdrSchema+`'; exit 0; fi
echo '{"id":"x","result":{"type":"ok"}}'`)
	if err := c.Require(ctx, CapPaneRun); err != nil {
		t.Errorf("the next call did not ask again: %v", err)
	}
}

// A document that lists no requests at all is not a Herdr with no
// capabilities; it is an answer this binary could not read. Accepting it
// would refuse every verb with UNSUPPORTED and blame Herdr for it.
func TestASchemaListingNoRequestsIsUnavailableRatherThanEmpty(t *testing.T) {
	c, f := client(t)
	f.Write(t, fake.HerdrSchemaFile, `{"protocol":1}`)
	_, err := c.Schema(context.Background())
	if got := codes.Of(err); got != codes.Unavailable {
		t.Fatalf("an empty schema = %v (%s), want UNAVAILABLE", err, got)
	}
}
