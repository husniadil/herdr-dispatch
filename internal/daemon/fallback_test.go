package daemon

import (
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// chainDaemon is codexDaemon with a chain rather than one profile, so doctor
// has something to report under profiles.
func chainDaemon(t *testing.T, doc, usage string) (*Daemon, *testenv.Fake) {
	t.Helper()
	d, f := newDaemon(t)
	cfg, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Config = cfg
	d.Loop.Policy.MaxUsedPercent = cfg.Proxy.MaxUsedPercent
	f.Bin(t, "proxenos", `case "$1" in
  status) echo '{"auth":{"account":"work-codex"}}' ;;
  usage) echo '`+usage+`' ;;
  accounts) echo '{"accounts":[{"account":"work-codex"},{"account":"spare-codex"}]}' ;;
esac`)
	return d, f
}

const doctorChainDoc = `default = "routed"
[proxy]
max_used_percent = 90
[profiles.routed]
provider = "codex"
account = "work-codex"
fallback = "spare"
[profiles.spare]
provider = "codex"
account = "spare-codex"
fallback = "plain"
[profiles.plain]
provider = "claude"
`

const spentWorkFreeSpare = `{"known":true,"limit_reached":true,"serving":{"account":"work-codex","plan":"team"},"windows":[{"used_percent":100.0}],
"accounts":[{"account":"work-codex","known":true,"limit_reached":true,"windows":[{"used_percent":100.0}]},
{"account":"spare-codex","known":true,"limit_reached":false,"windows":[{"used_percent":11.0}]}]}`

// A chain is invisible otherwise: an operator whose fleet is quietly running
// everything through the second profile has no other way to see that the
// first one's account is spent.
func TestDoctorReportsAFallbackChainAndWhichStepWouldLaunch(t *testing.T) {
	stateDir(t)
	d, _ := chainDaemon(t, doctorChainDoc, spentWorkFreeSpare)

	rep := doctorOf(t, d)
	// Every profile that names a fallback gets a line, not only the one the
	// default routes to: a route can send a task to any of them, so each is
	// a head an operator may be reading.
	if len(rep.Profiles) != 2 {
		t.Fatalf("profiles: %+v, want a line for routed and one for spare", rep.Profiles)
	}
	if rep.Profiles[1].Profile != "spare" {
		t.Fatalf("the lines are not in profile-name order: %+v", rep.Profiles)
	}
	h := rep.Profiles[0]
	if h.Profile != "routed" || h.Fallback != "spare" {
		t.Fatalf("the chain is headed %q -> %q", h.Profile, h.Fallback)
	}
	if len(h.Chain) != 3 {
		t.Fatalf("the chain walks %+v, want three steps", h.Chain)
	}
	// Each step's own account, and whether it spends the proxy's quota at
	// all: the claude end does not, which is what makes it always eligible.
	if h.Chain[0].Account != "work-codex" || h.Chain[0].Refusal == "" {
		t.Fatalf("the spent step reads %+v", h.Chain[0])
	}
	if h.Chain[1].Account != "spare-codex" || h.Chain[1].Refusal != "" {
		t.Fatalf("the step with room reads %+v", h.Chain[1])
	}
	if h.Chain[2].Gated {
		t.Fatalf("a claude step is reported as gated on the proxy: %+v", h.Chain[2])
	}
	// The answer the chain exists to give.
	if h.Launches != "spare" {
		t.Fatalf("doctor says a spawn launches through %q, want spare", h.Launches)
	}
}

// A fleet whose profiles name no fallback has nothing to report, and it gets
// an empty list rather than an absent field.
func TestDoctorReportsNoChainForAFleetThatNamesNoFallback(t *testing.T) {
	stateDir(t)
	d, _ := codexDaemon(t, `{"known":true,"limit_reached":false,"serving":{"account":"work-codex"},"windows":[{"used_percent":4.0}]}`)

	rep := doctorOf(t, d)
	if rep.Profiles == nil {
		t.Fatal("profiles came back absent, want an empty list")
	}
	if len(rep.Profiles) != 0 {
		t.Fatalf("profiles: %+v, want nothing for a fleet naming no fallback", rep.Profiles)
	}
}

// With every codex step spent, the claude end is what would launch — and a
// chain with no claude end has nothing left, which doctor says by naming no
// step at all.
func TestDoctorSaysWhenNoStepOfAChainCouldLaunch(t *testing.T) {
	stateDir(t)
	d, _ := chainDaemon(t, `default = "routed"
[proxy]
max_used_percent = 90
[profiles.routed]
provider = "codex"
account = "work-codex"
fallback = "spare"
[profiles.spare]
provider = "codex"
account = "spare-codex"
`, `{"known":true,"limit_reached":true,"serving":{"account":"work-codex"},"windows":[{"used_percent":100.0}],
"accounts":[{"account":"work-codex","known":true,"limit_reached":true,"windows":[{"used_percent":100.0}]},
{"account":"spare-codex","known":true,"limit_reached":false,"windows":[{"used_percent":95.0}]}]}`)

	rep := doctorOf(t, d)
	if len(rep.Profiles) != 1 {
		t.Fatalf("profiles: %+v", rep.Profiles)
	}
	if got := rep.Profiles[0].Launches; got != "" {
		t.Fatalf("doctor says a spawn launches through %q with every step at quota", got)
	}
	for i, step := range rep.Profiles[0].Chain {
		if step.Refusal == "" {
			t.Fatalf("step %d reads as clear with its account spent: %+v", i, step)
		}
	}
}
