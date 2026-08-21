package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/decide"
)

func tempStore(t *testing.T) *Bindings {
	t.Helper()
	return &Bindings{Path: filepath.Join(t.TempDir(), "hdis-bindings.json")}
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
	if err := s.Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
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
	if len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
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
	if len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
}

// The write is atomic: no reader ever sees a partial document, so nothing
// but a whole file or the previous one is left in the state dir.
func TestSaveLeavesNoPartialFileBehind(t *testing.T) {
	s := tempStore(t)
	if err := s.Save([]decide.Binding{{TaskID: "01AAA", Pane: "wM:p9"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Save([]decide.Binding{{TaskID: "01BBB", Pane: "wM:pA"}}); err != nil {
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
	if len(got) != 1 || got[0].TaskID != "01BBB" {
		t.Fatalf("the second save did not replace the first: %+v", got)
	}
}

// Saving nothing is how the last binding is dropped, and it must read back
// as nothing rather than as the set before it.
func TestSavingAnEmptySetClearsTheStore(t *testing.T) {
	s := tempStore(t)
	if err := s.Save([]decide.Binding{{TaskID: "01AAA", Pane: "wM:p9"}}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Save(nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("bindings: %+v", got)
	}
}

// Timestamps are Unix milliseconds on disk, the one form the plugin contract
// allows a store to hold.
func TestTheStoredTimeIsUnixMilliseconds(t *testing.T) {
	s := tempStore(t)
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if err := s.Save([]decide.Binding{{TaskID: "01AAA", Pane: "wM:p9", PromptedAt: at}}); err != nil {
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
