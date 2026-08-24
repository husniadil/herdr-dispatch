package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Usage is the proxy's word about whether the serving account can pay for
// another worker, narrowed to what the spawn gate asks for.
//
// It is read off the document's TOP-LEVEL rollup rather than its accounts
// array: the rollup is already about the account the proxy is serving, which
// is the account a codex worker would spend, so reading it skips picking one.
type Usage struct {
	// Known is whether the account has a ceiling to measure against at all.
	// A metered key does not — it comes back known:false with no windows —
	// and there is nothing there to gate on.
	Known bool
	// LimitReached is the rollup's own flag for an account that cannot pay
	// now.
	LimitReached bool
	// UsedPercent is the highest used_percent across the rollup's windows.
	// The document gives no per-window threshold semantics and may carry
	// more than one window, so the fullest is the one nearest to stopping
	// the account.
	UsedPercent float64
	// Account is the serving account's stored name.
	Account string
	// Plan is the serving account's plan, for a report to print beside the
	// figure. Nothing gates on it.
	Plan string
}

// usagePayload is the shape `usage --json` answers with, read for the rollup
// alone. The accounts array is deliberately not decoded: every field this
// gate needs is at the top level, and picking an account out of the array
// would be re-deciding what the proxy already decided.
type usagePayload struct {
	Known        bool   `json:"known"`
	LimitReached bool   `json:"limit_reached"`
	Plan         string `json:"plan"`
	Serving      struct {
		Account  string `json:"account"`
		Provider string `json:"provider"`
		Plan     string `json:"plan"`
	} `json:"serving"`
	// Windows may be absent entirely, which is what an ungaugeable account
	// answers with. status, label and representative are all nullable and
	// none of them is read: a null status is a normal window, and requiring
	// the string would read most codex windows as abnormal.
	Windows []struct {
		UsedPercent float64 `json:"used_percent"`
	} `json:"windows"`
}

// Usage asks the proxy what the serving account has already spent.
//
// Without --refresh, on purpose: that is the cheap default, and it reports
// what the daemon already holds rather than spending an upstream request. A
// gate that spent a request per tick would cost the quota it exists to
// protect.
func (c *Client) Usage(ctx context.Context) (Usage, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "usage", "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Usage{}, fmt.Errorf("%s: %w", c.bin(), ErrNotInstalled)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Usage{}, fmt.Errorf("%s usage: %s", c.bin(), msg)
		}
		return Usage{}, fmt.Errorf("%s usage: %w", c.bin(), err)
	}
	var payload usagePayload
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &payload); err != nil {
		return Usage{}, fmt.Errorf("%s usage: unreadable json: %w", c.bin(), err)
	}

	u := Usage{
		Known:        payload.Known,
		LimitReached: payload.LimitReached,
		Account:      payload.Serving.Account,
		Plan:         payload.Serving.Plan,
	}
	if u.Plan == "" {
		u.Plan = payload.Plan
	}
	for _, w := range payload.Windows {
		if w.UsedPercent > u.UsedPercent {
			u.UsedPercent = w.UsedPercent
		}
	}
	return u, nil
}
