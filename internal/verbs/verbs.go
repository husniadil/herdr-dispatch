// Package verbs is the one registry both doors are built from. The CLI
// subcommands and the MCP tools are generated from this list rather than
// written twice, and a parity test enumerates both surfaces against it: a
// verb that exists on one door and not the other is a test failure, not
// something an operator discovers.
package verbs

// String and Bool are the kinds of argument a verb takes. Bool arrived with
// `parked resolve --reject`, which is a switch and not a value: rendering it
// as a string would have every door asking a caller to write the word "true".
const (
	String = "string"
	Bool   = "bool"
	// Int arrived with `events --limit`, which is a count. Rendering it as
	// a string would have every door asking a caller to quote a number and
	// the daemon parsing one back out of it.
	Int = "int"
)

// Arg is one parameter of a verb. A positional arg is a CLI positional and an
// ordinary named field over MCP and the socket.
type Arg struct {
	Name       string
	Type       string
	Desc       string
	Required   bool
	Positional bool
}

// Verb is one operation, in every surface it appears in.
type Verb struct {
	// Name is the verb on the socket, dotted for a namespaced verb:
	// parked.list, the way both siblings spell theirs.
	Name string
	// MCP is the tool name: the verb alone, with dots as underscores. The
	// door serves bare verbs, so a caller reads dispatch as herdr-dispatch's
	// dispatch rather than as a name that repeats the binary. It is a field
	// and not a transformation applied at the door, so an absence from the
	// agent surface is a decision written beside the verb.
	MCP string
	// CLI is the subcommand path, e.g. {"status"}.
	CLI []string
	// Short is the one-line help both doors show.
	Short string
	// Long is what the MCP tool description adds for a caller that cannot
	// ask a follow-up question.
	Long string
	// Args is the parameter list, in CLI positional order.
	Args []Arg
	// NoAutostart sends the verb to whatever daemon is already listening
	// and refuses when none is, instead of starting one.
	NoAutostart bool
	// Mutates says the verb changes the world, which is what §9.1 puts
	// behind the policy gate. A verb that only reads is neither gated nor
	// asked to explain itself.
	Mutates bool
	// Gated is the §9.4 verb name handed to the policy gate, `<short
	// name>.<verb>` with the short name §13.2 fixes. Empty means this verb
	// passes no name, which a Mutates verb must justify in Ungated.
	Gated string
	// Ungated is why a verb that writes passes no name to the policy gate.
	// Required exactly when Mutates is true and Gated is empty, so the
	// decision is written down beside the verb rather than inferred from
	// its absence.
	Ungated string
}

// GatedVerbs is the §9.4 list a policy plugin names, in registry order. The
// README carries the same list, and a test reads one against the other.
func GatedVerbs() []string {
	out := []string{}
	for _, v := range All {
		if v.Gated != "" {
			out = append(out, v.Gated)
		}
	}
	return out
}

