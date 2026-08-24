// Package protocol is what travels on the daemon's socket: one request in,
// one response out, both a single JSON document. Both doors build the same
// Request, and the daemon answers both with the same Response.
package protocol

import "encoding/json"

// Request is one verb call.
type Request struct {
	// Verb is a name from the verb table.
	Verb string `json:"verb"`
	// Args are the verb's declared arguments, by name.
	Args map[string]any `json:"args,omitempty"`
	// Pane is the Herdr pane the caller runs in, empty for a caller outside
	// one. The daemon records it and grants nothing for it: every caller
	// here is the operator's own tooling, reaching a socket only the
	// operator can open, and the board only ever hears from this binary as
	// plugin:hdis whoever asked.
	Pane string `json:"pane,omitempty"`
	// Door is the surface the call came in through, "cli" or "mcp", for the
	// daemon's log.
	Door string `json:"door,omitempty"`
	// Follow turns `events` into a subscription (§8.2): the daemon keeps
	// the connection and writes one Response per event until the caller
	// goes away. It is a property of the CONNECTION rather than an argument
	// of the verb, which is why it is here and not in the verb's Args: a
	// tool call answers once, so the MCP door has nothing to set it with.
	Follow bool `json:"follow,omitempty"`
}

// Response is the one answer. Exactly one of Result and Error is set.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Failure        `json:"error,omitempty"`
	// Done ends a stream on purpose. Without it a daemon that finished and
	// a daemon that was killed are the same closed socket, and a follower
	// cannot tell "there is no more" from "I stopped being told".
	Done bool `json:"done,omitempty"`
}

// Failure is a refusal with a name on it. ParkedID is the §9.3 addition a
// DENIED carries when the policy gate deferred the call rather than refusing
// it: the row the operator resolves, and the only handle the caller has on it.
type Failure struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	ParkedID string `json:"parked_id,omitempty"`
}

// Caller names who asked, for the daemon's log. It is a record, never a
// permission.
func (r Request) Caller() string {
	if r.Pane == "" {
		return "unknown"
	}
	return "agent:" + r.Pane
}
