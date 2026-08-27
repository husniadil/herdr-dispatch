package mcpdoor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// spy is a Caller that records every request the door built, which is what
// §7.5 is about: the declaration travels on the request or it travels nowhere.
func spy(seen *[]protocol.Request, mu *sync.Mutex) Caller {
	return func(req protocol.Request) (json.RawMessage, error) {
		mu.Lock()
		*seen = append(*seen, req)
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
}

// §7.5: a door started with the declaration sends it on every call, and the
// principal the daemon records for that call is the operator. A door nobody
// declared says nothing and is nobody, because absence of evidence is not
// evidence of the operator (§3.7).
func TestTheDeclaredDoorIsTheOperatorAndTheUndeclaredOneIsNot(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  Options
		want string
	}{
		"a door nobody declared":          {Options{}, "none"},
		"a door declared with --operator": {Options{Operator: true}, "human"},
	} {
		// The door §7.5 answers for stands in no pane: it was registered in
		// a desktop MCP client, which Herdr never injected a pane into.
		t.Setenv("HERDR_PANE_ID", "")
		var seen []protocol.Request
		var mu sync.Mutex
		sess := sessionWith(t, spy(&seen, &mu), tc.opt)

		for _, tool := range []string{"doctor", "status", "parked_list"} {
			if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool}); err != nil {
				t.Fatalf("%s: %s: %v", name, tool, err)
			}
		}
		mu.Lock()
		got := append([]protocol.Request(nil), seen...)
		mu.Unlock()
		if len(got) != 3 {
			t.Fatalf("%s: %d requests reached the daemon, want 3", name, len(got))
		}
		for _, req := range got {
			if req.Operator != tc.opt.Operator {
				t.Errorf("%s: %s was sent with operator=%v, and the door was started with %v",
					name, req.Verb, req.Operator, tc.opt.Operator)
			}
			if caller := req.Caller(); caller != tc.want {
				t.Errorf("%s: %s is attributed to %q, want %q", name, req.Verb, caller, tc.want)
			}
		}
	}
}

// §7.5's first property: the declaration is read once, from the server
// command, and MUST NOT arrive as a tool argument. Three things have to hold
// at once, so all three are here — no schema offers it, a call that tries to
// carry it is refused with USAGE, and it does not reach the daemon either.
func TestTheOperatorDeclarationNeverArrivesPerCall(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	for _, v := range verbs.All {
		props, _ := tool(v).InputSchema.(map[string]any)["properties"].(map[string]any)
		for _, name := range []string{argOperator, "as", "principal", "human"} {
			if _, ok := props[name]; ok {
				t.Errorf("tool %q offers %q as an argument; §7.5 forbids the declaration "+
					"reaching a door through a call", v.MCP, name)
			}
		}
	}

	var seen []protocol.Request
	var mu sync.Mutex
	sess := session(t, spy(&seen, &mu))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "doctor", Arguments: map[string]any{argOperator: true}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("the door accepted a call carrying the declaration: %s", text(res))
	}
	// USAGE, naming the argument, and saying what to do instead. The
	// fall-through "takes no argument named" would satisfy the first two on
	// its own, so a refusal that lost the by-name case would go unnoticed —
	// and a caller that wrote `operator` meant something this door will never
	// do and should be pointed at the flag that does it.
	body := text(res)
	if !strings.Contains(body, string(codes.Usage)) || !strings.Contains(body, argOperator) {
		t.Fatalf("refused with %s, want USAGE naming %s", body, argOperator)
	}
	if !strings.Contains(body, "hdis mcp --operator") {
		t.Fatalf("refused with %s, and it does not say the declaration is made by starting "+
			"the door with `hdis mcp --operator` (§7.5)", body)
	}
	mu.Lock()
	reached := len(seen)
	mu.Unlock()
	if reached != 0 {
		t.Fatalf("%d requests reached the daemon; the refusal happens at the door", reached)
	}

	// And the same call without the rejected argument goes through an
	// undeclared door declaring nothing.
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("%d requests reached the daemon, want 1", len(seen))
	}
	if seen[0].Operator {
		t.Fatal("an undeclared door told the daemon it speaks for the operator")
	}
}

// §7.5's fourth property, the loud half: a door given --operator inside a
// Herdr pane refuses to START, with FORBIDDEN. It is defence in depth rather
// than the thing that stops the escalation — the test below holds that — and
// it earns its place by failing loudly once instead of running an ambiguous
// door all day.
func TestServeRefusesADeclaredDoorInsideAPane(t *testing.T) {
	// Cancelled before it starts, so the cases that ARE allowed to serve
	// return from the transport promptly instead of reading stdin. What is
	// under test is which of the two answers comes back.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	silent := func(protocol.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

	for name, tc := range map[string]struct {
		pane    string
		opt     Options
		refused bool
	}{
		"declared inside a pane":   {"wM:p1", Options{Operator: true}, true},
		"declared in no pane":      {"", Options{Operator: true}, false},
		"undeclared inside a pane": {"wM:p1", Options{}, false},
		"undeclared and paneless":  {"", Options{}, false},
	} {
		t.Setenv("HERDR_PANE_ID", tc.pane)
		done := make(chan error, 1)
		opt := tc.opt
		go func() { done <- Serve(ctx, "0.1.0", silent, opt) }()
		var err error
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: Serve did not return", name)
		}
		var named *codes.Error
		refused := errors.As(err, &named) && named.Code == codes.Forbidden
		if refused != tc.refused {
			t.Errorf("%s: refused = %v, want %v (err = %v)", name, refused, tc.refused, err)
			continue
		}
		if tc.refused && !strings.Contains(err.Error(), tc.pane) {
			t.Errorf("%s: the refusal does not name the pane: %v", name, err)
		}
	}
}

// And the property that startup check is only the loud half of: a declared
// door that somehow IS inside a pane is still that pane's agent, never the
// operator. This is what actually prevents the escalation — Caller reads the
// pane before it reads the declaration — so it is pinned here rather than
// left resting on a check a caller can avoid by starting the door another way.
func TestAnInPaneDeclaredDoorIsStillThePanesAgent(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "wM:p9")
	var seen []protocol.Request
	var mu sync.Mutex
	sess := sessionWith(t, spy(&seen, &mu), Options{Operator: true})
	if _, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "doctor"}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("%d requests reached the daemon, want 1", len(seen))
	}
	if got := seen[0].Caller(); got != "agent:wM:p9" {
		t.Fatalf("caller = %q, want agent:wM:p9: the declaration overruled the pane", got)
	}
	// And the door really did send the declaration, so what loses to the pane
	// is the ordering in Caller and not the door quietly dropping the flag.
	if !seen[0].Operator {
		t.Fatal("the door dropped the declaration; this test would then pass for the wrong reason")
	}
}
