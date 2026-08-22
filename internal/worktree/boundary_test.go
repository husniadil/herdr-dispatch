package worktree

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The work is the worker's and the branch is the operator's. hdis creates a
// branch and it removes checkouts; bringing the work home — fast-forward,
// merge, cherry-pick, push — and deleting the branch afterwards are the
// operator's own acts, on their own judgment, after review.
//
// This walks the source the way the board adapter's review-verb guard does:
// a git verb that integrates or discards work must not appear as an argument
// anywhere in this binary.
func TestNoSourceFilePassesAMergePushOrBranchDeleteAsAnArgument(t *testing.T) {
	root := filepath.Join("..", "..")
	verb := regexp.MustCompile(`"(merge|rebase|push|cherry-pick)"`)
	// `git branch -d`/`-D`, however the two arguments are spelled apart.
	kill := regexp.MustCompile(`"branch"[^\n]{0,40}"(-d|-D|--delete)"`)
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
			t.Errorf("%s passes %s as an argument: this binary integrates nothing", path, m)
		}
		if m := kill.Find(body); m != nil {
			t.Errorf("%s deletes a branch: %s", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// A guard that read nothing passes for the wrong reason.
	if read < 10 {
		t.Fatalf("the walk read %d source files, so it guarded nothing", read)
	}
}
