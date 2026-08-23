package decide

import "testing"

// ready builds a Ready list and the rows behind it, each task filed on the
// project given beside it.
func ready(pairs ...[2]string) Snapshot {
	s := Snapshot{Tasks: map[string]Task{}, Agents: map[string]string{}, Now: t0}
	for _, p := range pairs {
		s.Ready = append(s.Ready, p[0])
		s.Tasks[p[0]] = Task{ID: p[0], Status: "todo", Project: p[1]}
	}
	return s
}

func spawned(as []Action) []string {
	var ids []string
	for _, a := range as {
		if a.Kind == Spawn {
			ids = append(ids, a.TaskID)
		}
	}
	return ids
}

func TestReadyTasksAreTakenRoundRobinByProjectAndNotInBoardOrder(t *testing.T) {
	// One board's rows arrive first and offer more work than there are
	// slots. Taken in list order it takes both; round-robin gives the
	// second board its first task before the first board gets its second.
	s := ready(
		[2]string{"a1", "alpha"},
		[2]string{"a2", "alpha"},
		[2]string{"a3", "alpha"},
		[2]string{"b1", "beta"},
		[2]string{"b2", "beta"},
	)
	p := pol()
	p.MaxWorkers = 2

	got := spawned(Decide(s, p))
	want := []string{"a1", "b1"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}

func TestASingleProjectMachineSpawnsInExactlyTheOrderTheBoardOffered(t *testing.T) {
	// Nothing to be fair between, so the fairness rule must cost nothing:
	// the order out is the order in.
	s := ready(
		[2]string{"t1", "alpha"},
		[2]string{"t2", "alpha"},
		[2]string{"t3", "alpha"},
		[2]string{"t4", "alpha"},
	)
	p := pol()
	p.MaxWorkers = 3

	got := spawned(Decide(s, p))
	want := []string{"t1", "t2", "t3"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
}
