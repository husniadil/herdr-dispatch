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
	// Project is the §4.1 canonical project the caller named with
	// --project, already resolved by the door: a relative path is relative
	// to the CALLER's working directory and the daemon's is somewhere else.
	// Empty means the caller named no single board.
	Project string `json:"project,omitempty"`
	// AllProjects is the dispatcher's own default (§4.4 note in
	// docs/contract-notes.md): one daemon per user drives every board, so a
	// call that names no project reads them all. It travels rather than
	// being inferred from an empty Project, so a door that grows a
	// different default cannot change what an old call meant.
	AllProjects bool `json:"all_projects,omitempty"`
	// As is the §3.2 declared principal, `cron:`, `trigger:` or `plugin:`.
	// The door has already refused every other kind: agent and human are
	// DERIVED, and a call that could declare one would be declaring the
	// fact the rule exists to keep underived.
	As string `json:"as,omitempty"`
	// Operator is the door saying that the PROCESS it runs in was started
	// by a deliberate human act: `hdis mcp --operator` (§7.5). It is read
	// once from the door's own startup and never from a call, which is what
	// keeps it from being `--as human` with a different spelling — the one
	// principal §3.2 will not let a call declare. The pane is resolved
	// first, so a declared door inside a pane is still that pane's agent.
	Operator bool `json:"operator,omitempty"`
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

// Caller names who asked: the §9 gate's subject and the resolver a parked row
// records. It is a record, never a permission.
//
// The order is §3.2's, with §7.5's declaration last of the three: what the
// call declared with --as, else the pane it runs in, else the operator when
// the DOOR was started with `hdis mcp --operator`, else the literal `none`
// §3.7 spells for a caller with none of the three. The pane is read before
// the declaration on purpose (§7.5): a door started inside a pane is that
// pane's agent whatever it was declared, so declaring one gains an agent
// nothing.
func (r Request) Caller() string {
	// §3.2: the declared principal is the one exception to derivation, and
	// it wins over the pane, because a cron job firing from inside a pane is
	// still the cron job acting and not the agent sitting there.
	if r.As != "" {
		return r.As
	}
	if r.Pane != "" {
		return "agent:" + r.Pane
	}
	if r.Operator {
		return "human"
	}
	// §3.7: a door with neither a pane nor the declaration has NO principal,
	// and the contract spells that `none` — one word the four plugins share,
	// so an operator reading a gate script, a parked row or a doctor line at
	// any of them reads the same one.
	return "none"
}
