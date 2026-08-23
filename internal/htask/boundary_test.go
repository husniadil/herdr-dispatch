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
	// GetIn is Get scoped to one project: the read a pane's number needs,
	// because a number is unique only inside one. It reads a row and no
	// more, exactly as Get does.
	want := []string{"Doctor", "Get", "GetIn", "Held", "Ready", "Release"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the board adapter's verbs are %v, and the ones this binary may have are %v", got, want)
	}
}

// The same boundary, from the other side: no source file in this repo passes
// "approve" or "reject" as an argument to anything. The verifier's own
// condition names both, in a sentence forbidding them, and that is a
// sentence rather than an argument.
//
// One exemption, and only one: `hdis parked resolve --reject` closes a
// PARKED ACTION this daemon deferred to the operator. That is not a verdict
// on a board submission — no task changes state, the board is never called,
// and the row it closes exists only in this binary's own document. The word
// is the board's, but the thing it rules on is ours, and the sibling plugins
// spell the same operator verdict `--reject`, so diverging here would cost a
// caller for nothing. What the guard still holds is the shape of the
// exemption: exactly the two files below, exactly one occurrence in each,
// and `"approve"` nowhere at all. A third occurrence, or the word appearing
// in a third file, fails and gets read by a human.
func TestNoSourceFilePassesAReviewVerbAsAnArgument(t *testing.T) {
	root := filepath.Join("..", "..")
	exempt := map[string]int{
		// The switch's declaration in the one verb table both doors render.
		filepath.Join(root, "internal", "verbs", "verbs.go"): 1,
		// Where the daemon reads that switch off the request.
		filepath.Join(root, "internal", "daemon", "daemon.go"): 1,
	}
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
		found := verb.FindAll(body, -1)
		if allowed, ok := exempt[path]; ok {
			// The exemption is for `"reject"` and nothing else, and it
			// is spent by the first occurrence: anything past it, and
			// every `"approve"`, is the boundary being crossed.
			rejects := 0
			for _, m := range found {
				if string(m) == `"reject"` {
					rejects++
					if rejects <= allowed {
						continue
					}
				}
				t.Errorf("%s passes %s as an argument: this binary never approves or rejects, and its %d exempt %q may not grow", path, m, allowed, `"reject"`)
			}
			return nil
		}
		if len(found) > 0 {
			t.Errorf("%s passes %s as an argument: this binary never approves or rejects", path, found[0])
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

// Every board call is spawned in one place, and that place scrubs the pane.
//
// The method-set pin above forces a human decision when a new board verb
// arrives, but it says nothing about HOW the new verb spawns: a second
// exec site added the obvious way inherits the daemon's environment, and
// the call goes back to declaring a plugin principal while carrying a pane.
// Pinning that there is exactly ONE spawn, and that it sets cmd.Env, is what
// makes the scrub cover verbs nobody has written yet.
func TestEveryBoardCallGoesThroughTheOneScrubbedSpawn(t *testing.T) {
	var spawns int
	var scrubbed bool
	var read int
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		read++
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "exec.Command") {
				continue
			}
			spawns++
			// The scrub is the assignment that follows the spawn.
			for _, next := range lines[i+1 : min(i+4, len(lines))] {
				if strings.Contains(next, "cmd.Env = envWithoutPane(") {
					scrubbed = true
				}
			}
		}
	}
	if read == 0 {
		t.Fatal("scanned no source files; the scan is broken, not the boundary")
	}
	if spawns != 1 {
		t.Fatalf("the board adapter spawns htask in %d places; every board call goes through one, so the pane is scrubbed once and for every verb", spawns)
	}
	if !scrubbed {
		t.Fatal("the one spawn does not set cmd.Env = envWithoutPane(...); a board call that inherits the daemon's environment declares a plugin principal and carries a pane anyway")
	}
}
