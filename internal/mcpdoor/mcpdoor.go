// Package mcpdoor serves the verb table over stdio MCP. It is the second
// thin door over the same daemon calls the CLI makes, and it holds nothing:
// a door is spawned once per client session, so a binding kept here would be
// one of several disagreeing sets and the follow-through would run once per
// session. Every tool builds the same protocol.Request the CLI builds and
// hands back the same JSON the CLI prints with --json.
package mcpdoor

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/client"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// ServerName is how this server registers itself: the repository an operator
// wiring it into a client sees. It is not the tool prefix, which is the
// binary's short name and is pinned by the parity test.
const ServerName = "herdr-dispatch"

// Title is the display name.
const Title = "Herdr Dispatch"

// Door names this surface in the daemon's log.
const Door = "mcp"

// Instructions is what a caller reads before it picks a tool.
const Instructions = "herdr-dispatch drives the htask board's ready work: it brings a worker agent " +
	"up in a Herdr pane for a task, delivers the task's goal, tracks the worker, and stops at " +
	"review, where the board's own review gate takes over. `pane`, `agent` and `agent_status` mean " +
	"what Herdr says they mean. dispatch reserves one ready task and returns at once — bringing " +
	"a worker up runs to minutes, so read the outcome with status rather than waiting on the " +
	"call. The worker claims the task itself; nothing here claims, approves or rejects on its " +
	"behalf. Which agent kind, model and effort a worker launches with is this binary's config and " +
	"is not selectable per call. The board row's priority selects WHICH configured profile a " +
	"worker launches with. doctor says why a dispatch would refuse."

// Caller is what the door needs to reach the daemon. The default dials the
// real socket, starting a daemon when none answers; a test swaps in something
// that answers in process.
type Caller func(protocol.Request) (json.RawMessage, error)

// Options is what the door was STARTED with, which under the process-bound
// identity rule (§3.2) is the only place a door's identity may come from. It
// is not a general-purpose bag: everything else a door needs, it derives per
// call.
type Options struct {
	// Operator is §7.5's declaration: this door speaks for the operator.
	// Read once from `hdis mcp --operator` and never from a tool call,
	// which is what keeps it from being --as with a different spelling.
	Operator bool
}

// New builds the MCP server with one tool per verb.
func New(version string, call Caller, opt Options) *mcp.Server {
	if call == nil {
		call = (&client.Client{}).Call
	}
	s := mcp.NewServer(&mcp.Implementation{
		Name:        ServerName,
		Title:       Title,
		Version:     version,
		Description: "Dispatch ready tasks from the htask board to worker agents in Herdr panes",
	}, &mcp.ServerOptions{Instructions: Instructions})
	for _, v := range verbs.All {
		s.AddTool(tool(v), handlerFor(v, call, opt))
	}
	return s
}

// Serve runs the door on stdio until the client disconnects.
//
// The refusal below is §7.5's second half, and it is defence in depth rather
// than the guarantee: protocol.Request.Caller resolves the pane BEFORE it
// reads the declaration, so a declared door inside a pane is that pane's
// agent on every call whether this returns or not. What this buys is failing
// loudly once, instead of running a door whose two answers about who it is
// disagree for as long as it is up.
func Serve(ctx context.Context, version string, call Caller, opt Options) error {
	if pane := os.Getenv("HERDR_PANE_ID"); opt.Operator && pane != "" {
		return codes.Errorf(codes.Forbidden,
			"--operator declares this door speaks for the operator, but it is starting inside "+
				"Herdr pane %s, which makes it that pane's agent (§3.2); the declaration is for "+
				"a door in no pane (§7.5)", pane)
	}
	return New(version, call, opt).Run(ctx, &mcp.StdioTransport{})
}

// tool renders one verb as an MCP tool. The schema is built from the same
// Args the CLI builds its positionals from, which is what parity means here:
// same name, same arguments, same result.
func tool(v verbs.Verb) *mcp.Tool {
	props := map[string]any{}
	var required []string
	for _, a := range v.Args {
		kind := "string"
		switch a.Type {
		case verbs.Bool:
			kind = "boolean"
		case verbs.Int:
			kind = "integer"
		}
		props[a.Name] = map[string]any{"type": kind, "description": a.Desc}
		if a.Required {
			required = append(required, a.Name)
		}
	}
	// The scope pair, mirrored from the CLI's global flags so that parity is
	// about the whole surface and not only the per-verb half (§4.2). --json
	// and --as are deliberately absent: a tool call already answers with a
	// structured document, and §3.2 derives a principal from the calling
	// process rather than reading one off a call. §7.5's --operator is that
	// exclusion's counterpart rather than a second spelling of it: it is
	// read from `hdis mcp --operator` and so is absent here too.
	props[argProject] = map[string]any{"type": "string",
		"description": "The board to act on; defaults to every board (§4.2)"}
	props[argAllProjects] = map[string]any{"type": "boolean",
		"description": "Act across every board, which is this daemon's default"}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	description := v.Help()
	return &mcp.Tool{Name: v.MCP, Description: description, InputSchema: schema}
}

