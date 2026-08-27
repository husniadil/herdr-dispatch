package mcpdoor

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
	"github.com/husniadil/herdr-dispatch/internal/worktree"
)

// pinnedTools is the tool list this door publishes. Adding, renaming or
// removing one is a deliberate change to a surface other harnesses call:
// it moves this list in the same commit, and it is a breaking change.
var pinnedTools = []string{
	"doctor",
	"dispatch",
	"status",
	"stop",
	"dump",
	"events",
	"parked_list",
	"parked_resolve",
}

// inProcessDaemon is a real daemon over a fake board and a fake herdr. No
// socket: both doors are tested against the same Handle the socket serves.
func inProcessDaemon(t *testing.T) (*daemon.Daemon, Caller) {
	t.Helper()
	f := testenv.New(t)
	f.Bin(t, "htask", `case "$1" in
"list") echo '{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}],"count":1}' ;;
"get") echo '{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo"}}' ;;
*) echo '{"version":"0.4.0","contract":"0.3","binary":"/bin/htask","socket_live":true,"herdr_reachable":true}' ;;
esac`)
	f.Bin(t, "herdr", `echo '{"id":"x","result":{"type":"pane_list","panes":[]}}'`)
	t.Setenv("HERDR_PANE_ID", "wM:p1")

	cfg, err := config.Parse([]byte(`default = "worker"
[profiles.worker]
provider = "claude"
`))
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon.Daemon{
		Loop: &loop.Loop{
			Board:  &htask.Client{},
			Herdr:  &herdrclient.Client{},
			Config: cfg,
			Policy: decide.Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2},
			Spawn: &spawn.Pipeline{
				Herdr: &herdrclient.Client{}, Proxy: &proxy.Client{},
				StartTimeout: time.Second, Poll: time.Second, Sleep: func(time.Duration) {},
			},
			BasePane: "wM:p1",
			Log:      log.New(io.Discard, "", 0),
		},
		Board:    &htask.Client{},
		Interval: time.Hour,
		Version:  "0.1.0",
		Log:      log.New(io.Discard, "", 0),
	}
	return d, func(req protocol.Request) (json.RawMessage, error) {
		return d.Handle(context.Background(), req)
	}
}

// session connects an in-memory MCP client to a door nobody declared, which
// is what an operator wiring `hdis mcp` into a client gets by default.
func session(t *testing.T, call Caller) *mcp.ClientSession {
	t.Helper()
	return sessionWith(t, call, Options{})
}

// sessionWith is the same, for a door started with the §7.5 declaration.
func sessionWith(t *testing.T, call Caller, opt Options) *mcp.ClientSession {
	t.Helper()
	srv := New("0.1.0", call, opt)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "parity-test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func text(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// The list a caller on another harness binds to. It moves only on purpose.
func TestTheServedToolListIsPinned(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var got []string
	for _, tl := range tools.Tools {
		got = append(got, tl.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), pinnedTools...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the served tool list moved.\n got: %v\nwant: %v\nIf this is intended, move pinnedTools in the same commit.", got, want)
	}
}

// The parity guard: every verb reaches both doors, under the same name, and
// neither door carries one the other does not.
func TestNeitherDoorCarriesAVerbTheOtherLacks(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	served := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		served[tl.Name] = tl
	}
	for _, v := range verbs.All {
		if _, ok := verbs.ByCLI(v.CLI); !ok {
			t.Errorf("verb %q has no CLI subcommand", v.Name)
		}
		tl, ok := served[v.MCP]
		if !ok {
			t.Errorf("verb %q is a CLI subcommand and no MCP tool", v.Name)
			continue
		}
		if tl.Name != v.MCP {
			t.Errorf("tool %q is served for verb %q: the tool name is the one the verb declares, %q", tl.Name, v.Name, v.MCP)
		}
		delete(served, v.MCP)
	}
	for name := range served {
		t.Errorf("tool %q is served and is no verb in the table", name)
	}
}

