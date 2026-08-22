package htask

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The dispatcher stops at review. It spawns a verifier to READ what was
// submitted, and the judgment stays with the operator: no code path in this
// repo issues `task approve` or `task reject`.
//
// The board adapter is the only way anything here reaches htask, so its
// method set is the whole of what this binary can ask the board for. A review
// verb added anywhere would have to arrive as a method here first.
func TestTheBoardAdapterCarriesNoReviewVerb(t *testing.T) {
	var got []string
	rt := reflect.TypeOf(&Client{})
	for i := 0; i < rt.NumMethod(); i++ {
		got = append(got, rt.Method(i).Name)
	}
	sort.Strings(got)
	// Held and Release are the restart's own pair: the board is the one
	// place a hold this daemon left behind survives the process, and handing
	// back a hold nobody is working is not a verdict on the work. Neither
	// reaches an approve, a reject or a note.
	want := []string{"Doctor", "Get", "Held", "Ready", "Release"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the board adapter's verbs are %v, and the ones this binary may have are %v", got, want)
	}
}

// The same boundary, from the other side: no source file in this repo passes
// "approve" or "reject" as an argument to anything. The verifier's own
// condition names both, in a sentence forbidding them, and that is a
// sentence rather than an argument.
func TestNoSourceFilePassesAReviewVerbAsAnArgument(t *testing.T) {
	root := filepath.Join("..", "..")
	verb := regexp.MustCompile(`"(approve|reject)"`)
	read := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// The root itself is spelled `../..`, whose name starts with a
			// dot: testing the name alone skips the whole walk on its first
			// step and passes without reading a line.
			if name := info.Name(); path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		// Production files only: the boundary is what the shipped binary
		// does, and a test naming a verb to prove it is refused is the
		// boundary being held rather than crossed.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		read++
		if m := verb.Find(body); m != nil {
			t.Errorf("%s passes %s as an argument: this binary never approves or rejects", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// A guard that read nothing passes for the wrong reason, which is
	// exactly how this one passed while its walk stopped on its first step.
	if read < 10 {
		t.Fatalf("the walk read %d source files, so it guarded nothing", read)
	}
}