// handlerFor turns a tool call into the same daemon call the CLI makes.
func handlerFor(v verbs.Verb, call Caller, opt Options) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return failure(codes.Refusef(codes.Invalid, "unreadable arguments: %v", err)), nil
			}
		}
		if err := check(v, args); err != nil {
			return failure(err), nil
		}
		// The scope is the door's to resolve and never the verb's, so it
		// leaves Args before the daemon sees them: an argument no verb
		// declares would be refused there.
		named, _ := args[argProject].(string)
		everyBoard, _ := args[argAllProjects].(bool)
		delete(args, argProject)
		delete(args, argAllProjects)
		project, allProjects, err := cli.Scope(named, everyBoard)
		if err != nil {
			return failure(err), nil
		}
		raw, err := call(protocol.Request{
			Verb: v.Name,
			Args: args,
			// Resolved here, in the door: a relative path is the CALLER's,
			// and this process is the only one that knows what it was
			// relative to (§4.1).
			Project:     project,
			AllProjects: allProjects,
			// The pane this door was spawned in, if it was spawned in one. A
			// caller on another harness has none; the daemon records what it
			// is given and grants nothing for it.
			Pane: os.Getenv("HERDR_PANE_ID"),
			// From the door's own startup, never from args: §7.5 says the
			// declaration may not arrive per call, and this is the line
			// that makes that true rather than intended.
			Operator: opt.Operator,
			Door:     Door,
		})
		if err != nil {
			return failure(err), nil
		}
		var structured any
		if err := json.Unmarshal(raw, &structured); err != nil {
			structured = nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
			StructuredContent: structured,
		}, nil
	}
}

// check holds the door to the schema it published. The go-sdk validates
// arguments only for tools registered through its generic AddTool, which
// wants a Go type per tool; this door is built from a table, so the check
// lives here and walks the same Args the schema was rendered from.
func check(v verbs.Verb, args map[string]any) error {
	declared := make(map[string]verbs.Arg, len(v.Args))
	for _, a := range v.Args {
		declared[a.Name] = a
	}
	// The two the door injects into every schema rather than the registry
	// declaring them, held to the types the schema published.
	for name, want := range map[string]string{argProject: verbs.String, argAllProjects: verbs.Bool} {
		raw, ok := args[name]
		if !ok || raw == nil {
			continue
		}
		if err := daemon.CheckArg(v, verbs.Arg{Name: name, Type: want}, raw); err != nil {
			return err
		}
	}
	for name := range args {
		if _, ok := declared[name]; !ok {
			if name == argProject || name == argAllProjects {
				continue
			}
			// §7.5: the declaration is read from how the door was STARTED,
			// so a call carrying it is refused BY name rather than falling
			// through to "takes no argument named" — a caller that wrote it
			// meant something this door will never do, and should be told
			// what to do instead.
			if name == argOperator {
				return codes.Refusef(codes.Invalid,
					"%q is not an argument: a door speaks for the operator because of how it was "+
						"started, never because a call says so (§7.5). Start the server with "+
						"`hdis mcp --operator` instead", argOperator)
			}
			return codes.Refusef(codes.Invalid, "%s takes no argument named %q", v.Name, name)
		}
	}
	for _, a := range v.Args {
		raw, ok := args[a.Name]
		if !ok || raw == nil {
			if a.Required {
				return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
			}
			continue
		}
		if err := daemon.CheckArg(v, a, raw); err != nil {
			return err
		}
	}
	return nil
}

// failure is a refusal as a tool error carrying the daemon's own code, never
// as a JSON-RPC protocol error: the caller asked a fair question and gets a
// named answer.
func failure(err error) *mcp.CallToolResult {
	code, message := codes.Of(err), err.Error()
	var named *codes.Error
	if errors.As(err, &named) {
		message = named.Message
	}
	envelope := map[string]string{"code": string(code), "message": message}
	// §9.3: a DENIED the gate deferred names the row the operator resolves.
	// A caller told only that it was denied has nothing to point at.
	if id := codes.ParkedOf(err); id != "" {
		envelope["parked_id"] = id
	}
	body, _ := json.Marshal(map[string]any{"error": envelope})
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}

// The scope arguments every tool takes, mirroring the CLI's global flags.
// They are constants because tool() publishes them and check() enforces them,
// and a typo in one place would make the door promise what it does not keep.
const (
	argProject     = "project"
	argAllProjects = "all_projects"
)

// argOperator is not an argument, and never becomes one (§7.5). It is named
// here so that check can refuse it BY name, because a reserved word nothing
// spells out is one a later edit spells differently.
const argOperator = "operator"
