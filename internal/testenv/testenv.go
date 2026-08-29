// Package testenv puts stand-in `htask`, `herdr` and `proxenos` binaries
// on PATH for a test. Every case in this repo that shells out answers its own
// calls this way: the operator's live board, herdr server and proxy daemon are
// never reachable from a test, and the gate needs none of them installed.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RunIsolated runs one package's tests with the operator's own home, state and
// config directories replaced by a temporary one, and returns the code
// TestMain should exit with:
//
//	func TestMain(m *testing.M) { os.Exit(testenv.RunIsolated(m)) }
//
// It is TestMain rather than a per-test helper because the directory a case
// writes is not always a directory the case names. config.StateDir() reads the
// environment wherever it is called from, so a spawn that mentions no path at
// all still resolves the operator's own — measured on 2026-08-29, where a unit
// test's default worker MCP document landed in the live
// ~/.local/state/dispatch/worker.mcp.json naming a `htask` inside a t.TempDir
// that no longer existed, and every worker spawned on that machine afterwards
// had a dead tasks door.
//
// The whole home moves rather than only the two XDG names, because the fallback
// under them is the home directory itself. config.StateDir refuses to resolve
// under the real home inside a test binary, so a package that reaches it
// without this fails loud instead of writing.
func RunIsolated(m *testing.M) int {
	dir, err := os.MkdirTemp("", "hdis-test-home-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: no temporary home, so the operator's own would be used: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	for name, value := range map[string]string{
		"HOME":            dir,
		"XDG_STATE_HOME":  filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
	} {
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintf(os.Stderr, "testenv: %s could not be moved off the operator's own: %v\n", name, err)
			return 1
		}
	}
	return m.Run()
}

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

// HerdrSchemaFile is where a test writes the schema its fake `herdr` should
// answer `api schema --json` with, to stand up a Herdr that is missing a
// capability. Without it the fake answers DefaultHerdrSchema.
const HerdrSchemaFile = "schema.json"

// DefaultHerdrSchema is every request this binary asks Herdr for, in the flat
// form §11.2 requires a reader to accept. It is the fake's answer unless a
// test writes HerdrSchemaFile, so a case that is not about feature detection
// does not have to know feature detection exists.
const DefaultHerdrSchema = `{"protocol":1,"requests":[` +
	`"tab.create","tab.list","tab.close",` +
	`"pane.split","pane.send_input","pane.read","pane.list","pane.close","pane.send_keys",` +
	`"agent.start","agent.get","agent.list","agent.prompt",` +
	`"notification.show"],"events":["pane_exited"]}`

// herdrSchemaPrelude answers `herdr api schema --json` ahead of whatever the
// test's own script does. §11.2 makes that read part of every Herdr client, so
// every fake `herdr` has to answer it — writing the case into each of the
// twenty-odd scripts by hand would mean a new script silently failing feature
// detection instead of failing the thing it was written for.
const herdrSchemaPrelude = `if [ "$1 $2" = "api schema" ] && [ -z "${HDIS_FAKE_NO_SCHEMA:-}" ]; then
  if [ -f "$HDIS_FAKE_DIR/` + HerdrSchemaFile + `" ]; then cat "$HDIS_FAKE_DIR/` + HerdrSchemaFile + `"; else echo '` + DefaultHerdrSchema + `'; fi
  exit 0
fi
`

// NoHerdrSchema stops the fake `herdr` answering `api schema` ahead of the
// test's own script, so a case can stand up a Herdr that cannot be asked what
// it offers at all.
func NoHerdrSchema(t *testing.T) { t.Setenv("HDIS_FAKE_NO_SCHEMA", "1") }

// htaskFlatPrelude refuses the dead `htask task <verb>` group ahead of
// whatever the test's own script does. htask hoisted those verbs to the top
// level, and the real binary still answers the old spelling as a HIDDEN ALIAS
// for one transition window — which is precisely why no fake may. A fake that
// answered both would keep passing after this adapter regressed to the dead
// form, and would keep passing right up to the day the aliases are deleted
// upstream, at which point the failure lands in production instead of the
// gate. Most fake htask scripts end in a permissive `*)` that answers an
// unknown verb with `{}`, so without this the old form reads as SUCCESS.
//
// Only the `task` group. `note` stayed a group, spelled with a space, and
// `htask note add` is a live form that must pass straight through.
const htaskFlatPrelude = `if [ "$1" = task ]; then
  echo "htask: unknown command \"task\" for \"htask\": the task verbs are top-level, so this is '$2', not 'task $2'" >&2
  exit 1
fi
`

// Bin writes an executable /bin/sh script under the given name. The script
// runs with $HDIS_FAKE_DIR set, and "$@" carries the argv it was called with.
func (f *Fake) Bin(t *testing.T, name, script string) {
	t.Helper()
	path := filepath.Join(f.Dir, name)
	if name == "herdr" {
		script = herdrSchemaPrelude + script
	}
	if name == "htask" {
		script = htaskFlatPrelude + script
	}
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
