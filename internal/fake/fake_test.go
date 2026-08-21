package fake

import (
	"os/exec"
	"testing"
)

// The guarantee the whole gate rests on: inside a fake environment the real
// htask, herdr and codex-cc-proxy cannot be resolved at all. A verb whose
// stand-in was never written fails as "not found" rather than reaching the
// operator's live board, Herdr server or proxy daemon.
func TestTheRealBinariesAreUnreachable(t *testing.T) {
	New(t)
	for _, name := range []string{"htask", "herdr", "codex-cc-proxy"} {
		if path, err := exec.LookPath(name); err == nil {
			t.Fatalf("%s still resolves, to %s", name, path)
		}
	}
}

// A stand-in records the argv it was called with, whole, spaces included.
func TestAStandInRecordsItsArgvWhole(t *testing.T) {
	f := New(t)
	f.Bin(t, "htask", `echo hello`)

	if err := exec.Command("htask", "task", "goal", "7", "--one-line", "with a space").Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	argv := f.Argv(t)
	if len(argv) != 1 {
		t.Fatalf("calls: %v", argv)
	}
	if got, want := argv[0][4], "with a space"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
