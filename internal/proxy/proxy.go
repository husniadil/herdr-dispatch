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
//
// A non-empty account appends the launcher's own per-session selection, and
// it comes AFTER the eval on purpose: the eval sets ANTHROPIC_AUTH_TOKEN
// itself, so an export placed before it is the one that gets overwritten.
// The daemon reads the tag per turn, and refuses a name its store does not
// hold both at launch and at the turn. An empty account gives back exactly
// the line this returned before the field existed.
//
// The name is not quoted, and nothing here makes it safe: config.AccountNameOK
// is what keeps it to characters that need no quoting, and a document is
// refused at load rather than mangled here.
func (c *Client) EnvCommand(account string) string {
	line := fmt.Sprintf(`eval "$(%s env)"`, c.bin())
	if account == "" {
		return line
	}
	return line + "; export " + AccountEnvVar + "=" + AccountTagPrefix + account
}

// AccountEnvVar and AccountTagPrefix are how the launcher spells a
// per-session account selection. The variable's VALUE is ignored by the
// launcher by design except as this tag, which is why a name rather than a
// secret travels here.
const (
	AccountEnvVar    = "ANTHROPIC_AUTH_TOKEN"
	AccountTagPrefix = "proxenos-account:"
)

// accountsPayload is the shape `accounts list --json` answers with, read for
// the one field doctor names.
type accountsPayload struct {
	Accounts []struct {
		Name string `json:"name"`
	} `json:"accounts"`
}

// Accounts is every account name the launcher's store holds, in the order it
// lists them. doctor checks a configured profile account against it, because
// a name the store does not hold is refused at launch and again at the turn —
// and a spawn dying in its pane is a worse place to learn about a typo than
// `hdis doctor`.
//
// An unreadable store is an error rather than an empty list: "nothing could
// be read" and "the name is not there" are different facts, and doctor
// reports them differently.
func (c *Client) Accounts(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "accounts", "list", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%s: %w", c.bin(), ErrNotInstalled)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s accounts list: %s", c.bin(), msg)
		}
		return nil, fmt.Errorf("%s accounts list: %w", c.bin(), err)
	}
	var payload accountsPayload
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
		return nil, fmt.Errorf("%s accounts list: unreadable json: %w", c.bin(), err)
	}
	names := make([]string, 0, len(payload.Accounts))
	for _, a := range payload.Accounts {
		names = append(names, a.Name)
	}
	return names, nil
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
