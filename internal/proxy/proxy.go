// Package proxy is the codex provider's launcher, and nothing more. The
// routing itself belongs to the launcher binary, which the config names: this
// package runs the two steps that binary documents — the environment half
// through the pane's shell, the settings half through the worker's argv — and
// carries no routing logic of its own.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/husniadil/herdr-dispatch/internal/config"
)

// Client runs the proxy launcher's CLI.
type Client struct {
	// Bin is the binary to run; empty means config.DefaultProxy off PATH.
	Bin string
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return config.DefaultProxy
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
			return "", fmt.Errorf("%s settings: %s", c.bin(), msg)
		}
		return "", fmt.Errorf("%s settings: %w", c.bin(), err)
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, bytes.TrimSpace(stdout.Bytes())); err != nil {
		return "", fmt.Errorf("%s settings: unreadable json: %w", c.bin(), err)
	}
	return compact.String(), nil
}

// ErrNotInstalled is what Status gives when the launcher binary cannot be
// resolved at all. A proxy nobody installed is a different fact from one that
// is down, and doctor reports them differently: the first affects nothing
// until a codex profile is launched, the second breaks one that is.
var ErrNotInstalled = errors.New("not installed")

// Status is the proxy's own report of itself, narrowed to what doctor asks
// for. The daemon prints more; nothing here decides on the rest.
type Status struct {
	// Account is the stored account the proxy is currently routing through.
	Account string
}

// statusPayload is the shape `status --json` answers with, read for the one
// field doctor names.
type statusPayload struct {
	Auth struct {
		Account string `json:"account"`
	} `json:"auth"`
}

// Status reports whether the proxy is up, and through which account. A daemon
// that is down answers in its own words — they name the socket and say how to
// start it — and those words are what the caller gets, unswallowed.
func (c *Client) Status(ctx context.Context) (Status, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "status", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Status{}, fmt.Errorf("%s: %w", c.bin(), ErrNotInstalled)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Status{}, fmt.Errorf("%s status: %s", c.bin(), msg)
		}
		return Status{}, fmt.Errorf("%s status: %w", c.bin(), err)
	}
	var payload statusPayload
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
		return Status{}, fmt.Errorf("%s status: unreadable json: %w", c.bin(), err)
	}
	return Status{Account: payload.Auth.Account}, nil
}
