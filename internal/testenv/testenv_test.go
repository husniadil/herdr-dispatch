package testenv

import (
	"os/exec"
	"strings"
	"testing"
)

// The guarantee the whole gate rests on: inside a fake environment the real
// htask, herdr and proxenos cannot be resolved at all. A verb whose
// stand-in was never written fails as "not found" rather than reaching the
// operator's live board, Herdr server or proxy daemon.
func TestTheRealBinariesAreUnreachable(t *testing.T) {
	New(t)
	for _, name := range []string{"htask", "herdr", "proxenos"} {
		if path, err := exec.LookPath(name); err == nil {
			t.Fatalf("%s still resolves, to %s", name, path)
		}
	}
}

// A stand-in records the argv it was called with, whole, spaces included.
func TestAStandInRecordsItsArgvWhole(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `echo hello`)

	if err := exec.Command("htask", "goal", "7", "--one-line", "with a space").Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	argv := f.Argv(t)
	if len(argv) != 1 {
		t.Fatalf("calls: %v", argv)
	}
	if got, want := argv[0][3], "with a space"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// htask's task verbs are top-level now: `htask get 7`, not `htask task get 7`.
// The real binary still answers the old form as a hidden alias through the
// transition window, and that is exactly why the fake must NOT: a fake that
// answers both would keep passing after the adapter regressed to the dead
// form, and would go on passing after the aliases are deleted upstream. The
// fake is deliberately stricter than the board it stands in for, so the gate
// fails here rather than in production on the day the aliases go.
//
// The `note` group is untouched — it stayed a group, spelled with a space.
func TestTheFakeHtaskRefusesTheOldTaskGroupForm(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `case "$1" in
"get") echo '{"task":{"id":"01AAA"}}' ;;
*) echo '{}' ;;
esac`)

	// The new form reaches the test's own script.
	out, err := exec.Command("htask", "get", "7").CombinedOutput()
	if err != nil {
		t.Fatalf("the flat form must reach the script: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "01AAA") {
		t.Fatalf("the flat form answered %q, not the script's document", out)
	}

	// The old form never reaches it, even though this script's own `*)`
	// branch would have answered it with a document that reads as success.
	out, err = exec.Command("htask", "task", "get", "7").CombinedOutput()
	if err == nil {
		t.Fatalf("the fake answered the dead group form with %q instead of refusing it", out)
	}
	if !strings.Contains(string(out), "task get") {
		t.Fatalf("the refusal must name the form it refused, and said %q", out)
	}
}