// Same arguments on both doors: the schema is rendered from the same Args the
// CLI reads its positionals from, and nothing else may appear in it.
func TestTheSchemaDeclaresExactlyWhatTheCLITakes(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	for _, v := range verbs.All {
		raw, err := json.Marshal(byName[v.MCP].InputSchema)
		if err != nil {
			t.Fatalf("schema for %q: %v", v.Name, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("schema for %q: %v", v.Name, err)
		}
		// The two scope arguments are injected into every tool rather than
		// declared by a verb: they are the CLI's --project and
		// --all-projects, and TestEveryToolTakesTheScopeArguments is what
		// holds them. Everything else in the schema comes from the registry.
		declared := len(schema.Properties)
		for _, name := range []string{argProject, argAllProjects} {
			if _, ok := schema.Properties[name]; ok {
				declared--
			}
		}
		if declared != len(v.Args) {
			t.Errorf("tool %q declares %d arguments and the CLI takes %d", v.Name, declared, len(v.Args))
		}
		for _, a := range v.Args {
			if _, ok := schema.Properties[a.Name]; !ok {
				t.Errorf("tool %q is missing the %q argument the CLI takes", v.Name, a.Name)
			}
			required := false
			for _, name := range schema.Required {
				required = required || name == a.Name
			}
			if a.Required != required {
				t.Errorf("tool %q requires %q: %t, and the CLI requires it: %t", v.Name, a.Name, required, a.Required)
			}
		}
	}
}

// Both doors build the same request out of the same call. This is what makes
// one verb table a guarantee rather than a convention.
func TestBothDoorsBuildTheSameRequest(t *testing.T) {
	cases := []struct {
		verb string
		argv []string
		args map[string]any
	}{
		{"doctor", nil, map[string]any{}},
		{"status", nil, map[string]any{}},
		{"dispatch", []string{"7"}, map[string]any{"task": "7"}},
		// The switch is spelled the same on both doors, and it is the
		// board's own word: a parked action is refused with --reject
		// here because htask and hmail spell the same operator verdict
		// that way, and a caller should not have to remember which
		// plugin calls it what.
		{"parked.resolve", []string{"--reject", "pk-1"}, map[string]any{"id": "pk-1", "reject": true}},
	}
	for _, tc := range cases {
		v, ok := verbs.ByName(tc.verb)
		if !ok {
			t.Fatalf("no verb named %q", tc.verb)
		}

		fromCLI, _, err := cli.Request(v, tc.argv)
		if err != nil {
			t.Fatalf("cli %s: %v", tc.verb, err)
		}

		var fromMCP protocol.Request
		catch := func(req protocol.Request) (json.RawMessage, error) {
			fromMCP = req
			return json.RawMessage(`{}`), nil
		}
		sess := session(t, catch)
		if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: v.MCP, Arguments: tc.args}); err != nil {
			t.Fatalf("mcp %s: %v", tc.verb, err)
		}

		if fromCLI.Verb != fromMCP.Verb {
			t.Errorf("%s: the cli asks for %q and the mcp door for %q", tc.verb, fromCLI.Verb, fromMCP.Verb)
		}
		cliArgs, _ := json.Marshal(fromCLI.Args)
		mcpArgs, _ := json.Marshal(fromMCP.Args)
		if string(cliArgs) != string(mcpArgs) {
			t.Errorf("%s: the cli sends %s and the mcp door %s", tc.verb, cliArgs, mcpArgs)
		}
		if fromCLI.Project != fromMCP.Project || fromCLI.AllProjects != fromMCP.AllProjects {
			t.Errorf("%s: the cli scopes to %q/all=%t and the mcp door to %q/all=%t",
				tc.verb, fromCLI.Project, fromCLI.AllProjects, fromMCP.Project, fromMCP.AllProjects)
		}
		if fromCLI.Pane != fromMCP.Pane {
			t.Errorf("%s: the doors derive different panes, %q and %q", tc.verb, fromCLI.Pane, fromMCP.Pane)
		}
		if fromCLI.Door != cli.Door || fromMCP.Door != Door {
			t.Errorf("%s: the doors do not name themselves: %q and %q", tc.verb, fromCLI.Door, fromMCP.Door)
		}
	}
}

