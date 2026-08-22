// Package worktree is the checkout an agent this dispatcher spawns works in.
//
// The verification lane used to run in the project directory itself, the one
// its worker still holds and the operator reviews in, and the first live run
// of the lane broke both ways at once: the verifier restored the tree from
// HEAD and destroyed the operator's uncommitted mutation, then reported a
// gate result it had measured on that mutation rather than on the commit
// under review. A gate run means nothing when the tree is not the commit.
//
// So a verifier gets a checkout nobody else is holding: a detached git
// worktree of the project at its committed HEAD, outside the project
// directory, removed when the binding that owns it is dropped. Uncommitted
// work stays where it was, because a worktree carries none of it.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Prefix is what every directory this package creates is named with. A
// restart reaps by it: a directory under the root carrying it is one hdis
// made, and anything else under there is not hdis's to remove.
const Prefix = "hdis-"

// WorkPrefix names a worker's checkout and VerifyPrefix a verifier's. Both
// carry Prefix, so one reap covers both lanes.
const (
	WorkPrefix   = Prefix + "work-"
	VerifyPrefix = Prefix + "verify-"
)

// Branch is the branch a worker's checkout is put on: named for the task, so
// an operator reading `git branch` sees which task each one carries.
func Branch(seq int) string { return fmt.Sprintf("hdis/task-%d", seq) }

// Manager creates and removes the worktrees verifiers work in.
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

// Verifier checks the SUBMITTED commit out in a directory of its own and
// returns it. The commit is named by the caller — the branch the worker
// committed on — because the project's own HEAD is not what was submitted
// once a worker stopped committing to it, and a gate run means nothing when
// the tree is not the commit under review.
//
// The checkout is detached on purpose: nothing done in it can move the
// branch the worker's commits live on.
func (m *Manager) Verifier(ctx context.Context, project string, seq int, commit string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("no worktree for %s: nothing names the commit that was submitted", project)
	}
	root, dir, err := m.prepare(ctx, project, VerifyPrefix, seq)
	if err != nil {
		return "", err
	}
	if _, err := m.run(ctx, root, "worktree", "add", "--detach", dir, commit); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	return dir, nil
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
	// --force because the verifier is meant to have dirtied it: making one
	// mutation and showing it caught is the job.
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
