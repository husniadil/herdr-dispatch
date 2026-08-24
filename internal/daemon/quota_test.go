package daemon

import (
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// codexDaemon is the standard daemon with a codex profile and a threshold, so
// the quota gate applies to the lane its workers launch on. usage is what the
// fake proxy answers `usage --json` with.
func codexDaemon(t *testing.T, usage string) (*Daemon, *testenv.Fake) {
	t.Helper()
	d, f := newDaemon(t)
	cfg, err := config.Parse([]byte(`default = "worker"
[proxy]
max_used_percent = 90
[profiles.worker]
provider = "codex"
`))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg
	d.Loop.Policy.MaxUsedPercent = cfg.Proxy.MaxUsedPercent
	f.Bin(t, "proxenos", `case "$1" in
  status) echo '{"auth":{"account":"work-codex"}}' ;;
  usage) echo '`+usage+`' ;;
esac`)
	return d, f
}

// The reachability section said whether the proxy answers; this says whether
// the account behind it can pay. Both are what an operator asks doctor when
// nothing is spawning.
func TestDoctorReportsWhatTheAccountHasSpent(t *testing.T) {
	stateDir(t)
	d, _ := codexDaemon(t, `{"known":true,"limit_reached":false,"serving":{"account":"work-codex","plan":"team"},"windows":[{"used_percent":63.5}]}`)

	rep := doctorOf(t, d)
	q := rep.Proxy.Quota
	if !q.Known {
		t.Fatalf("quota: %+v", q)
	}
	if got, want := q.UsedPercent, 63.5; got != want {
		t.Fatalf("used_percent: got %v, want %v", got, want)
	}
	if got, want := q.MaxUsedPercent, 90; got != want {
		t.Fatalf("threshold: got %d, want %d", got, want)
	}
	if q.LimitReached || q.Refusal != "" {
		t.Fatalf("an account with room reads as refusing: %+v", q)
	}
	if got, want := q.Plan, "team"; got != want {
		t.Fatalf("plan: got %q, want %q", got, want)
	}
}

// Doctor's job here: an operator whose fleet has stopped reads the refusal in
// the same words a dispatch would give them, without having to try one.
func TestDoctorCarriesTheQuotaRefusalThatWouldStopASpawn(t *testing.T) {
	stateDir(t)
	d, _ := codexDaemon(t, `{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}]}`)

	q := doctorOf(t, d).Proxy.Quota
	if !q.LimitReached {
		t.Fatalf("quota: %+v", q)
	}
	if !strings.Contains(q.Refusal, "work-codex") || !strings.Contains(q.Refusal, "limit") {
		t.Fatalf("refusal: %q", q.Refusal)
	}
}

// A metered key has no ceiling. Reporting it as a zero-percent account would
// tell an operator a number that means nothing.
func TestDoctorSaysWhenThereIsNoQuotaToRead(t *testing.T) {
	stateDir(t)
	d, _ := codexDaemon(t, `{"known":false,"reason":"metered","serving":{"account":"openai-api"}}`)

	q := doctorOf(t, d).Proxy.Quota
	if q.Known {
		t.Fatalf("a metered account read as gaugeable: %+v", q)
	}
	if q.Refusal != "" {
		t.Fatalf("an unknown quota refused a spawn: %q", q.Refusal)
	}
}

// The claude path never touches the proxy, so there is no account to report
// and doctor asks for none.
func TestDoctorReadsNoQuotaWhenNoProfileLaunchesThroughTheProxy(t *testing.T) {
	stateDir(t)
	d, f := newDaemon(t)
	f.Bin(t, "proxenos", `case "$1" in
  status) echo '{"auth":{"account":"work-codex"}}' ;;
  usage) echo '{"known":true,"limit_reached":true,"serving":{"account":"work-codex"}}' ;;
esac`)

	rep := doctorOf(t, d)
	if rep.Proxy.Quota.Known || rep.Proxy.Quota.Refusal != "" {
		t.Fatalf("a claude-only fleet reported a quota: %+v", rep.Proxy.Quota)
	}
	var asked int
	for _, c := range f.Calls(t) {
		if strings.HasPrefix(c, "usage") {
			asked++
		}
	}
	if asked != 0 {
		t.Fatalf("doctor asked the proxy about a quota nothing spends: %d calls", asked)
	}
}
