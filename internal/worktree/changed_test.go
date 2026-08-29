package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// git runs one command in a directory and fails the test on anything but a
// clean exit.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A checkout nobody has worked in yet has changed nothing.
func TestAnUntouchedCheckoutHasChangedNothing(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	dir, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := m.Changed(context.Background(), src, dir, branch)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an untouched checkout reports %v", got)
	}
}

// What the work touched is what comes back, committed and staged alike, and
// it is measured against the commit the branch was cut from rather than
// whatever the project's HEAD has moved on to since.
func TestChangedIsTheWorkAgainstTheCommitTheBranchWasCutFrom(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	dir, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	write(t, dir, "internal/loop/loop.go", "package loop\n")
	git(t, dir, "add", "internal/loop/loop.go")
	git(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "work")
	write(t, dir, "notes.txt", "still going\n")
	git(t, dir, "add", "notes.txt")

	// The project moves on under the worker, which is the ordinary case.
	write(t, src, "elsewhere.md", "someone else\n")
	git(t, src, "add", "elsewhere.md")
	git(t, src, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "two")

	got, err := m.Changed(context.Background(), src, dir, branch)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	slices.Sort(got)
	want := []string{"internal/loop/loop.go", "notes.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
}

// A file the worker never tracked is not a change: git does not know it, and
// a caller that read it as one would call an untracked deliverable code.
func TestChangedLeavesUntrackedFilesOut(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	dir, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	write(t, dir, "deliverable.txt", "the whole task\n")
	got, err := m.Changed(context.Background(), src, dir, branch)
	if err != nil {
		t.Fatalf("changed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an untracked file was reported as a change: %v", got)
	}
}

// A checkout that is gone, or a branch the repository does not have, is an
// ERROR and never an empty answer: a caller deciding on "nothing changed"
// would be deciding on a question git never answered.
func TestAnUnreadableDiffIsAnError(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	dir, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Changed(context.Background(), src, dir, branch); err == nil {
		t.Fatal("a checkout that is gone answered rather than failed")
	}
	if _, err := m.Changed(context.Background(), src, src, "hdis/task-404"); err == nil {
		t.Fatal("a branch the repository does not have answered rather than failed")
	}
	if _, err := m.Changed(context.Background(), src, "", ""); err == nil {
		t.Fatal("a binding carrying no checkout answered rather than failed")
	}
}
