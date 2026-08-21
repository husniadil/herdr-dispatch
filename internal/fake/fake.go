// Package fake puts stand-in `htask`, `herdr` and `codex-cc-proxy` binaries
// on PATH for a test. Every case in this repo that shells out answers its own
// calls this way: the operator's live board, herdr server and proxy daemon are
// never reachable from a test, and the gate needs none of them installed.
package fake

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake is one directory of stand-in binaries, first on PATH for the test that
// made it. Scripts find it again as $HDIS_FAKE_DIR.
type Fake struct{ Dir string }

// New creates the directory and makes it the only place a test can resolve a
// binary from, apart from the system directories the scripts themselves need.
// Replacing PATH rather than prepending to it is the point: a verb whose fake
// was never written fails as "not found" instead of quietly reaching the
// operator's real herdr, htask or proxy daemon.
func New(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", dir+sep+"/usr/bin"+sep+"/bin")
	t.Setenv("HDIS_FAKE_DIR", dir)
	return &Fake{Dir: dir}
}

// Bin writes an executable /bin/sh script under the given name. The script
// runs with $HDIS_FAKE_DIR set, and "$@" carries the argv it was called with.
func (f *Fake) Bin(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(f.Dir, name)
	body := "#!/bin/sh\nprintf '%s\\037' \"$@\" >> \"$HDIS_FAKE_DIR/calls.log\"\nprintf '\\n' >> \"$HDIS_FAKE_DIR/calls.log\"\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// Argv returns the argument vector of every call to every fake binary, in
// order and with each argument whole, spaces included.
func (f *Fake) Argv(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
	}
	return out
}

// Calls returns the same calls with each argv joined by a space, which reads
// better in an assertion when no argument contains one.
func (f *Fake) Calls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, argv := range f.Argv(t) {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// Path names a file inside the fake's directory, for a script that needs to
// keep a counter or a canned document between calls.
func (f *Fake) Path(name string) string { return filepath.Join(f.Dir, name) }

// Write puts a file in the fake's directory for a script to read.
func (f *Fake) Write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(f.Path(name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
