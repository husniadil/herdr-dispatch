package decide

import (
	"testing"
	"time"
)

// The mutation pass asks for a compiling mutation per claimed guard and the
// tests the report names. A submission that changed no code and names no test
// gives it nothing to bite on, so the evidence says the shot has no work.
func TestASubmissionWithNoCodeAndNoTestNamedHasNothingToMutate(t *testing.T) {
	for name, tc := range map[string]struct {
		changed []string
		report  string
	}{
		"nothing tracked changed at all": {nil, "wrote the notes into a file"},
		"only prose changed":             {[]string{"docs/notes.txt", "README.md"}, "wrote the notes"},
		"a file with no extension":       {[]string{"NOTES"}, "wrote the notes"},
		"a dotfile with no extension":    {[]string{".gitignore"}, "ignored the debris"},
		"the word test inside another":   {[]string{"notes.md"}, "this is the latest reading of it"},
	} {
		if !NothingToMutate(tc.changed, tc.report) {
			t.Errorf("%s: the mutation shot was kept for %v with report %q", name, tc.changed, tc.report)
		}
	}
}

// Either half is enough to keep it: code in the diff gives a mutation
// somewhere to land, and a report naming a test gives one something to run.
func TestCodeInTheDiffOrATestInTheReportKeepsTheMutationShot(t *testing.T) {
	for name, tc := range map[string]struct {
		changed []string
		report  string
	}{
		"a go file":                {[]string{"internal/loop/loop.go"}, "wrote it"},
		"a go file among prose":    {[]string{"README.md", "internal/loop/loop.go"}, "wrote it"},
		"another language":         {[]string{"scripts/release.py"}, "wrote it"},
		"a shell script":           {[]string{"scripts/gate.sh"}, "wrote it"},
		"a test named in a report": {nil, "the gate: `go test ./...` passes"},
		"a named Go test":          {[]string{"docs/notes.txt"}, "TestTheGoalIsDelivered pins it"},
		"a hyphenated test word":   {nil, "the unit-tests pass"},
	} {
		if NothingToMutate(tc.changed, tc.report) {
			t.Errorf("%s: the mutation shot was skipped for %v with report %q", name, tc.changed, tc.report)
		}
	}
}

// The skip is remembered on the binding, because the evidence is read at the
// edge and the core is what stops asking for the shot again. Without it the
// same submission is re-examined every tick the worker sits idle in review.
func TestASkippedShotIsNotAskedForAgain(t *testing.T) {
	s := verifySnapshot()
	s.Agents["wM:p9"] = "idle"
	s.Bindings[0].ShotSkipped = true
	p := Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true}
	if got := selfReviews(Decide(s, p)); len(got) != 0 {
		t.Fatalf("a shot the evidence skipped was asked for again: %+v", got)
	}
}

// And it is forgotten with the submission it was about: a task out of review
// carrying the skip is rearmed, so the next submission is judged on its own
// evidence.
func TestASkippedShotIsRearmedWithTheNextSubmission(t *testing.T) {
	s := verifySnapshot()
	s.Tasks["t1"] = Task{ID: "t1", Status: "doing", ClaimedBy: "wM:p9"}
	s.Bindings[0].ShotSkipped = true
	p := Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Verify: true}
	if !has(Decide(s, p), Rearm) {
		t.Fatalf("the binding that skipped a shot was not rearmed: %v", actionKinds(Decide(s, p)))
	}
}
