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
	"is not selectable per call. doctor says why a dispatch would refuse."

// Caller is what the door needs to reach the daemon. The default dials the
// real socket, starting a daemon when none answers; a test swaps in something
// that answers in process.
type Caller func(protocol.Request) (json.RawMessage, error)

// New builds the MCP server with one tool per verb.
func New(version string, call Caller) *mcp.Server {
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
		s.AddTool(tool(v), handlerFor(v, call))
	}
	return s
}

// Serve runs the door on stdio until the client disconnects.
func Serve(ctx context.Context, version string, call Caller) error {
	return New(version, call).Run(ctx, &mcp.StdioTransport{})
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
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	description := v.Short
	if v.Long != "" {
		description += ". " + v.Long
	}
	return &mcp.Tool{Name: v.Name, Description: description, InputSchema: schema}
}

// handlerFor turns a tool call into the same daemon call the CLI makes.
func handlerFor(v verbs.Verb, call Caller) mcp.ToolHandler {
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
		raw, err := call(protocol.Request{
			Verb: v.Name,
			Args: args,
			// The pane this door was spawned in, if it was spawned in one. A
			// caller on another harness has none; the daemon records what it
			// is given and grants nothing for it.
			Pane: os.Getenv("HERDR_PANE_ID"),
			Door: Door,
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
	for name := range args {
		if _, ok := declared[name]; !ok {
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
