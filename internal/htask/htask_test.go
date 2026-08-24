package htask

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

func client(t *testing.T) (*Client, *testenv.Fake) {
	t.Helper()
	return &Client{}, testenv.New(t)
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
	want := "list --ready --all-projects --json --as plugin:hdis"
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
	if got, want := f.Calls(t)[0], "get 01AAA --all-projects --json --as plugin:hdis"; got != want {
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
	if got, want := f.Calls(t)[0], "get 01AAA --all-projects --json --as plugin:hdis"; got != want {
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
	want := "list --mine --all-projects --json --as plugin:hdis@wM:p1"
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
	want := "release 01AAA --all-projects --note no worker was ever brought up --json --as plugin:hdis@wM:p1"
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
	if got, want := f.Calls(t)[0], "get 01AAA --all-projects --json --as plugin:hdis"; got != want {
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

// A pane names its task by number, and a number is unique only inside a
// project — so a by-number read is scoped to one, which is the addressing
// the board's refusal of a bare number across projects points at. The by-id
// read above stays board-agnostic; this is the other half of it.
func TestGetInScopesANumberToOneProject(t *testing.T) {
	c, f := client(t)
	f.Bin(t, "htask", `case " $* " in
*" --all-projects "*) echo '{"error":{"code":"USAGE","message":"\"7\" is not a 26-character id"}}'; exit 2 ;;
esac
cat <<'EOF2'
{"task":{"id":"01AAA","seq":7,"project":"/src/p","title":"first","status":"doing"},"ready":false,"dependents":[]}
EOF2`)

	task, err := c.GetIn(context.Background(), "7", "/src/p")
	if err != nil {
		t.Fatalf("get in: %v", err)
	}
	if task.ID != "01AAA" || task.Seq != 7 {
		t.Fatalf("got %+v", task)
	}
	if got, want := f.Calls(t)[0], "get 7 --project /src/p --json --as plugin:hdis"; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
}

// The board call declares a plugin principal, and a principal is what it
// declares INSTEAD of a pane. Leaving cmd.Env nil hands the child the
// daemon's environment verbatim, so every call arrives carrying both a
// declared principal and the daemon's pane id, and the board cannot tell a
// worker claiming as plugin:hdis apart from the dispatcher itself.
func TestABoardCallCarriesNoPaneIntoTheSubprocess(t *testing.T) {
	c, f := client(t)
	t.Setenv("HERDR_PANE_ID", "wM:p1")
	t.Setenv("HERDR_TAB_ID", "wM:t1")
	t.Setenv("HERDR_WORKSPACE_ID", "wM")
	f.Bin(t, "htask", `env > "$HDIS_FAKE_DIR/env.txt"
echo '{"version":"0.1.0"}'`)

	if _, err := c.Doctor(context.Background()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	child := childEnv(t, f)
	for _, name := range []string{"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID"} {
		if v, ok := child[name]; ok {
			t.Fatalf("%s reached the board call as %q; a plugin principal carries no pane", name, v)
		}
	}
	// The scrub removes four names, not the environment: a child with no
	// PATH cannot find the binaries the board itself shells out to.
	if child["PATH"] == "" {
		t.Fatalf("the subprocess lost PATH; want the daemon's environment minus the four Herdr names")
	}
}

// The daemon still reads those names for its own purposes: cmd/hdis/main.go
// defaults --pane from HERDR_PANE_ID, and the base pane is what every split
// comes off. The scrub is scoped to the subprocess and must not reach it.
func TestScrubbingThePaneLeavesTheDaemonsOwnEnvironmentAlone(t *testing.T) {
	c, f := client(t)
	t.Setenv("HERDR_PANE_ID", "wM:p1")
	t.Setenv("HERDR_TAB_ID", "wM:t1")
	t.Setenv("HERDR_WORKSPACE_ID", "wM")
	f.Bin(t, "htask", `echo '{"version":"0.1.0"}'`)

	if _, err := c.Doctor(context.Background()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for name, want := range map[string]string{
		"HERDR_PANE_ID":      "wM:p1",
		"HERDR_TAB_ID":       "wM:t1",
		"HERDR_WORKSPACE_ID": "wM",
	} {
		if got := os.Getenv(name); got != want {
			t.Fatalf("daemon's own %s is %q after a board call, want %q", name, got, want)
		}
	}
}

// The point is a plugin principal WITHOUT a pane, not a call with neither.
// A call that lost its --as would be attributed to nobody at all.
func TestTheCallStillDeclaresThePluginPrincipalWithoutAPane(t *testing.T) {
	c, f := client(t)
	c.Principal = PrincipalFor("wM:p1")
	t.Setenv("HERDR_PANE_ID", "wM:p1")
	f.Bin(t, "htask", `env > "$HDIS_FAKE_DIR/env.txt"
echo '{"tasks":[{"id":"01AAA","seq":7,"project":"/src/a","title":"first","status":"todo"}],"count":1}'`)

	tasks, err := c.Ready(context.Background())
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "01AAA" {
		t.Fatalf("got %+v", tasks)
	}
	want := "list --ready --all-projects --json --as plugin:hdis@wM:p1"
	if got := f.Calls(t)[0]; got != want {
		t.Fatalf("argv: got %q, want %q", got, want)
	}
	if v, ok := childEnv(t, f)["HERDR_PANE_ID"]; ok {
		t.Fatalf("the call declares a principal and still carried HERDR_PANE_ID=%q", v)
	}
}

// childEnv reads the environment the fake binary saw, as name to value.
func childEnv(t *testing.T, f *testenv.Fake) map[string]string {
	t.Helper()
	b, err := os.ReadFile(f.Path("env.txt"))
	if err != nil {
		t.Fatalf("read child env: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

// The scrub removes four names and nothing else.
//
// A prefix match on HERDR_ would look equivalent and is not: htask dials
// herdr itself - that is what `herdr_reachable` in its doctor report is - and
// it finds it through HERDR_SOCKET_PATH and HERDR_BIN_PATH. Stripping those
// would leave the board unable to reach herdr on every call the dispatcher
// makes, so the list is exact names rather than a family.
func TestTheScrubRemovesFourNamesAndKeepsEveryOther(t *testing.T) {
	in := []string{
		"HERDR_PANE_ID=wM:p1",
		"HERDR_TAB_ID=wM:t1",
		"HERDR_WORKSPACE_ID=wM",
		"HERDR_PLUGIN_CONTEXT_JSON={}",
		"HERDR_SOCKET_PATH=/run/herdr.sock",
		"HERDR_BIN_PATH=/opt/herdr",
		"HERDR_ENV=1",
		"PATH=/usr/bin:/bin",
		"HOME=/Users/x",
	}
	got := envWithoutPane(in)
	want := []string{
		"HERDR_SOCKET_PATH=/run/herdr.sock",
		"HERDR_BIN_PATH=/opt/herdr",
		"HERDR_ENV=1",
		"PATH=/usr/bin:/bin",
		"HOME=/Users/x",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("scrubbed environment:\n got %q\nwant %q", got, want)
	}
}

// §4.2: htask resolves its project from HERDR_PLUGIN_CONTEXT_JSON before it
// falls back to the working directory, reading the focused pane's cwd out of
// it. Herdr fills that variable in for the commands it spawns itself — this
// plugin's [[startup]] among them — so a daemon started that way hands it to
// every board call, and every call is silently scoped to whatever project the
// operator happened to be looking at when Herdr started the plugin.
//
// That is worse than the pane names it sits beside. A pane on a board call is
// a wrong ATTRIBUTION and the call still lands; a context document is a wrong
// BOARD, and a board with nothing on it looks exactly like a board with
// nothing ready. The dispatcher scopes its own calls with --project and
// --all-projects, and neither means anything if the variable underneath has
// already chosen.
func TestABoardCallCarriesNoHerdrPluginContext(t *testing.T) {
	c, f := client(t)
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", `{"focused_pane_cwd":"/src/somewhere-else"}`)
	f.Bin(t, "htask", `env > "$HDIS_FAKE_DIR/env.txt"
echo '{"version":"0.1.0"}'`)

	if _, err := c.Doctor(context.Background()); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if v, ok := childEnv(t, f)["HERDR_PLUGIN_CONTEXT_JSON"]; ok {
		t.Fatalf("HERDR_PLUGIN_CONTEXT_JSON reached the board call as %q; every call would be scoped to whatever pane the operator was focused on", v)
	}
}

// A name that merely starts the same is a different variable and stays.
func TestOnlyAWholeNameMatchesTheScrub(t *testing.T) {
	got := envWithoutPane([]string{"HERDR_PANE_ID_SUFFIX=keep", "HERDR_PANE_ID=drop"})
	if want := []string{"HERDR_PANE_ID_SUFFIX=keep"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
