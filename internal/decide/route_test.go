package decide

import (
	"testing"
	"time"
)

// The routing table as the README carries it.
var routes = []Route{{MinPriority: 3, Profile: "medium"}, {MinPriority: 5, Profile: "heavy"}}

// The band edges, which is where a routing rule is either right or quietly
// off by one: below the lowest minimum, exactly at each minimum, and above
// the highest.
func TestPriorityPicksTheProfileAtEveryBandEdge(t *testing.T) {
	for _, c := range []struct {
		priority int
		want     string
	}{
		{-1, "worker"},
		{0, "worker"},
		{2, "worker"},
		{3, "medium"},
		{4, "medium"},
		{5, "heavy"},
		{9, "heavy"},
	} {
		if got := RouteProfile(c.priority, "worker", routes); got != c.want {
			t.Errorf("priority %d routed to %q, want %q", c.priority, got, c.want)
		}
	}
}

// The highest matching minimum wins whatever order the document wrote them
// in: an operator's table is not sorted, and a rule that took the first match
// would send every high-priority task to the lowest band.
func TestTheHighestMatchingMinimumWinsWhateverTheDocumentOrder(t *testing.T) {
	unsorted := []Route{{MinPriority: 5, Profile: "heavy"}, {MinPriority: 3, Profile: "medium"}}
	if got := RouteProfile(7, "worker", unsorted); got != "heavy" {
		t.Fatalf("priority 7 routed to %q", got)
	}
	if got := RouteProfile(3, "worker", unsorted); got != "medium" {
		t.Fatalf("priority 3 routed to %q", got)
	}
}

// No routes is every config written before this existed, and it behaves
// exactly as it did: the project's own profile, whatever the priority.
func TestWithNoRoutesEveryPriorityKeepsTheDefaultProfile(t *testing.T) {
	for _, priority := range []int{0, 3, 5, 99} {
		if got := RouteProfile(priority, "worker", nil); got != "worker" {
			t.Fatalf("priority %d routed to %q with no routes", priority, got)
		}
	}
}

// The chosen profile is part of the decision's output, so the adapter is told
// which profile to launch rather than resolving one of its own.
func TestASpawnCarriesTheProfileItsPriorityRoutedTo(t *testing.T) {
	now := time.Now()
	s := Snapshot{
		Ready: []string{"hot", "cold"},
		Tasks: map[string]Task{
			"hot":  {ID: "hot", Status: "todo", Project: "/src/p", Priority: 5, Profile: "worker"},
			"cold": {ID: "cold", Status: "todo", Project: "/src/p", Priority: 0, Profile: "worker"},
		},
		Agents: map[string]string{},
		Now:    now,
	}
	p := Policy{MaxWorkers: 2, ClaimTimeout: time.Minute, MaxPrompts: 2, Routes: routes}
	got := map[string]string{}
	for _, a := range Decide(s, p) {
		if a.Kind == Spawn {
			got[a.TaskID] = a.Profile
		}
	}
	if got["hot"] != "heavy" {
		t.Errorf("the priority 5 task spawns with %q", got["hot"])
	}
	if got["cold"] != "worker" {
		t.Errorf("the priority 0 task spawns with %q", got["cold"])
	}
}
