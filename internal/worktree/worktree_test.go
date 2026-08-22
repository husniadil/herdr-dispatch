package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a git repository with one commit and returns its path.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "one"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// A worktree is its own checkout of the project's commit, somewhere else.
func TestCreateGivesAWorktreeOfItsOwnAtTheProjectCommit(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if path == src || strings.HasPrefix(path, src+string(filepath.Separator)) {
		t.Fatalf("the worktree is inside the project it must not touch: %s", path)
	}
	if got, want := head(t, path), head(t, src); got != want {
		t.Fatalf("worktree is at %s, project is at %s", got, want)
	}
	// Detached, so nothing in it can move the project's branch.
	out, err := exec.Command("git", "-C", path, "symbolic-ref", "-q", "HEAD").Output()
	if err == nil {
		t.Fatalf("the worktree is on a branch: %s", out)
	}
}

// Uncommitted work in the project stays in the project. This is the whole
// reason the lane moved out of the shared tree.
func TestCreateDoesNotCarryOrDisturbTheProjectsUncommittedWork(t *testing.T) {
	src := repo(t)
	debris := filepath.Join(src, "mutation.go")
	if err := os.WriteFile(debris, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Root: t.TempDir()}
	path, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(debris); err != nil {
		t.Fatalf("the project's uncommitted file did not survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "mutation.go")); !os.IsNotExist(err) {
		t.Fatalf("the project's uncommitted file leaked into the worktree: %v", err)
	}
}

// Two verifiers at once are two worktrees, never one shared between them.
func TestTwoCreatesDoNotShareADirectory(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	a, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	b, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if a == b {
		t.Fatalf("both verifiers were given %s", a)
	}
}

// A directory that is not a git repository earns a refusal that names it.
func TestCreateRefusesADirectoryThatIsNotARepository(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	plain := t.TempDir()
	path, err := m.Verifier(context.Background(), plain, 7, "HEAD")
	if err == nil {
		t.Fatalf("created %s from a directory that is not a repository", path)
	}
	if !strings.Contains(err.Error(), plain) {
		t.Fatalf("the refusal does not name the directory: %v", err)
	}
}

// Remove takes the directory and git's own record of it.
func TestRemoveLeavesNeitherDirectoryNorRecord(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.Remove(context.Background(), path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the worktree directory outlived its removal: %v", err)
	}
	out, err := exec.Command("git", "-C", src, "worktree", "list").Output()
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if strings.Contains(string(out), path) {
		t.Fatalf("git still records the worktree: %s", out)
	}
}

// A verifier that left work behind is still removed: the whole point is that
// nothing in it is worth keeping.
func TestRemoveTakesAWorktreeWithChangesInIt(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, err := m.Verifier(context.Background(), src, 7, "HEAD")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "mutation.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the worktree directory outlived its removal: %v", err)
	}
}

// Removing what was already removed is not a failure: a binding may be
// dropped twice, and a restart may find the directory gone.
func TestRemoveIsQuietAboutAWorktreeThatIsAlreadyGone(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	if err := m.Remove(context.Background(), filepath.Join(t.TempDir(), "never")); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

// A worker commits, so its checkout is on a branch of its own named for the
// task rather than detached at a commit.
func TestAWorkerGetsAWorktreeOnABranchNamedForItsTask(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if path == src || strings.HasPrefix(path, src+string(filepath.Separator)) {
		t.Fatalf("the worker was given the project directory itself: %s", path)
	}
	if !strings.Contains(branch, "7") {
		t.Fatalf("branch %q is not named for task 7", branch)
	}
	out, err := exec.Command("git", "-C", path, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("the worker's checkout is not on a branch: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != branch {
		t.Fatalf("the checkout is on %q, the binding would record %q", got, branch)
	}
	if got, want := head(t, path), head(t, src); got != want {
		t.Fatalf("the branch starts at %s, the project is at %s", got, want)
	}
}

// Reaping is only safe because the work is not in the directory: removing a
// worker's checkout leaves the branch, and every commit on it, reachable.
func TestRemovingAWorkersWorktreeLeavesTheBranchAndItsCommitsReachable(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "the work"}} {
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	done := head(t, path)

	if err := m.Remove(context.Background(), path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	out, err := exec.Command("git", "-C", src, "rev-parse", branch).Output()
	if err != nil {
		t.Fatalf("the branch went with the directory: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != done {
		t.Fatalf("branch %s is at %s, the work was committed at %s", branch, got, done)
	}
}

// A verifier reads the commit that was SUBMITTED. Now that a worker commits
// on a branch of its own, the project's HEAD is not that commit and reading
// it verifies the wrong tree.
func TestAVerifierDetachesAtTheCommitItIsGivenNotTheProjectsHead(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	work, branch, err := m.Worker(context.Background(), src, 7)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "work.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "the work"}} {
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	submitted := head(t, work)
	if submitted == head(t, src) {
		t.Fatalf("the project moved with the worker, so this test proves nothing")
	}

	path, err := m.Verifier(context.Background(), src, 7, branch)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	if got := head(t, path); got != submitted {
		t.Fatalf("the verifier reads %s; the submitted commit is %s", got, submitted)
	}
	if _, err := os.Stat(filepath.Join(path, "work.go")); err != nil {
		t.Fatalf("the submitted work is not in the verifier's checkout: %v", err)
	}
	// Detached, so nothing the verifier does can move the worker's branch.
	if out, err := exec.Command("git", "-C", path, "symbolic-ref", "-q", "HEAD").Output(); err == nil {
		t.Fatalf("the verifier's checkout is on a branch: %s", out)
	}
}

// No commit to read is no verifier: falling back to the project's HEAD is
// the bug this lane exists to avoid.
func TestVerifierRefusesWhenItIsGivenNoCommitToRead(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	path, err := m.Verifier(context.Background(), src, 7, "")
	if err == nil {
		t.Fatalf("a verifier was given %s with no commit to read", path)
	}
}
