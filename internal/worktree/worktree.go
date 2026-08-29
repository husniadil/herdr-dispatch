// Package worktree is the checkout an agent this dispatcher spawns works in.
//
// A worker gets a checkout nobody else is holding: a git worktree of the
// project on a branch named for the task, outside the project directory,
// removed when the binding that owns it is dropped. The project directory —
// the one the operator sits in and every other worker would otherwise hold —
// is the one place a worker must never run: two workers in that tree is how
// one task's commit swept up another task's uncommitted work.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Prefix is what every directory this package creates is named with. A
// restart reaps by it: a directory under the root carrying it is one hdis
// made, and anything else under there is not hdis's to remove.
const Prefix = "hdis-"

// WorkPrefix names a worker's checkout. It carries Prefix, so the reap covers
// it.
const WorkPrefix = Prefix + "work-"

// Branch is the branch a worker's checkout is put on: named for the task, so
// an operator reading `git branch` sees which task each one carries.
func Branch(seq int) string { return fmt.Sprintf("hdis/task-%d", seq) }

// Manager creates and removes the worktrees workers work in.
type Manager struct {
	// Root is the directory worktrees are created under; empty means the
	// system temp directory. It is never inside a project.
	Root string
	// Git is the binary to run; empty means `git` off PATH.
	Git string
}

// RootDir is where this manager creates its checkouts. The reap is bounded
// by it, and it is a method rather than the field so a caller can hold a
// manager behind an interface.
func (m *Manager) RootDir() string { return m.Root }

func (m *Manager) git() string {
	if m.Git != "" {
		return m.Git
	}
	return "git"
}

// Worker checks the project out in a directory of its own, on a branch named
// for the task, and returns both.
//
// A worker commits, so it needs somewhere its commits can live: a branch,
// created at the project's current HEAD. Removing the directory later leaves
// the branch and everything committed on it reachable from the project,
// which is what makes reaping a worker's checkout safe.
//
// A task dispatched again — a rejection reworked in a new pane — continues
// the branch it already has rather than starting a second one.
func (m *Manager) Worker(ctx context.Context, project string, seq int) (string, string, error) {
	root, dir, err := m.prepare(ctx, project, WorkPrefix, seq)
	if err != nil {
		return "", "", err
	}
	branch := Branch(seq)
	args := []string{"worktree", "add", "-b", branch, dir, "HEAD"}
	if _, err := m.run(ctx, root, "rev-parse", "--verify", "--quiet", branch); err == nil {
		args = []string{"worktree", "add", dir, branch}
	}
	if _, err := m.run(ctx, root, args...); err != nil {
		os.RemoveAll(dir)
		return "", "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	return dir, branch, nil
}

// Behind reports whether the project's HEAD has moved past the branch: true
// when HEAD is no longer reachable from the branch, which is exactly the
// state `git merge --ff-only <branch>` refuses.
//
// It is measured against the project's CURRENT HEAD rather than the commit
// the branch was cut from, because HEAD is what the operator merges into and
// it is the only side of the comparison that moves after a spawn.
//
// A branch the repository does not have is an error, never a quiet false: an
// operator reading "not behind" for a branch that does not exist is worse
// off than one told the question could not be answered.
func (m *Manager) Behind(ctx context.Context, project, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, fmt.Errorf("no base for %s: %w", branch, err)
	}
	if _, err := m.run(ctx, root, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		return false, fmt.Errorf("no base for %s: the repository has no such branch", branch)
	}
	cmd := exec.CommandContext(ctx, m.git(), "-C", root, "merge-base", "--is-ancestor", "HEAD", branch)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		// git says 1 for "not an ancestor", which is the answer rather than
		// a failure. Anything else is git itself refusing, and reading that
		// as "behind" would invent a fact.
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return true, nil
		}
		return false, fmt.Errorf("no base for %s: %w", branch, err)
	}
	return false, nil
}

// Unmoved reports the signature of an isolation escape: a branch that was
// already handed to a worker, carries nothing the project does not already
// have, while the project's HEAD has moved past the point it was cut from.
//
// Read together those two facts say one thing. A checkout was made for this
// task and the work that followed did not land on its branch, while
// SOMETHING landed on the project's own. Three occurrences on 2026-08-24/25
// had exactly that shape, each with the commit on the shared checkout's main
// and the task's branch untouched.
//
// Neither half means it alone. A branch carrying nothing while the project
// stands still is every first spawn. A project that moved past a branch
// carrying its own commits is an ordinary rework, which Behind already
// reports for the merge.
//
// A branch the repository does not have is a first spawn whose branch does
// not exist yet, and it answers false rather than failing: there is nothing
// to judge before the checkout is made. A git that cannot answer is an
// error, never a quiet true, because a true here refuses a spawn.
func (m *Manager) Unmoved(ctx context.Context, project, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, fmt.Errorf("no base for %s: %w", branch, err)
	}
	if _, err := m.run(ctx, root, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		return false, nil
	}
	carried, err := m.run(ctx, root, "rev-list", "--count", branch, "^HEAD")
	if err != nil {
		return false, fmt.Errorf("no base for %s: %w", branch, err)
	}
	if carried != "0" {
		return false, nil
	}
	return m.Behind(ctx, root, branch)
}

