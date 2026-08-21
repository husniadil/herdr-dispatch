// Package verbs is the one registry both doors are built from. The CLI
// subcommands and the MCP tools are generated from this list rather than
// written twice, and a parity test enumerates both surfaces against it: a
// verb that exists on one door and not the other is a test failure, not
// something an operator discovers.
package verbs

// ToolPrefix is the MCP tool-name prefix: the binary's short name, which is
// also the socket's and the state dir's.
const ToolPrefix = "hdis"

// String is the only kind of argument the first slice has. A second kind
// arrives with the first verb that needs one, and not before.
const String = "string"

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
	// Name is the verb on the socket, and the suffix of the MCP tool.
	Name string
	// CLI is the subcommand path, e.g. {"status"}.
	CLI []string
	// MCP is the tool name, always ToolPrefix + "_" + Name.
	MCP string
	// Short is the one-line help both doors show.
	Short string
	// Long is what the MCP tool description adds for a caller that cannot
	// ask a follow-up question.
	Long string
	// Args is the parameter list, in CLI positional order.
	Args []Arg
	// CLIOnly keeps a verb off the MCP door. It is the one asymmetry the
	// table may declare, and declaring it here is what keeps it from being
	// an accident: the parity guard reads this field rather than a list of
	// exceptions kept beside it.
	CLIOnly bool
	// NoAutostart sends the verb to whatever daemon is already listening
	// and refuses when none is, instead of starting one.
	NoAutostart bool
}

// All is the registry. Order is the order the CLI lists them in.
var All = []Verb{
	{
		Name: "doctor", CLI: []string{"doctor"}, MCP: "hdis_doctor",
		Short: "Report whether the dispatcher can work at all",
		Long: "Answers with the daemon's version, its base pane, the board's " +
			"reachability and herdr's. Run it first when a dispatch refuses.",
	},
	{
		Name: "dispatch", CLI: []string{"dispatch"}, MCP: "hdis_dispatch",
		Short: "Bring a worker up for one ready task, now",
		Long: "Reserves the task for the next tick and returns at once: bringing " +
			"a worker up takes minutes, and this call does not wait for it. Read " +
			"the outcome with status. Refuses with NOT_READY when the board will " +
			"not hand the task out, AT_CAPACITY when the fleet is full, and " +
			"NO_BASE_PANE when the daemon has no pane to split a worker off.",
		Args: []Arg{
			{Name: "task", Type: String, Desc: "The task id or its number on the board", Required: true, Positional: true},
		},
	},
	{
		Name: "stop", CLI: []string{"stop"}, MCP: "hdis_stop",
		Short: "Ask the running daemon to shut down",
		Long: "The daemon stops ticking, closes its socket, drops its lock and " +
			"exits; it writes nothing to the board on the way out, and the tasks " +
			"it was driving keep their claims and their leases, which are the " +
			"board's to time out. Answers NOT_RUNNING when no daemon is " +
			"listening: nothing is started just to be stopped.",
		CLIOnly: true, NoAutostart: true,
	},
	{
		Name: "status", CLI: []string{"status"}, MCP: "hdis_status",
		Short: "List what the dispatcher is driving",
		Long: "One row per binding: the task, the pane its worker lives in, when " +
			"the goal was delivered, how often, whether review was announced, and " +
			"the worker's agent_status as herdr reports it.",
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
