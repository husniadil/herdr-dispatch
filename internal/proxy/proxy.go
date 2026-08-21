// Package proxy is the codex provider's launcher, and nothing more. The
// routing itself belongs to codex-cc-proxy: this package runs the two steps
// the proxy documents — the environment half through the pane's shell, the
// settings half through the worker's argv — and carries no routing logic of
// its own.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Client runs the codex-cc-proxy CLI.
type Client struct {
	// Bin is the binary to run; empty means `codex-cc-proxy` off PATH.
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "codex-cc-proxy"
}

// EnvCommand is the shell line that exports the routing environment into a
// worker pane before its agent starts.
func (c *Client) EnvCommand() string {
	return fmt.Sprintf(`eval "$(%s env)"`, c.bin())
}

// Settings returns the client-policy half of the launch, compacted to a
// single line because herdr refuses an agent argument containing a newline.
// This is step zero of a codex spawn: when the daemon is down it fails here,
// in the daemon's own words, rather than as a startup timeout with the cause
// hidden in the pane.
func (c *Client) Settings(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "settings")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("codex-cc-proxy settings: %s", msg)
		}
		return "", fmt.Errorf("codex-cc-proxy settings: %w", err)
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSpace(stdout.Bytes())); err != nil {
		return "", fmt.Errorf("codex-cc-proxy settings: unreadable json: %w", err)
	}
	return compact.String(), nil
}