// The same document reaches both callers: what --json prints is what the tool
// hands back, byte for byte.
func TestBothDoorsHandBackTheSameDocument(t *testing.T) {
	_, call := inProcessDaemon(t)

	fromCLI, err := call(protocol.Request{Verb: "status", Args: map[string]any{}})
	if err != nil {
		t.Fatalf("cli status: %v", err)
	}
	var printed strings.Builder
	if err := cli.Write("status", fromCLI, true, &printed); err != nil {
		t.Fatalf("cli render: %v", err)
	}

	sess := session(t, call)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatalf("mcp status: %v", err)
	}
	if res.IsError {
		t.Fatalf("mcp status: %s", text(res))
	}
	if got, want := text(res), strings.TrimSpace(printed.String()); got != want {
		t.Fatalf("the doors disagree:\nmcp: %s\ncli: %s", got, want)
	}
}

// A refusal is a tool error carrying the daemon's own code, never a protocol
// error the caller cannot read.
func TestARefusalReachesTheCallerAsAToolErrorWithItsCode(t *testing.T) {
	d, call := inProcessDaemon(t)
	d.Loop.BasePane = ""
	sess := session(t, call)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "dispatch", Arguments: map[string]any{"task": "7"}})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a dispatch with no base pane succeeded: %s", text(res))
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(res)), &body); err != nil {
		t.Fatalf("error body: %v", err)
	}
	// §6.3: the code is one of the contract's nine, and the sub-reason this
	// binary refuses for is the first word of the message.
	if body.Error.Code != string(codes.Unsupported) {
		t.Fatalf("error body: %+v", body.Error)
	}
	if !strings.HasPrefix(body.Error.Message, string(codes.NoBasePane)+": ") {
		t.Errorf("the message does not carry the sub-reason: %q", body.Error.Message)
	}
	if strings.Contains(body.Error.Message, string(codes.Unsupported)) {
		t.Errorf("the message repeats the code: %q", body.Error.Message)
	}
}

// The door holds itself to the schema it published: an argument no verb
// declares is refused rather than dropped in silence.
func TestTheDoorRefusesAnArgumentItsSchemaForbids(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "dispatch", Arguments: map[string]any{"task": "7", "profile": "routed"}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("the door took an argument it never declared: %s", text(res))
	}
	if !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("refusal: %s", text(res))
	}
}

func TestARequiredArgumentIsRefusedWhenItIsMissing(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "dispatch"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("dispatch with no task: %s", text(res))
	}
}

// The registration name is the repository an operator wires in, and the tools
// are bare verbs under it: a caller reads them as herdr-dispatch's dispatch,
// not as a name that repeats the binary.
func TestTheServerRegistersUnderTheRepositoryAndServesBareVerbs(t *testing.T) {
	if ServerName != "herdr-dispatch" {
		t.Fatalf("registered as %q", ServerName)
	}
	for _, want := range []string{"dispatch", "status", "review", "claims"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the instructions do not mention %q", want)
		}
	}
}

// A door is spawned per client session, so it must take nothing from the
// process that spawned it beyond the pane it was told about.
func TestTheDoorKeepsNoStateOfItsOwn(t *testing.T) {
	_, call := inProcessDaemon(t)
	first := New("0.1.0", call, Options{})
	second := New("0.1.0", call, Options{})
	if first == second {
		t.Fatal("two sessions share one server")
	}
	os.Unsetenv("HERDR_PANE_ID")
	var got protocol.Request
	sess := session(t, func(req protocol.Request) (json.RawMessage, error) {
		got = req
		return json.RawMessage(`{}`), nil
	})
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "status"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got.Pane != "" {
		t.Fatalf("a door outside a pane claimed pane %q", got.Pane)
	}
}

