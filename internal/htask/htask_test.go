package htask

import (
	"context"
	"errors"
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
	if got, want := f.Calls(t)[0], "task get 01AAA --all-projects --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
	if (Task{}).Pane() != "" {
		t.Fatal("an unclaimed task has no pane")
	}
	if (Task{ClaimedBy: "human"}).Pane() != "" {
		t.Fatal("a principal that is not an agent has no pane")
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

// The board's error envelope carries a code a caller can branch on, and a
// call that never reached the board carries none: the difference between a
// task that does not exist and a door that could not answer.
func TestTheBoardsErrorEnvelopeIsCarriedAsARefusal(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `echo '{"error":{"code":"NOT_FOUND","message":"no task 999 in /src/p"}}'; exit 3`)

	_, err := c.Get(context.Background(), "999")
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refusal.Code != "NOT_FOUND" || refusal.Message != "no task 999 in /src/p" {
		t.Fatalf("refusal: %+v", refusal)
	}

	f.Bin(t, "htask", `echo 'unknown flag --as' >&2; exit 2`)
	_, err = c.Get(context.Background(), "999")
	if errors.As(err, &refusal) {
		t.Fatalf("a door that never answered was read as a refusal: %+v", refusal)
	}
	if !strings.Contains(err.Error(), "unknown flag --as") {
		t.Fatalf("want the door's own words, got %v", err)
	}
}

// A task id is unique to the board, not to a project, and the dispatcher is
// not scoped to one repository: `task list --ready` already looks across
// every project, and the by-id lookup that validates a dispatch has to look
// exactly as wide, or a task filed on another project's board reads as a
// task that does not exist.
func TestGetLooksAcrossEveryProject(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"task":{"id":"01AAA","seq":7,"project":"/src/other","title":"first","status":"todo"},"ready":true,"dependents":[]}
EOF`)

	if _, err := c.Get(context.Background(), "01AAA"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got, want := f.Calls(t)[0], "task get 01AAA --all-projects --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// The principal a daemon writes with names the daemon, so a row the board is
// holding can be told from a peer daemon's row without asking anyone.
func TestThePrincipalCarriesTheDaemonThatMadeTheReservation(t *testing.T) {
	if got, want := PrincipalFor("wM:p1"), "plugin:hdis@wM:p1"; got != want {
		t.Fatalf("principal: got %q, want %q", got, want)
	}
	// A daemon with no pane of its own has nothing to carry, and falls back
	// to the bare plugin principal the contract names.
	if got, want := PrincipalFor(""), Principal; got != want {
		t.Fatalf("principal with no pane: got %q, want %q", got, want)
	}
}

// Held is the board's own answer to "what is this dispatcher holding": the
// rows its principal claims, on every project, whatever their status.
func TestHeldAsksTheBoardWhatThisDaemonIsHolding(t *testing.T) {
	c, f := client(t)
	c.Principal = "plugin:hdis@wM:p1"
	f.Bin(t, "htask", `cat <<'EOF'
{"tasks":[{"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"doing","claimed_by":"plugin:hdis@wM:p1"}],"count":1}
EOF`)

	held, err := c.Held(context.Background())
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if len(held) != 1 || held[0].ID != "01AAA" {
		t.Fatalf("got %+v", held)
	}
	want := "task list --mine --all-projects --json --as plugin:hdis@wM:p1"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// Releasing a hold this daemon left behind hands the task back with a note
// saying why, in the daemon's own name.
func TestReleaseHandsBackAHoldWithItsReason(t *testing.T) {
	c, f := client(t)
	c.Principal = "plugin:hdis@wM:p1"
	f.Bin(t, "htask", `echo '{"task":{"id":"01AAA","seq":7,"status":"todo"}}'`)

	if err := c.Release(context.Background(), "01AAA", "no worker was ever brought up"); err != nil {
		t.Fatalf("release: %v", err)
	}
	want := "task release 01AAA --all-projects --note no worker was ever brought up --json --as plugin:hdis@wM:p1"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// The row names the pane it was created from when a pane created it. That
// address is the whole point of the read: a report belongs to whoever wanted
// the work, and until the row carries a pane there is nothing to route to.
//
// A task an operator filed at a terminal legitimately has no pane, so the
// field arrives empty rather than failing the parse. Both bodies below go
// through the read the dispatcher actually uses.
func TestGetReportsThePaneATaskWasCreatedFrom(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"task":{"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"todo","pane_id":"wZ:p2"},"ready":true,"dependents":[]}
EOF`)

	task, err := c.Get(context.Background(), "01AAA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got, want := task.PaneID, "wZ:p2"; got != want {
		t.Fatalf("pane of origin is %q, want %q", got, want)
	}
	if got, want := f.Calls(t)[0], "task get 01AAA --all-projects --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// A board that records no pane for a row is not an error: it is the operator
// case, and the field simply arrives empty. The body below is a capture of
// the real CLI answering for a task a human filed.
func TestATaskWithNoPaneOfOriginParsesWithAnEmptyOne(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `cat <<'EOF'
{"task":{"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"todo","created_by":"human"},"ready":true,"dependents":[]}
EOF`)

	task, err := c.Get(context.Background(), "01AAA")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if task.PaneID != "" {
		t.Fatalf("pane of origin is %q, want empty", task.PaneID)
	}
	if task.ID != "01AAA" {
		t.Fatalf("the row did not survive the parse: %+v", task)
	}
}
