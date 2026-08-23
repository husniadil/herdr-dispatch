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

// Which project a directory belongs to is git's own answer, and a worktree
// answers with the repository it was cut from rather than with itself. That
// is what lets a pane be named on the board by the number its agent carries:
// a number is unique inside a project, and this is the project.
func TestProjectNamesTheRepositoryACheckoutWasCutFrom(t *testing.T) {
	project := repo(t)
	m := &Manager{Root: t.TempDir()}
	work, _, err := m.Worker(context.Background(), project, 7)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	verify, err := m.Verifier(context.Background(), project, 7, head(t, project))
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	for _, dir := range []string{work, verify, project, filepath.Join(project, ".git")} {
		got, err := m.Project(context.Background(), dir)
		if err != nil {
			t.Fatalf("project of %s: %v", dir, err)
		}
		if got != resolved(t, project) {
			t.Fatalf("project of %s is %q, want %q", dir, got, resolved(t, project))
		}
	}
}

// A directory that is no repository names no project, and says so rather
// than answering with something a board would then be asked about.
func TestProjectRefusesADirectoryThatIsNotARepository(t *testing.T) {
	m := &Manager{}
	if got, err := m.Project(context.Background(), t.TempDir()); err == nil {
		t.Fatalf("a directory that is no repository named the project %q", got)
	}
}

// resolved is the path with symlinks taken out, which is what git prints.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return out
}

// Behind is the one fact the operator is missing at merge time: a branch cut
// from a HEAD the project has since moved past cannot be fast-forwarded.
// True and false both matter, so this pins both against the same repository.
func TestBehindIsTrueOnlyOnceTheProjectHeadHasMovedPastTheBranch(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	ctx := context.Background()
	if _, _, err := m.Worker(ctx, src, 7); err != nil {
		t.Fatalf("worker: %v", err)
	}
	branch := Branch(7)

	behind, err := m.Behind(ctx, src, branch)
	if err != nil {
		t.Fatalf("behind: %v", err)
	}
	if behind {
		t.Fatalf("a branch cut from the project's own HEAD reads as behind")
	}

	commit(t, src, "two")
	behind, err = m.Behind(ctx, src, branch)
	if err != nil {
		t.Fatalf("behind after the project moved: %v", err)
	}
	if !behind {
		t.Fatalf("the project moved past the branch and nothing said so")
	}
}

// A name no branch carries is a failure to report, never a quiet false: the
// operator would read "not behind" for a branch that does not exist.
func TestBehindRefusesABranchTheRepositoryDoesNotHave(t *testing.T) {
	src := repo(t)
	m := &Manager{Root: t.TempDir()}
	if _, err := m.Behind(context.Background(), src, "hdis/task-999"); err == nil {
		t.Fatalf("a branch that does not exist answered instead of failing")
	}
}

// commit puts one more empty commit on the repository's checked-out branch.
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", msg).CombinedOutput()
	if err != nil {
		t.Fatalf("commit in %s: %v: %s", dir, err, out)
	}
}