// All is the registry. Order is the order the CLI lists them in.
var All = []Verb{
	{
		Name: "doctor", MCP: "doctor", CLI: []string{"doctor"},
		Short: "Report whether the dispatcher can work at all",
		Long: "Answers with the daemon's version, its base pane, the board's " +
			"reachability and herdr's. Run it first when a dispatch refuses.",
	},
	{
		Name: "dispatch", MCP: "dispatch", CLI: []string{"dispatch"},
		Short: "Bring a worker up for one ready task, now",
		Long: "Reserves the task for the next tick and returns at once: bringing " +
			"a worker up takes minutes, and this call does not wait for it. Read " +
			"the outcome with status. Every refusal carries a contract code and " +
			"opens the message with the sub-reason it refused for: USAGE when no " +
			"task was named; CONFLICT as NOT_READY when the board will not hand " +
			"the task out, as AT_CAPACITY when the fleet is full, or as " +
			"ALREADY_DISPATCHED when this daemon is already driving it; " +
			"UNSUPPORTED as NO_BASE_PANE when the daemon has no pane to split a " +
			"worker off; NOT_FOUND when no board has the task; and UNAVAILABLE " +
			"when the board itself could not be read.",
		Args: []Arg{
			{Name: "task", Type: String, Desc: "The task id or its number on the board", Required: true, Positional: true},
		},
		Mutates: true,
		Gated:   "dispatch.dispatch",
	},
	{
		Name: "stop", MCP: "stop", CLI: []string{"stop"},
		Short: "Ask the running daemon to shut down",
		Long: "The daemon stops ticking, closes its socket, drops its lock and " +
			"exits; it writes nothing to the board on the way out, and the tasks " +
			"it was driving keep their claims and their leases, which are the " +
			"board's to time out. Answers CONFLICT as NOT_RUNNING when no daemon is " +
			"listening: nothing is started just to be stopped. It is a brake on " +
			"the WHOLE dispatcher and not on one task: every worker it is " +
			"driving keeps running in its pane, and no new one comes up until a " +
			"daemon is started again. Confirm with the operator before calling " +
			"it, the same way you would before any act whose blast radius is " +
			"everyone else's work.",
		NoAutostart: true,
		Mutates:     true,
		Gated:       "dispatch.stop",
	},
	{
		Name: "status", MCP: "status", CLI: []string{"status"},
		Short: "List what the dispatcher is driving",
		Long: "One row per binding: the task, the pane its worker lives in, when " +
			"the goal was delivered, how often, whether review was announced, " +
			"the worker's agent_status as herdr reports it, and the branch its " +
			"commits are on — marked behind when the project's HEAD has moved " +
			"past that branch, which is what makes a fast-forward merge refuse.",
	},
	{
		Name: "dump", MCP: "dump", CLI: []string{"dump"},
		Short: "Print the whole store as JSON",
		Long: "Everything this daemon remembers across restarts, in one " +
			"document (§5.8): the bindings, the reservations no tick has " +
			"spawned for yet, and the actions the policy gate parked. It is " +
			"the daemon's own live set rather than a re-read of the file, so " +
			"it is what the next save will write. Nothing here is a board " +
			"fact — task state, claims and leases are htask's and are read " +
			"from htask.",
	},
	{
		Name: "events", MCP: "events", CLI: []string{"events"},
		Short: "Read the append-only trail of what the dispatcher did",
		Long: "The §8.1 events this dispatcher owns and nothing else records: a task " +
			"reserved or given back, a worker spawned, adopted, prompted, retired or " +
			"gone, review announced, and the policy gate parking or releasing a call. " +
			"Board facts are NOT here — a task claimed, submitted or approved is on " +
			"htask's own trail. Without since this reads from the BEGINNING of what " +
			"the daemon still holds, oldest first, so a consumer that resumes passes " +
			"since with the last event id it saw, or a Unix-millisecond timestamp, or " +
			"it reads everything again. The trail is bounded: an id that has rotated " +
			"out of it is refused rather than answered with the whole window. On the " +
			"CLI --follow turns this into a subscription that keeps printing; a tool " +
			"call answers once and has no follow, because a stream is not a tool call.",
		Args: []Arg{
			{Name: "since", Type: String, Desc: "An event id, or a Unix-millisecond timestamp, to resume after"},
			{Name: "limit", Type: Int, Desc: "Stop after this many events"},
		},
	},
	{
		Name: "parked.list", MCP: "parked_list", CLI: []string{"parked", "list"},
		Short: "List the actions the policy gate deferred to the operator",
		Long: "A gate that answers defer parks the call instead of performing " +
			"it (§9.3) and refuses with DENIED carrying the parked_id. This is " +
			"where those rows are read: who asked, which gated verb, what " +
			"target, the reason the gate gave, and whether the action is still " +
			"waiting or was resolved and then failed. A failed row is not " +
			"finished business — the operator decided and the verb did not run.",
	},
	{
		Name: "parked.resolve", MCP: "parked_resolve", CLI: []string{"parked", "resolve"},
		Short: "Let a parked action through, or reject it",
		Long: "Re-runs the parked verb under the subject the gate stopped, never " +
			"the resolver's (§9.3), and skips the gate, because the resolution " +
			"IS the decision the gate deferred. The row records who resolved " +
			"it. With --reject the verb never runs and the row is closed. This " +
			"is the operator's authority and therefore advice rather than a " +
			"refusal this door makes (§3.7): confirm with the user before " +
			"resolving one on their behalf.",
		Args: []Arg{
			{Name: "id", Type: String, Desc: "The parked action id, as DENIED reported it", Required: true, Positional: true},
			// Spelled `reject`, as the sibling plugins spell the same
			// operator verdict. It rules on a PARKED ACTION of this
			// daemon's, never on a board submission — no task moves and
			// the board is not called — so the guard that forbids the
			// board's review words as arguments,
			// TestNoSourceFilePassesAReviewVerbAsAnArgument, carries a
			// named exemption for this one switch rather than pushing a
			// caller to remember a spelling only this plugin uses.
			{Name: "reject", Type: Bool, Desc: "Close the action without running the verb"},
		},
		Mutates: true,
		Ungated: "resolving a deferral is the answer to a gate that already spoke; gating it would let a gate park its own resolution and strand every deferred action",
	},
}

// ByName finds the verb a socket request names.
func ByName(name string) (Verb, bool) {
	for _, v := range All {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}

// ByCLI finds the verb a subcommand path names.
func ByCLI(path []string) (Verb, bool) {
	for _, v := range All {
		if len(v.CLI) != len(path) {
			continue
		}
		same := true
		for i := range v.CLI {
			if v.CLI[i] != path[i] {
				same = false
				break
			}
		}
		if same {
			return v, true
		}
	}
	return Verb{}, false
}

// Help is the description a caller reads on either door: the one-line summary
// and the paragraph a caller who cannot ask a follow-up question needs. The
// MCP tool description and `hdis <verb> --help` are the same words, because a
// verb explained two ways is a verb that drifts.
func (v Verb) Help() string {
	if v.Long == "" {
		return v.Short
	}
	return v.Short + ". " + v.Long
}
