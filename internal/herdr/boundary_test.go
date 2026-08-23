package herdr

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sources is every Go file this repository ships, tests excluded: what a
// boundary rule is about is the code that runs, and a test naming a forbidden
// word in a comment is how the rule gets removed for the wrong reason.
func sources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(out) < 10 {
		t.Fatalf("walked %d source files; the walk found nothing to check", len(out))
	}
	return out
}

// §1.1 Herdr is the only terminal substrate. A plugin MUST NOT drive tmux, zmx
// or any other multiplexer, and MUST NOT spawn PTYs of its own.
//
// Everything this binary does to a terminal goes through `herdr <verb>`: pane
// split, agent start, agent prompt, pane read. A second substrate underneath
// that is a second opinion about what a pane IS, and the dispatcher's whole
// model — herdr's agent_status is the only truth about a worker — rests on
// there not being one.
//
// It walks the source rather than the imports, because a multiplexer is
// reachable by exec as easily as by import and the exec path is the one a
// plugin drifts into.
func TestNoSourceFileDrivesATerminalBesidesHerdr(t *testing.T) {
	forbidden := regexp.MustCompile(`\b(tmux|zmx|screen -[SX]|creack/pty|os\.OpenFile\("/dev/pt)`)
	for path, body := range sources(t) {
		if m := forbidden.FindString(body); m != "" {
			t.Errorf("%s reaches for %q; §1.1 makes Herdr the only terminal substrate", path, m)
		}
	}
}

// §1.2 Herdr is the agent registry. A plugin MUST NOT maintain a registry of
// its own.
//
// The bindings this daemon persists are the one thing Herdr cannot answer:
// which task a pane was prompted for. They are keyed BY the pane and hold no
// agent facts — no name, no harness, no status, no session — because every one
// of those is Herdr's word, read fresh each tick and never written down. A
// binding that started carrying them would be a second register of agents that
// goes stale the moment Herdr's changes.
func TestTheBindingsAreNoRegisterOfAgents(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "decide", "decide.go"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(body), "type Binding struct {")
	if start < 0 {
		t.Fatal("no Binding type; this test no longer knows what it is reading")
	}
	rest := string(body)[start:]
	fields := rest[:strings.Index(rest, "\n}")]
	for _, held := range []string{"Name", "Harness", "Status", "Session", "AgentStatus", "Cwd"} {
		if regexp.MustCompile(`(?m)^\t` + held + `\s`).MatchString(fields) {
			t.Errorf("a binding holds %s, which is Herdr's word about an agent; §1.2 leaves the registry to Herdr", held)
		}
	}
}
