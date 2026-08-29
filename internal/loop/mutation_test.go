package loop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/store"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// workedIn stages a file in the checkout the one worker was given, which is
// what a submission's diff is read from.
func workedIn(t *testing.T, project, name, body string) {
	t.Helper()
	dirs := worktreesOf(t, project)
	if len(dirs) != 1 {
		t.Fatalf("the worker holds %d checkouts: %v", len(dirs), dirs)
	}
	path := filepath.Join(dirs[0], name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dirs[0], "add", name).CombinedOutput()
	if err != nil {
		t.Fatalf("git add %s: %v: %s", name, err, out)
	}
}

// A submission whose diff carries code earns the shot: there is somewhere for
// a compiling mutation to land.
func TestASubmissionWhoseDiffCarriesCodeEarnsTheMutationShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	workedIn(t, project, "guard.go", "package p\n")
	submittedWithReport(t, f, project, "wrote the guard")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("a submission carrying code earned %d shots: %v", len(got), got)
	}
}

// And a submission whose deliverable is a document nobody tracked, with a
// report naming no test, earns none: the mutation pass has nothing to bite on
// and the operator would be mailed a report of zero mutations.
func TestASubmissionWithNoCodeAndNoTestNamedEarnsNoMutationShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submittedWithReport(t, f, project, "wrote the notes into notes.txt")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 0 {
		t.Fatalf("a submission with no code earned %d shots: %v", len(got), got)
	}
	// Announced all the same: the operator still judges the submission.
	if got := calls(t, f, "notification show"); len(got) != 1 {
		t.Fatalf("review was announced %d times: %v", len(got), got)
	}
}

// The trail says why it did not fire, because a shot that silently never
// happens is indistinguishable from a lane that is off.
func TestASkippedShotIsOnTheTrailWithItsReason(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submittedWithReport(t, f, project, "wrote the notes into notes.txt")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	ev := one(t, l, EventName(store.EntityWorker, KindShotSkipped))
	if got := ev.Detail["reason"]; got != decide.ReasonNoCodeToMutate {
		t.Fatalf("the skipped shot carries reason %q", got)
	}
	if got := ev.Detail["pane"]; got != "wM:p9" {
		t.Fatalf("the skipped shot names pane %q", got)
	}
	if ev.EntityID != "01AAA" {
		t.Fatalf("the skipped shot names task %q", ev.EntityID)
	}
}

// Once, however many ticks pass over the same submission: the binding
// remembers the answer, so the diff is not re-read to the same conclusion
// every tick and the trail is not filled with it.
func TestTheSameSubmissionIsSkippedOnlyOnce(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submittedWithReport(t, f, project, "wrote the notes into notes.txt")
	for i := 0; i < 4; i++ {
		if err := l.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	skips := 0
	for _, name := range names(t, l) {
		if name == EventName(store.EntityWorker, KindShotSkipped) {
			skips++
		}
	}
	if skips != 1 {
		t.Fatalf("one submission was skipped %d times: %v", skips, names(t, l))
	}
	if got := calls(t, f, "agent prompt"); len(got) != 0 {
		t.Fatalf("the skipped submission earned %d shots: %v", len(got), got)
	}
	w, _ := bindingFor(l, "wM:p9")
	if !w.ShotSkipped {
		t.Fatalf("the binding does not remember the skip: %+v", w)
	}
}

// A report that names a test keeps the shot even with no code in the diff:
// there is something to run, and a documentation task can still name the gate.
func TestAReportNamingATestKeepsTheShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submittedWithReport(t, f, project, "wrote the notes; `go test ./...` still passes")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("a report naming a test earned %d shots: %v", len(got), got)
	}
}

// A diff nobody could read keeps the shot. No evidence is not evidence of
// nothing, and the expensive answer is the safe one.
func TestADiffThatCannotBeReadKeepsTheShot(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	for _, dir := range worktreesOf(t, project) {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	submittedWithReport(t, f, project, "wrote the notes into notes.txt")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	shots := calls(t, f, "agent prompt")
	if len(shots) != 1 {
		t.Fatalf("an unreadable diff earned %d shots: %v", len(shots), shots)
	}
}

// And the skip is about ONE submission. A rejection rearms the binding, and
// the next submission is judged on its own evidence.
func TestANewSubmissionIsJudgedOnItsOwnEvidence(t *testing.T) {
	l, f, project := newVerifyLoop(t, true)
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	submittedWithReport(t, f, project, "wrote the notes into notes.txt")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}

	// Rejected: back to doing, with the worker still holding it and working.
	f.Write(t, "get.json", row(project, "doing", "agent:wM:p9"))
	f.Write(t, "panes.json", paneList("wM:p9", "working"))
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if w, _ := bindingFor(l, "wM:p9"); w.ShotSkipped {
		t.Fatalf("the binding still remembers the settled submission: %+v", w)
	}

	// Submitted again, this time with code behind it.
	workedIn(t, project, "guard.go", "package p\n")
	submittedWithReport(t, f, project, "wrote the guard")
	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("fourth tick: %v", err)
	}
	if got := calls(t, f, "agent prompt"); len(got) != 1 {
		t.Fatalf("the second submission earned %d shots: %v", len(got), got)
	}
}

// submittedWithReport is submitted with the board's row carrying the report
// the worker filed, which is half of what the shot is decided on.
func submittedWithReport(t *testing.T, f *testenv.Fake, project, report string) {
	t.Helper()
	f.Write(t, "ready.json", `{"tasks":[],"count":0}`)
	f.Write(t, "get.json", strings.Replace(row(project, "review", "agent:wM:p9"),
		`"pane_id":""`, `"pane_id":"","report":`+quote(report), 1))
	f.Write(t, "panes.json", paneList("wM:p9", "idle"))
}

// quote renders one JSON string, so a report with a backtick or a quote in it
// reaches the fake board the way the real one would answer it.
func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
