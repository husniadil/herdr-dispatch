package spawn

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The dispatcher publishes an address; it does not carry the mail. hmail is
// the worker's tool, named inside a condition this binary composes, and that
// is the whole of the relationship: no file here imports herdr-mail, reads
// its store, or runs its binary.
//
// The sender of anything a worker then writes is stamped by the mail daemon
// from the pane the worker runs in, so nothing this repo publishes could
// forge one even if it tried.
// The variable's NAME is the whole contract with a spawned agent: it reads
// what this constant spells, and README is where it is told what to read. A
// rename that README does not follow leaves the document lying about the one
// thing an agent cannot guess, so the name is pinned in one direction only —
// derived from the constant, never written out a second time here.
func TestReadmeDocumentsTheDispatcherVariableByItsRealName(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	found := regexp.MustCompile(`HDIS_[A-Z_]+`).FindAllString(string(body), -1)
	if len(found) == 0 {
		t.Fatalf("README documents no HDIS_ variable, and an agent has no other way to learn %s", DispatcherPaneVar)
	}
	for _, name := range found {
		if name != DispatcherPaneVar {
			t.Errorf("README tells an agent to read %s, and the pane is launched with %s", name, DispatcherPaneVar)
		}
	}
}

func TestNoSourceFileReachesIntoHerdrMail(t *testing.T) {
	root := filepath.Join("..", "..")
	// An import of the mail repo, a read of its state directory, or hmail
	// passed as a program to run — never as a word inside a condition.
	reach := regexp.MustCompile(`herdr-mail|"hmail"|exec\.Command\(\s*"?hmail`)
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
		if m := reach.Find(body); m != nil {
			t.Errorf("%s reaches into herdr-mail with %s: this binary publishes an address and sends no mail", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
