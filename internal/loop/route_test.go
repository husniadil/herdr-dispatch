package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

const routingDoc = `default = "worker"

[profiles.worker]
provider = "claude"
model = "opus"

[profiles.heavy]
provider = "claude"
model = "opus"
effort = "high"

[[route]]
min_priority = 5
profile = "heavy"
`

// routedLoop is the ordinary loop with a routing table and one ready task at
// the given board priority.
func routedLoop(t *testing.T, priority int) (*Loop, *testenv.Fake) {
	t.Helper()
	l, f := newLoop(t)
	cfg, err := config.Parse([]byte(routingDoc))
	if err != nil {
		t.Fatal(err)
	}
	l.Config = cfg
	l.Policy.Routes = Routes(cfg)
	f.Write(t, "ready.json", `{"tasks":[{"id":"01AAA","seq":7,"project":"/src/p","title":"do the thing","status":"todo","priority":`+itoa(priority)+`}],"count":1}`)
	return l, f
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The whole feature at the edge: a task the board prices high enough comes up
// with the routed profile's model and effort in the argv herdr forwards.
func TestAHighPriorityTaskLaunchesWithTheRoutedProfilesModelAndEffort(t *testing.T) {
	l, f := routedLoop(t, 5)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	start := calls(t, f, "agent start")
	if len(start) != 1 {
		t.Fatalf("agent start ran %d times", len(start))
	}
	if !strings.Contains(start[0], "--model opus --effort high") {
		t.Fatalf("the routed model and effort did not reach agent start: %q", start[0])
	}
	if len(l.bindings) != 1 || l.bindings[0].Profile != "heavy" {
		t.Fatalf("the binding does not record the profile it launched with: %+v", l.bindings)
	}
}

// The same fleet, a task nothing routes: the project's own profile, exactly
// as before routes existed.
func TestATaskBelowEveryRouteKeepsTheDefaultProfile(t *testing.T) {
	l, f := routedLoop(t, 1)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	start := calls(t, f, "agent start")
	if len(start) != 1 {
		t.Fatalf("agent start ran %d times", len(start))
	}
	if !strings.Contains(start[0], "--model opus --effort low") {
		t.Fatalf("the default profile did not reach agent start: %q", start[0])
	}
	if len(l.bindings) != 1 || l.bindings[0].Profile != "worker" {
		t.Fatalf("binding: %+v", l.bindings)
	}
}

// An operator reading status is told which profile each worker is running,
// which is the one thing the board cannot tell them.
func TestStatusSaysWhichProfileEachWorkerLaunchedWith(t *testing.T) {
	l, f := routedLoop(t, 5)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	_ = f

	st, err := l.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(st.Workers) != 1 || st.Workers[0].Profile != "heavy" {
		t.Fatalf("workers: %+v", st.Workers)
	}
}