// Holder is the checkout that currently has a branch checked out, or empty
// when no checkout has it.
//
// git refuses a second `worktree add` on a branch some other worktree already
// holds, and the refusal names the directory in its own prose. This asks git's
// RECORD instead — `worktree list --porcelain`, in the project's repository —
// because the error text is git's to reword and the record is the fact.
//
// A repository that cannot be asked is an error, never a quiet empty: an empty
// answer means the branch is free, and a spawn that believes a false one walks
// straight into the refusal this exists to avoid.
func (m *Manager) Holder(ctx context.Context, project, branch string) (string, error) {
	if branch == "" {
		return "", nil
	}
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("nothing can say what holds %s: %w", branch, err)
	}
	out, err := m.run(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("nothing can say what holds %s: %w", branch, err)
	}
	// The porcelain form is one paragraph per checkout: a `worktree <path>`
	// line first, and a `branch <ref>` line for a checkout that is on one.
	want := "refs/heads/" + branch
	dir := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			dir = path
			continue
		}
		if ref, ok := strings.CutPrefix(line, "branch "); ok && ref == want {
			return dir, nil
		}
	}
	return "", nil
}

// prepare finds the project's repository root and makes the empty directory
// its checkout goes in.
func (m *Manager) prepare(ctx context.Context, project, prefix string, seq int) (string, string, error) {
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	if m.Root != "" {
		if err := os.MkdirAll(m.Root, 0o700); err != nil {
			return "", "", fmt.Errorf("no worktree for %s: %w", project, err)
		}
	}
	dir, err := os.MkdirTemp(m.Root, fmt.Sprintf(prefix+"%d-*", seq))
	if err != nil {
		return "", "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	return root, dir, nil
}

// Remove takes the worktree directory and git's own record of it. A
// directory that is already gone is not a failure: a binding can be dropped
// twice, and a restart can find one removed by hand.
func (m *Manager) Remove(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	// --force because a worker is meant to have dirtied it: it edits and
	// stages, and its commits are on the branch, which outlives the tree.
	_, err := m.run(ctx, dir, "worktree", "remove", "--force", dir)
	if err == nil {
		return nil
	}
	// git refused, so the directory is still ours to take. Removing it
	// leaves a stale record behind, which prune is for.
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		return fmt.Errorf("worktree %s: %w (and it could not be removed: %v)", dir, err, rmErr)
	}
	return fmt.Errorf("worktree %s: %w (the directory was removed anyway; `git worktree prune` clears the record)", dir, err)
}

func (m *Manager) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, m.git(), append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr == "" {
			stderr = err.Error()
		}
		return "", fmt.Errorf("git %s in %s: %s", strings.Join(args, " "), dir, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// Project is the repository a directory belongs to, which for a worktree is
// the repository it was cut from and not the worktree itself.
//
// git's common directory is what draws that line: every worktree of a
// repository shares one, and it sits inside the repository the worktree came
// from. A checkout under this daemon's state dir, a detached one, and a pane
// still sitting in the project directory all answer with the same path,
// which is what makes this the one way to name a pane's project that does
// not go stale the next time the lanes move.
func (m *Manager) Project(ctx context.Context, dir string) (string, error) {
	common, err := m.run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("no project for %s: %w", dir, err)
	}
	root, err := m.run(ctx, filepath.Dir(common), "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("no project for %s: %w", dir, err)
	}
	return root, nil
}

// Changed is the tracked paths a task's work has touched: the branch's
// checkout compared with the commit that branch was cut from, committed and
// uncommitted alike.
//
// The base is the merge base of the project's HEAD and the branch rather than
// the project's HEAD itself, because the project moves under a worker: a
// checkout cut yesterday and a project that has taken three commits since
// would otherwise report every one of them as this task's work.
//
// Untracked files are not in it, and that is git's own line rather than a
// choice made here: `git diff` compares what git knows about. A caller reading
// "no tracked change" as "no code" is reading exactly what it says.
//
// It fails rather than answering empty whenever git cannot answer — a checkout
// that is gone, a branch the repository does not have, a directory that is not
// a repository at all. An empty answer is a fact about the work, and inventing
// one from a failure would put a decision on evidence nobody read.
func (m *Manager) Changed(ctx context.Context, project, dir, branch string) ([]string, error) {
	if dir == "" || branch == "" {
		return nil, fmt.Errorf("no diff for %q on %q: the binding carries no checkout", dir, branch)
	}
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("no diff for %s: %w", branch, err)
	}
	base, err := m.run(ctx, root, "merge-base", "HEAD", branch)
	if err != nil {
		return nil, fmt.Errorf("no diff for %s: %w", branch, err)
	}
	out, err := m.run(ctx, dir, "diff", "--name-only", base)
	if err != nil {
		return nil, fmt.Errorf("no diff for %s: %w", branch, err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}