// Stop is on the door like every other verb, and this is the case that reads
// like an exception and is not one.
//
// The reason it was withheld was that an MCP door is spawned once per client
// session, so any agent holding one could take the dispatcher away from every
// other worker it is driving — stopping it being the operator's act, at the
// terminal they started it from. §7.3 answers that directly: withholding a
// verb from a principal is the §9 gate's job and never a door's, and the
// reason "this authority is the operator's" no longer stands on its own.
//
// It also protected nothing measurable. `hdis stop` is a CLI subcommand, so
// any agent with a shell could already run it; what the asymmetry actually
// cost was a harness with no shell. The blast radius is real and it belongs
// where a caller reads it, which is the verb's own Long — checked here, so a
// later edit cannot quietly drop the warning while keeping the tool.
func TestStopIsServedWithItsBlastRadiusStated(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)

	v, ok := verbs.ByName("stop")
	if !ok {
		t.Fatal("no stop verb")
	}
	if _, ok := verbs.ByCLI([]string{"stop"}); !ok {
		t.Error("stop has no CLI subcommand")
	}
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	served := false
	for _, tl := range tools.Tools {
		if tl.Name == v.Name {
			served = true
		}
	}
	if !served {
		t.Fatal("stop is not served over MCP; §7.3 leaves no verb on one door only")
	}
	for what, want := range map[string]string{
		"that it is not scoped to one task": "WHOLE dispatcher",
		"what happens to the live workers":  "keeps running in its pane",
		"what the caller owes first":        "Confirm with the operator",
	} {
		if !strings.Contains(v.Long, want) {
			t.Errorf("stop's description does not say %s (looked for %q)", what, want)
		}
	}
}

// §4.2 on this door: the two scope arguments the CLI's --project and
// --all-projects become are injected into every tool's schema, so a caller on
// MCP can narrow to one board the way a caller on the CLI can.
func TestEveryToolTakesTheScopeArguments(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools served")
	}
	for _, tl := range tools.Tools {
		props := properties(t, tl)
		for name, want := range map[string]string{argProject: "string", argAllProjects: "boolean"} {
			var prop struct {
				Type string `json:"type"`
			}
			raw, ok := props[name]
			if !ok {
				t.Errorf("tool %q takes no %q argument", tl.Name, name)
				continue
			}
			if err := json.Unmarshal(raw, &prop); err != nil {
				t.Fatalf("tool %q, argument %q: %v", tl.Name, name, err)
			}
			if prop.Type != want {
				t.Errorf("tool %q declares %q as %q, want %q", tl.Name, name, prop.Type, want)
			}
		}
	}
}

// The default is every board on this door too, which is what makes the
// injection additive: a caller that names no scope is answered exactly as it
// was before the arguments existed.
func TestTheMCPDoorDefaultsToEveryBoard(t *testing.T) {
	req := scopedCall(t, map[string]any{})
	if req.Project != "" || !req.AllProjects {
		t.Fatalf("default scope = %q / all=%t, want every board", req.Project, req.AllProjects)
	}
}

// §4.2: an explicit project is resolved to §4.1's canonical path HERE, in the
// door, because a relative path is the CALLER's and the daemon's working
// directory is somewhere else entirely.
func TestAnExplicitProjectIsResolvedInTheMCPDoor(t *testing.T) {
	want, err := (&worktree.Manager{}).Project(context.Background(), ".")
	if err != nil {
		t.Skipf("no git project here: %v", err)
	}
	req := scopedCall(t, map[string]any{argProject: "."})
	if req.Project != want {
		t.Fatalf("project = %q, want the canonical %q", req.Project, want)
	}
	if req.AllProjects {
		t.Fatal("a named project came through as every board")
	}
}

// Naming one board and every board is refused rather than ranked, the same
// way the CLI refuses --project with --all-projects.
func TestNamingOneBoardAndEveryBoardIsRefusedOnTheMCPDoor(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "status", Arguments: map[string]any{argProject: ".", argAllProjects: true}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
		t.Fatalf("naming both: %s", text(res))
	}
}

