//go:build e2e

// Package e2e is layer 3: the SHIPPED `hdis` binary against a REAL `htask`
// built from the sibling repository, over a real socket, with a fake `herdr`.
//
// It is out of `make test-full` on purpose. CI has no herdr-tasks checkout, so
// a gate that ran this would be red on every machine but one; this is run
// before a release, on the machine that cuts it.
//
// Herdr is faked here where the board's own layer 3 uses a real headless
// server. The boundary this layer exists to prove is the one between the two
// PLUGINS — that `htask <verb> --json` really is the integration surface the
// README declares, against the real binary rather than a shell script written
// to agree with the code under test. A real Herdr proves nothing extra about
// that seam, and it would put the operator's own multiplexer one env var away.
//
// Nothing here may reach the operator's live board, Herdr, config or state:
// HOME, the XDG bases, both plugins' own dirs and PATH are all temporary.
package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr answers the calls a dispatch makes before it needs a real
// multiplexer: the §11.2 schema read, an empty world, and a refusal to start
// an agent. The refusal is the point — the reservation is what this layer is
// about, and a worker that came up would need a real terminal.
const fakeHerdr = `case "$1 $2" in
"api schema") echo '{"protocol":1,"requests":["tab.create","tab.list","tab.close","pane.split","pane.run","pane.read","pane.list","pane.close","agent.start","agent.get","agent.list","agent.prompt","notification.show"],"events":["pane_exited"]}' ;;
"pane list") echo '{"id":"x","result":{"type":"pane_list","panes":[]}}' ;;
"tab list") echo '{"id":"x","result":{"type":"tab_list","tabs":[]}}' ;;
"agent list") echo '{"id":"x","result":{"type":"agent_list","agents":[]}}' ;;
"agent start") echo '{"id":"x","error":{"code":"timeout","message":"no terminal in this layer"}}' >&2; exit 1 ;;
*) echo '{"id":"x","result":{"type":"ok"}}' ;;
esac`

// world is one throwaway pair of plugins.
type world struct {
	t       *testing.T
	root    string
	project string
	env     []string
	hdis    string
	htask   string
}

func setup(t *testing.T) *world {
	t.Helper()
	// A unix socket path has a hard length limit in the kernel, and macOS's
	// TMPDIR spends most of it before either daemon gets a name.
	root, err := os.MkdirTemp("/tmp", "hde2e")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })

	w := &world{t: t, root: root}
	w.hdis = buildHdis(t, root)
	w.htask = buildHtask(t, root)

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("bin dir: %v", err)
	}
	write(t, filepath.Join(bin, "herdr"), "#!/bin/sh\n"+fakeHerdr+"\n", 0o755)

	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	for _, d := range []string{home, config} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// A base pane so a dispatch is not refused NO_BASE_PANE, and a profile so
	// the daemon starts. Nothing is ever split off it: the fake herdr refuses
	// to start an agent.
	write(t, filepath.Join(config, "dispatch.toml"),
		"pane = \"wM:p1\"\ndefault = \"worker\"\n\n[profiles.worker]\nprovider = \"claude\"\n", 0o600)

	w.env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+filepath.Dir(w.htask)+string(os.PathListSeparator)+"/usr/bin:/bin",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"DISPATCH_STATE_DIR="+filepath.Join(root, "state"),
		"DISPATCH_CONFIG_DIR="+config,
		"TASKS_STATE_DIR="+filepath.Join(root, "board-state"),
		"TASKS_CONFIG_DIR="+filepath.Join(root, "board-config"),
		"HERDR_BIN_PATH="+filepath.Join(bin, "herdr"),
		// A door derives its pane from its own environment. The shell that
		// ran the test must not lend it one.
		"HERDR_PANE_ID=", "HERDR_TAB_ID=", "HERDR_WORKSPACE_ID=",
		"HERDR_PLUGIN_CONTEXT_JSON=",
	)

	// The board is scoped to a git root, so the task needs a repository to be
	// filed on. It is under the temp root like everything else.
	w.project = filepath.Join(root, "project")
	if err := os.MkdirAll(w.project, 0o755); err != nil {
		t.Fatalf("project: %v", err)
	}
	w.run(t, w.project, "git", "init", "-q", w.project)

	t.Cleanup(func() {
		// Both daemons were started by a door and outlive the call. Stop
		// them before the temp root goes, or they write into a directory
		// that no longer exists and the machine keeps them.
		//
		// Each stop carries w.env, and that is not tidiness. `hdis stop`
		// and `htask stop` find their daemon through the state dir in the
		// environment, so a stop run with the ambient one reaches for the
		// OPERATOR's socket — it would leave this suite's daemons running
		// and try to end the operator's, which is the one thing §12.3
		// forbids absolutely. Measured: without the environment, four of
		// these daemons were left behind by four runs.
		for _, bin := range []string{w.hdis, w.htask} {
			stop := exec.Command(bin, "stop")
			stop.Env, stop.Dir = w.env, w.project
			stop.Run()
		}
	})
	return w
}

