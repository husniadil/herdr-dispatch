// Package worktree is the throwaway checkout a verifier works in.
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

// Manager creates and removes the worktrees verifiers work in.
type Manager struct {
	// Root is the directory worktrees are created under; empty means the
	// system temp directory. It is never inside a project.
	Root string
	// Git is the binary to run; empty means `git` off PATH.
	Git string
}

func (m *Manager) git() string {
	if m.Git != "" {
		return m.Git
	}
	return "git"
}

// Create checks the project out at its committed HEAD in a directory of its
// own and returns that directory.
//
// The checkout is detached on purpose: nothing done in it can move the
// branch the worker's commits live on, and the verifier reads the commit
// that was submitted rather than whatever the shared tree happens to hold.
func (m *Manager) Create(ctx context.Context, project string, seq int) (string, error) {
	root, err := m.run(ctx, project, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("no worktree for %s: %w", project, err)
	}

	if m.Root != "" {
		if err := os.MkdirAll(m.Root, 0o700); err != nil {
			return "", fmt.Errorf("no worktree for %s: %w", project, err)
		}
	}
	dir, err := os.MkdirTemp(m.Root, fmt.Sprintf("hdis-verify-%d-*", seq))
	if err != nil {
		return "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	if _, err := m.run(ctx, root, "worktree", "add", "--detach", dir, "HEAD"); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("no worktree for %s: %w", project, err)
	}
	return dir, nil
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
