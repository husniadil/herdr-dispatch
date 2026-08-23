package herdr

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/husniadil/herdr-dispatch/internal/codes"
)

// The Herdr requests this binary needs, by the name the schema lists them
// under. They are constants rather than literals at the call site because
// §11.2 makes a missing one an UNSUPPORTED that NAMES the capability, and a
// name typed twice is a name that can be misspelled once.
const (
	CapTabCreate  = "tab.create"
	CapTabList    = "tab.list"
	CapTabClose   = "tab.close"
	CapPaneSplit  = "pane.split"
	CapPaneRun    = "pane.run"
	CapPaneRead   = "pane.read"
	CapPaneList   = "pane.list"
	CapPaneClose  = "pane.close"
	CapAgentStart = "agent.start"
	CapAgentGet   = "agent.get"
	CapAgentList  = "agent.list"
	CapPrompt     = "agent.prompt"
)

// Schema is what `herdr api schema --json` says this Herdr can do (§11.2).
// Requests are the request methods (`agent.get`, `pane.run`) and Events the
// event kinds Herdr publishes. The protocol number is read for doctor output
// only: §11.2 forbids deciding anything on it, and pinning one is a contract
// violation.
type Schema struct {
	Protocol int      `json:"protocol"`
	Requests []string `json:"requests"`
	Events   []string `json:"events"`
}

// rawSchema is the document Herdr actually prints: a JSON Schema whose
// `request` branch is a oneOf over method constants and whose `event` branch
// carries an EventKind enum. Feature detection reads those two lists.
type rawSchema struct {
	Protocol int `json:"protocol"`
	Schemas  struct {
		Request struct {
			OneOf []struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
				} `json:"properties"`
			} `json:"oneOf"`
		} `json:"request"`
		Event struct {
			Defs struct {
				EventKind struct {
					Enum []string `json:"enum"`
				} `json:"EventKind"`
			} `json:"$defs"`
		} `json:"event"`
	} `json:"schemas"`
	// Requests and Events are the flat form §11.2 also requires a reader to
	// accept. Nothing publishes it today; it is read so a future Herdr that
	// simplifies the document still works here, which is what "feature-detect,
	// never pin" asks for.
	Requests []string `json:"requests"`
	Events   []string `json:"events"`
}

func (r rawSchema) toSchema() Schema {
	s := Schema{Protocol: r.Protocol, Requests: r.Requests, Events: r.Events}
	for _, branch := range r.Schemas.Request.OneOf {
		if m := branch.Properties.Method.Const; m != "" {
			s.Requests = append(s.Requests, m)
		}
	}
	s.Events = append(s.Events, r.Schemas.Event.Defs.EventKind.Enum...)
	return s
}

// Has reports whether Herdr listed a request or an event. Herdr names its
// event kinds with underscores (`pane_exited`) where the contract writes dots
// (`pane.exited`), so both spellings match.
func (s *Schema) Has(capability string) bool {
	if s == nil {
		return false
	}
	alt := strings.ReplaceAll(capability, ".", "_")
	for _, list := range [][]string{s.Requests, s.Events} {
		for _, got := range list {
			if got == capability || got == alt {
				return true
			}
		}
	}
	return false
}

// Schema reads `herdr api schema --json` once and caches it: §11.2 says read
// it at daemon start and decide, not poll. A Herdr that cannot be asked is
// reported rather than assumed either way — the answer is not cached, so the
// next verb asks again instead of refusing forever over one unreachable call.
func (c *Client) Schema(ctx context.Context) (*Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schema != nil {
		return c.schema, nil
	}
	out, err := c.text(ctx, "api", "schema", "--json")
	if err != nil {
		return nil, err
	}
	var raw rawSchema
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "herdr api schema: unreadable answer: %v", err)
	}
	s := raw.toSchema()
	if len(s.Requests) == 0 {
		return nil, codes.Errorf(codes.Unavailable, "herdr api schema listed no requests")
	}
	c.schema = &s
	return c.schema, nil
}

// Seen is the schema this client already read, or nil. It is for doctor,
// which reports what feature detection found and must not go and run a
// command of its own to answer.
func (c *Client) Seen() *Schema {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schema
}

// Require is the §11.2 gate at the verb: a missing capability is UNSUPPORTED
// with the capability named, never a refusal to start.
func (c *Client) Require(ctx context.Context, capability string) error {
	s, err := c.Schema(ctx)
	if err != nil {
		return err
	}
	if !s.Has(capability) {
		return codes.Errorf(codes.Unsupported,
			"this Herdr does not offer %s, which this verb needs", capability)
	}
	return nil
}
