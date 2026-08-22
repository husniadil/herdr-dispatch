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
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := verb.Find(body); m != nil {
			t.Errorf("%s passes %s as an argument: this binary never approves or rejects", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
