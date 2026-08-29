package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/decide"
)

func tempStore(t *testing.T) *Bindings {
	t.Helper()
	return &Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")}
}

// The whole of a binding survives a round trip: which pane was prompted for
// which task, when, how often, and whether review was already announced.
func TestABindingSurvivesARoundTrip(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	want := []decide.Binding{
		{TaskID: "01AAA", Pane: "wM:p9", PromptedAt: at, Prompts: 2, Notified: true},
		{TaskID: "01BBB", Pane: "wM:pA", PromptedAt: at.Add(time.Minute), Prompts: 1},
	}
	if err := s.Save(State{Bindings: want}); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := state.Bindings
	if len(got) != len(want) {
		t.Fatalf("loaded %d bindings, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].TaskID != want[i].TaskID || got[i].Pane != want[i].Pane ||
			got[i].Prompts != want[i].Prompts || got[i].Notified != want[i].Notified ||
			!got[i].PromptedAt.Equal(want[i].PromptedAt) {
			t.Fatalf("binding %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// A store nobody has written yet is an empty set of bindings, not a failure:
// the first start of a daemon finds no file and must carry on.
func TestAStoreThatDoesNotExistLoadsAsEmpty(t *testing.T) {
	got, err := tempStore(t).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 0 || len(got.Reservations) != 0 {
		t.Fatalf("state: %+v", got)
	}
}

// A half-written document must not poison startup. The daemon hears about
// it and starts with nothing rather than refusing to start at all.
func TestATornDocumentIsReportedAndNotFatal(t *testing.T) {
	s := tempStore(t)
	if err := os.WriteFile(s.Path, []byte(`{"version":1,"bindings":[{"task":"01AA`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err == nil {
		t.Fatal("a torn document loaded without a word about it")
	}
	if len(got.Bindings) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
}

// The write is atomic: no reader ever sees a partial document, so nothing
// but a whole file or the previous one is left in the state dir.
func TestSaveLeavesNoPartialFileBehind(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(State{Bindings: []decide.Binding{{TaskID: "01AAA", Pane: "wM:p9"}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Save(State{Bindings: []decide.Binding{{TaskID: "01BBB", Pane: "wM:pA"}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(s.Path) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the state dir holds %v, want only the bindings file", names)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].TaskID != "01BBB" {
		t.Fatalf("the second save did not replace the first: %+v", got)
	}
}

// Saving nothing is how the last binding is dropped, and it must read back
// as nothing rather than as the set before it.
func TestSavingAnEmptySetClearsTheStore(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(State{Bindings: []decide.Binding{{TaskID: "01AAA", Pane: "wM:p9"}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Save(State{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
}

// Timestamps are Unix milliseconds on disk, the one form the plugin contract
// allows a store to hold.
func TestTheStoredTimeIsUnixMilliseconds(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if err := s.Save(State{Bindings: []decide.Binding{{TaskID: "01AAA", Pane: "wM:p9", PromptedAt: at}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := `"prompted_at_ms":1787313600000`
	if !contains(string(b), want) {
		t.Fatalf("want %s in %s", want, b)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The flag that says a submission has had its self-review shot comes back
// with the binding. Without it a restart would shoot the same submission
// again, and one submission earns exactly one.
func TestTheVerificationFlagRoundTrips(t *testing.T) {
	b := &Bindings{Path: filepath.Join(t.TempDir(), "bindings.json")}
	held := []decide.Binding{
		{TaskID: "t1", Pane: "wM:p9", PromptedAt: time.UnixMilli(1_700_000_000_000).UTC(), Prompts: 1, Notified: true, Verified: true, Kind: decide.KindWorker},
		{TaskID: "t2", Pane: "wM:p10", PromptedAt: time.UnixMilli(1_700_000_001_000).UTC(), Prompts: 1},
	}
	if err := b.Save(State{Bindings: held}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("loaded %d bindings", len(got.Bindings))
	}
	if !got.Bindings[0].Verified {
		t.Fatalf("the shot was forgotten: %+v", got.Bindings[0])
	}
	if got.Bindings[1].Verified {
		t.Fatalf("a shot was invented: %+v", got.Bindings[1])
	}
}

// A document written while the verifier lane existed still names verifier
// panes. Nothing drives one now, so the record is debris and comes back as
// nothing rather than as a worker this daemon would start nudging to claim.
func TestAVerifierRecordFromTheOldLaneIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	raw := `{"version":1,"bindings":[` +
		`{"task":"t1","pane":"wM:p9","prompted_at_ms":1700000000000,"prompts":1},` +
		`{"task":"t1","pane":"wM:p10","prompted_at_ms":1700000001000,"prompts":1,"kind":"verifier"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (&Bindings{Path: path}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Pane != "wM:p9" {
		t.Fatalf("read back as %+v", got.Bindings)
	}
}

// A document written before the lane existed has no kind on its rows, and
// every one of them is a worker.
func TestABindingWrittenBeforeTheLaneReadsAsAWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	raw := `{"version":1,"bindings":[{"task":"t1","pane":"wM:p9","prompted_at_ms":1700000000000,"prompts":1,"notified":false}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (&Bindings{Path: path}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Kind != "" {
		t.Fatalf("read back as %+v", got)
	}
}

// A reservation is intent that has not become a worker yet, and the restart
// window between reserving and spawning is exactly where it used to be lost.
// It survives the process, and it carries the daemon that made it.
func TestAReservationSurvivesARoundTripCarryingItsOwner(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	want := State{Reservations: []Reservation{{TaskID: "01AAA", Owner: "plugin:hdis@wM:p1", At: at}}}
	if err := s.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Reservations) != 1 {
		t.Fatalf("reservations: %+v", got.Reservations)
	}
	r := got.Reservations[0]
	if r.TaskID != "01AAA" || r.Owner != "plugin:hdis@wM:p1" || !r.At.Equal(at) {
		t.Fatalf("reservation: %+v", r)
	}
}

// The attempt count is what bounds a spawn that cannot succeed, and a restart
// that read it back as zero would hand the same doomed task a fresh budget on
// every start.
func TestAReservationRemembersHowOftenItsSpawnHasFailed(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	want := State{Reservations: []Reservation{{TaskID: "01AAA", Owner: "plugin:hdis@wM:p1", At: at, Attempts: 2}}}
	if err := s.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Reservations) != 1 || got.Reservations[0].Attempts != 2 {
		t.Fatalf("reservations: %+v", got.Reservations)
	}
}

// Bindings and reservations share one document, written whole, so saving
// either never drops the other.
func TestSavingBindingsKeepsTheReservationsBesideThem(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	err := s.Save(State{
		Bindings:     []decide.Binding{{TaskID: "01AAA", Pane: "wM:p9", PromptedAt: at, Prompts: 1}},
		Reservations: []Reservation{{TaskID: "01BBB", Owner: "plugin:hdis@wM:p1", At: at}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 1 || len(got.Reservations) != 1 {
		t.Fatalf("state: %+v", got)
	}
}

// A checkout is named by its binding and nowhere else. A binding that comes
// back without it leaves the tree with nothing to remove it: the retire
// cannot, and a startup reap would take it while the worker is still
// working in it.
func TestACheckoutSurvivesARoundTrip(t *testing.T) {
	s := tempStore(t)
	held := []decide.Binding{{
		TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker,
		Worktree: "/state/hdis/worktrees/hdis-work-7-abc", PromptedAt: time.Now().UTC(), Prompts: 1,
	}}
	if err := s.Save(State{Bindings: held}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Bindings) != 1 || got.Bindings[0].Worktree != held[0].Worktree {
		t.Fatalf("the checkout was forgotten: %+v", got.Bindings)
	}
}

// The branch a worker's commits live on is remembered beside the checkout
// they were made in: a restart that lost it could not tell a verifier which
// commit was submitted, and could not tell the operator where the work is.
func TestABindingsBranchAndWorktreeBothSurviveARestart(t *testing.T) {
	b := &Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")}
	want := decide.Binding{
		TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker,
		Worktree: "/state/hdis-work-7-abc", Branch: "hdis/task-7",
		PromptedAt: time.UnixMilli(1_700_000_000_000).UTC(), Prompts: 1,
	}
	if err := b.Save(State{Bindings: []decide.Binding{want}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := (&Bindings{Path: b.Path}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(back.Bindings) != 1 {
		t.Fatalf("bindings: %+v", back.Bindings)
	}
	// The kind comes back empty, which is what an absent kind means: the
	// document stays readable to an hdis that predates the verifier lane.
	want.Kind = ""
	if got := back.Bindings[0]; got != want {
		t.Fatalf("the binding came back as %+v, want %+v", got, want)
	}
}

// A restart reads back the profile a worker was launched with. The config may
// have been rewritten since; what is already running is a fact, and the
// document is only what the NEXT spawn reads.
func TestABindingKeepsTheProfileItsWorkerLaunchedWith(t *testing.T) {
	b := &Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")}
	if err := b.Save(State{Bindings: []decide.Binding{{
		TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker, Profile: "heavy",
	}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	held, err := b.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(held.Bindings) != 1 || held.Bindings[0].Profile != "heavy" {
		t.Fatalf("bindings: %+v", held.Bindings)
	}
}

// A task's death count is what keeps a fleet from spending worker after
// worker on a task whose agent cannot stay up, so it has to survive the
// daemon that counted it: a restart that forgot would start the count again
// from nothing and dispatch into the same failure.
func TestATasksDeathCountSurvivesARestart(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(State{Deaths: []Death{
		{TaskID: "01AAA", Project: "/src/p", Count: 2, SinceMS: 1787203459218},
	}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(state.Deaths) != 1 {
		t.Fatalf("loaded %d death records, want 1: %+v", len(state.Deaths), state.Deaths)
	}
	got := state.Deaths[0]
	if got.TaskID != "01AAA" || got.Project != "/src/p" || got.Count != 2 || got.SinceMS != 1787203459218 {
		t.Fatalf("death record came back as %+v", got)
	}
}

// The deaths share the one document with everything else in it, and the
// document is written whole: saving the bindings must not drop the counts
// beside them, or a spawn would clear the record of the spawn before it.
func TestSavingBindingsKeepsTheDeathCountsBesideThem(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(State{
		Bindings: []decide.Binding{{TaskID: "01AAA", Pane: "wM:p9"}},
		Deaths:   []Death{{TaskID: "01BBB", Count: 1}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(state.Bindings) != 1 || len(state.Deaths) != 1 {
		t.Fatalf("loaded %d bindings and %d deaths, want one of each", len(state.Bindings), len(state.Deaths))
	}
}

// A restart reads back BOTH names where a quota moved a spawn down a chain.
// The pane is running one profile and the fleet asked for another, and a
// restart that kept only the first would report a fleet running as configured
// while its first account is still spent.
func TestABindingKeepsTheProfileItWasAskedForAsWellAsTheOneItRan(t *testing.T) {
	b := &Bindings{Path: filepath.Join(t.TempDir(), "dispatch-bindings.json")}
	if err := b.Save(State{Bindings: []decide.Binding{{
		TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker,
		Profile: "spare", AskedProfile: "routed",
	}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	held, err := b.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(held.Bindings) != 1 {
		t.Fatalf("bindings: %+v", held.Bindings)
	}
	if got := held.Bindings[0]; got.Profile != "spare" || got.AskedProfile != "routed" {
		t.Fatalf("the binding came back as profile %q asked %q", got.Profile, got.AskedProfile)
	}
}

// A worker that launched with the profile it was asked for records one name.
// The key is omitted rather than written empty, which is what every binding
// on disk before chains existed reads as.
func TestABindingThatDidNotFallBackWritesNoAskedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch-bindings.json")
	b := &Bindings{Path: path}
	if err := b.Save(State{Bindings: []decide.Binding{{
		TaskID: "01AAA", Pane: "wM:p9", Kind: decide.KindWorker, Profile: "routed",
	}}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "asked_profile") {
		t.Fatalf("a binding that moved nowhere wrote an asked profile: %s", raw)
	}
}