// The injected arguments are held to the types the schema publishes, the same
// way the declared ones are.
func TestTheScopeArgumentsAreHeldToTheirTypes(t *testing.T) {
	_, call := inProcessDaemon(t)
	sess := session(t, call)
	for _, args := range []map[string]any{
		{argProject: 7},
		{argAllProjects: "yes"},
	} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "status", Arguments: args})
		if err != nil {
			t.Fatalf("CallTool %v: %v", args, err)
		}
		if !res.IsError || !strings.Contains(text(res), string(codes.Invalid)) {
			t.Fatalf("%v was taken: %s", args, text(res))
		}
	}
}

// §4.2: the board `hdis mcp --project <path>` named is the DEFAULT scope of
// every tool call that names none. An explicit `project` or `all_projects`
// wins, because the caller is the one who knows which board it means.
func TestTheDoorsProjectIsTheDefaultScopeOfEveryCall(t *testing.T) {
	here, err := (&worktree.Manager{}).Project(context.Background(), ".")
	if err != nil {
		t.Skipf("no git project here: %v", err)
	}
	door := Options{Project: "."}

	// A call that names nothing is answered on the board the door was
	// started on, resolved to §4.1's canonical path like any other.
	req := scopedCallWith(t, door, map[string]any{})
	if req.Project != here || req.AllProjects {
		t.Errorf("a call naming no board came through as %q / all=%t, want the door's %q",
			req.Project, req.AllProjects, here)
	}

	// An explicit board wins over the door's.
	req = scopedCallWith(t, Options{Project: "/nowhere/at/all"}, map[string]any{argProject: "."})
	if req.Project != here || req.AllProjects {
		t.Errorf("an explicit project came through as %q / all=%t, want %q",
			req.Project, req.AllProjects, here)
	}

	// And so does an explicit all_projects, which is unchanged by the
	// default: a caller that asked for every board gets every board.
	req = scopedCallWith(t, door, map[string]any{argAllProjects: true})
	if req.Project != "" || !req.AllProjects {
		t.Errorf("all_projects came through as %q / all=%t, want every board",
			req.Project, req.AllProjects)
	}
}

// A door started on no board is unchanged: every call defaults to every
// board, exactly as it did before the flag was read.
func TestADoorStartedOnNoBoardStillDefaultsToEveryBoard(t *testing.T) {
	req := scopedCallWith(t, Options{}, map[string]any{})
	if req.Project != "" || !req.AllProjects {
		t.Fatalf("default scope = %q / all=%t, want every board", req.Project, req.AllProjects)
	}
}

// scopedCall makes one call through the door and hands back the request it
// built, so a test can read what the scope resolved to.
func scopedCall(t *testing.T, args map[string]any) protocol.Request {
	t.Helper()
	return scopedCallWith(t, Options{}, args)
}

// scopedCallWith is the same, for a door STARTED with a scope of its own.
func scopedCallWith(t *testing.T, opt Options, args map[string]any) protocol.Request {
	t.Helper()
	var got protocol.Request
	sess := sessionWith(t, func(req protocol.Request) (json.RawMessage, error) {
		got = req
		return json.RawMessage(`{}`), nil
	}, opt)
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "status", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %v: %v", args, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %v: %s", args, text(res))
	}
	return got
}

// properties reads one tool's published schema properties.
func properties(t *testing.T, tl *mcp.Tool) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(tl.InputSchema)
	if err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema for %q: %v", tl.Name, err)
	}
	return schema.Properties
}

// The two claims about profiles are both true and neither replaces the other:
// a caller cannot pick a model or an effort, and the board row's priority
// picks which configured profile the worker launches with.
func TestTheInstructionsSayPriorityPicksTheProfileAndACallerStillCannot(t *testing.T) {
	for what, want := range map[string]string{
		"that model and effort are not per-call": "not selectable per call",
		"that priority selects the profile":      "priority selects WHICH configured profile",
	} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the instructions do not say %s (looked for %q)", what, want)
		}
	}
}