// TestADispatchReservesARealBoardsTask is the whole layer: a task created
// through the real `htask` CLI, and `hdis dispatch` taking it, over the
// integration surface the README declares — `htask <verb> --json`, shelled
// out to, never its socket.
func TestADispatchReservesARealBoardsTask(t *testing.T) {
	w := setup(t)

	var created struct {
		Task struct {
			ID    string `json:"id"`
			Seq   int    `json:"seq"`
			Title string `json:"title"`
		} `json:"task"`
	}
	w.json(t, &created, w.htask, "task", "create", "do the e2e thing", "--json")
	if created.Task.ID == "" {
		t.Fatal("the real board created no task")
	}

	// The daemon a door started is already watching the board, so its first
	// tick may reserve this task before the explicit dispatch reaches it.
	// Both answers are correct and which one arrives is a race, so what is
	// asserted is the outcome they share: the task is reserved, and if the
	// dispatch is the one that took it, it answers with the board's own row.
	out, code := w.try(t, w.hdis, "dispatch", created.Task.ID, "--json")
	switch code {
	case 0:
		var got struct {
			Seq   int    `json:"seq"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("dispatch answered %q: %v", out, err)
		}
		if got.Seq != created.Task.Seq || got.Title != created.Task.Title {
			t.Fatalf("dispatch answered %+v for the board's %+v", got, created.Task)
		}
	case 6:
		if !strings.Contains(out, "ALREADY_DISPATCHED") {
			t.Fatalf("dispatch refused CONFLICT for something other than the watching tick: %s", out)
		}
	default:
		t.Fatalf("dispatch exited %d: %s", code, out)
	}

	// §5.8: and the dispatcher remembers it, in the store a reader can open.
	var dumped struct {
		Reservations []struct {
			TaskID string `json:"task"`
		} `json:"reservations"`
	}
	w.json(t, &dumped, w.hdis, "dump", "--json")
	held := false
	for _, r := range dumped.Reservations {
		if r.TaskID == created.Task.ID {
			held = true
		}
	}
	if !held {
		t.Fatalf("the dispatched task is in no reservation: %+v", dumped.Reservations)
	}
}

// A task the board does not have is NOT_FOUND with §6.3's exit status, from
// the real board rather than a script written to say so.
func TestDispatchingATaskTheRealBoardDoesNotHaveIsNotFound(t *testing.T) {
	w := setup(t)
	// One real task, so the board and both daemons are up and the refusal is
	// about this id rather than about an empty world.
	w.run(t, w.project, w.htask, "task", "create", "something else", "--json")

	out, code := w.try(t, w.hdis, "dispatch", "01JZZZZZZZZZZZZZZZZZZZZZZZ", "--json")
	if code != 3 {
		t.Fatalf("dispatch of a task no board has exited %d, want NOT_FOUND's 3: %s", code, out)
	}
	if !strings.Contains(out, `"code":"NOT_FOUND"`) {
		t.Fatalf("the §6.2 envelope does not carry NOT_FOUND: %s", out)
	}
}

func (w *world) json(t *testing.T, into any, name string, args ...string) {
	t.Helper()
	out, code := w.try(t, name, args...)
	if code != 0 {
		t.Fatalf("%s %s exited %d: %s", filepath.Base(name), strings.Join(args, " "), code, out)
	}
	if err := json.Unmarshal([]byte(out), into); err != nil {
		t.Fatalf("%s %s: unreadable answer %q: %v", filepath.Base(name), strings.Join(args, " "), out, err)
	}
}

func (w *world) try(t *testing.T, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env, cmd.Dir = w.env, w.project
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", name, err)
	}
	return strings.TrimSpace(out.String()), code
}

func (w *world) run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env, cmd.Dir = w.env, dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func buildHdis(t *testing.T, root string) string {
	t.Helper()
	out := filepath.Join(root, "hdis")
	build(t, filepath.Join("..", ".."), out, "./cmd/hdis")
	return out
}

// buildHtask builds the real board from the sibling checkout. Its location is
// DISPATCH_E2E_HTASK_SRC, else the sibling directory beside this repository. A
// missing one is a loud SKIP: this layer is not part of the gate, and a
// machine without the board is a machine where it cannot run rather than one
// where it failed.
//
// DISPATCH_E2E_REQUIRED turns that skip into a failure, which is what
// `make release-check` sets. A release must not be cut on a suite that
// silently did not run.
func buildHtask(t *testing.T, root string) string {
	t.Helper()
	src := os.Getenv("DISPATCH_E2E_HTASK_SRC")
	if src == "" {
		here, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		src = filepath.Join(filepath.Dir(here), "herdr-tasks")
	}
	if _, err := os.Stat(filepath.Join(src, "cmd", "htask")); err != nil {
		msg := "no herdr-tasks checkout at %s, so there is no real board to drive: " +
			"clone it beside this repository or set DISPATCH_E2E_HTASK_SRC (%v)"
		if os.Getenv("DISPATCH_E2E_REQUIRED") != "" {
			t.Fatalf(msg, src, err)
		}
		t.Skipf(msg, src, err)
	}
	// Into a directory of its own, because it goes on PATH: `hdis` shells out
	// to `htask` by name, which is the integration surface under test.
	dir := filepath.Join(root, "board")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("board dir: %v", err)
	}
	out := filepath.Join(dir, "htask")
	build(t, src, out, "./cmd/htask")
	return out
}

func build(t *testing.T, dir, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s in %s: %v\n%s", pkg, dir, err, b)
	}
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
