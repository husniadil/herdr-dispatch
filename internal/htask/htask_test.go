package htask

import (
	"context"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/fake"
)

func client(t *testing.T) (*Client, *fake.Fake) {
	t.Helper()
	return &Client{}, fake.New(t)
}

// Discovery goes through `doctor --json`, and every call names hdis as its
// principal so the board's event trail attributes them.
func TestDoctorReadsTheBoardsOwnReport(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"version":"0.1.0","contract":"0.3.0-draft","binary":"/opt/htask","project":"/src/p",
 "socket_live":true,"herdr_reachable":true,"principal":"agent:wM:pP","lease_seconds":900}
EOF`)

	d, err := c.Doctor(context.Background())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if d.Version != "0.1.0" || d.Contract != "0.3.0-draft" || d.Project != "/src/p" {
		t.Fatalf("got %+v", d)
	}
	if !d.SocketLive || !d.HerdrReachable {
		t.Fatalf("want a live board reaching herdr, got %+v", d)
	}
	if got, want := f.Calls(t)[0], "doctor --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// Ready work is every unblocked, unclaimed todo task on every project.
func TestReadyListsUnclaimedTodoAcrossEveryProject(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"tasks":[
  {"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"todo"},
  {"id":"01BBB","seq":9,"project":"/src/b","title":"second","status":"todo"}
],"count":2}
EOF`)

	tasks, err := c.Ready(context.Background())
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if len(tasks) != 2 || tasks[0].ID != "01AAA" || tasks[1].Seq != 9 || tasks[0].Project != "/src/a" {
		t.Fatalf("got %+v", tasks)
	}
	want := "task list --ready --all-projects --json --as plugin:hdis"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// A claim names a principal; the pane inside it is what a binding is keyed on.
//
// `task get --json` wraps the row in an envelope, unlike `task list` and
// `doctor`, which answer flat. Reading it flat silently yields a zero row,
// which is worse than an error: the dispatcher holds a binding whose task it
// believes it knows nothing about, so review is never announced. The body
// below is a capture of the real CLI.
func TestGetReportsTheClaimingPane(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"task":{"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"doing","claimed_by":"agent:wM:p3","claimed_by_name":"hdis-7","ever_claimed":true},"ready":false,"dependents":[]}
EOF`)

	task, err := c.Get(context.Background(), "01AAA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.Status != "doing" || task.Pane() != "wM:p3" {
		t.Fatalf("got %+v pane %q", task, task.Pane())
	}
	if got, want := f.Calls(t)[0], "task get 01AAA --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
	if (Task{}).Pane() != "" {
		t.Fatal("an unclaimed task has no pane")
	}
	if (Task{ClaimedBy: "human"}).Pane() != "" {
		t.Fatal("a principal that is not an agent has no pane")
	}
}

// The goal reaches the worker through an argv that refuses a newline, so it
// is asked for in the form that has none.
func TestGoalIsAskedForAsOneLine(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `printf 'do the thing. \xc2\xb7 Done when: ... '`)

	goal, err := c.Goal(context.Background(), "01AAA")
	if err != nil {
		t.Fatalf("goal: %v", err)
	}
	if strings.ContainsAny(goal, "\n\r") {
		t.Fatalf("goal carries a line break: %q", goal)
	}
	if strings.HasSuffix(goal, " ") || !strings.HasPrefix(goal, "do the thing") {
		t.Fatalf("goal not trimmed: %q", goal)
	}
	if got, want := f.Calls(t)[0], "task goal 01AAA --one-line --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// When htask refuses, the operator gets htask's own words and not a wrapper's.
func TestAnHtaskRefusalIsReportedInItsOwnWords(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `echo "htask: CONFLICT: task is not claimed" >&2; exit 1`)

	_, err := c.Get(context.Background(), "01AAA")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "CONFLICT: task is not claimed") {
		t.Fatalf("want htask's message, got %v", err)
	}
}

// A board that is not installed is a loud failure, not an empty queue.
func TestAMissingHtaskIsNotAnEmptyQueue(t *testing.T) {
	c, _ := client(t)
	c.Bin = "htask-that-is-not-installed"
	if _, err := c.Ready(context.Background()); err == nil {
		t.Fatal("want an error")
	}
}
