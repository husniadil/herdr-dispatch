package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// §6.1 with §3.2 and §4.2: the three sibling binaries present the same flag
// shape, and the four the contract names are the ones a caller writes without
// looking up which plugin they are talking to. They are persistent flags on
// the root, so every verb takes them and none of them declares them itself.
func TestEveryVerbTakesTheFourContractGlobals(t *testing.T) {
	root := Root(nil)
	for _, name := range []string{"json", "project", "all-projects", "as"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s is not a global on the root", name)
		}
	}
	for _, v := range verbs.All {
		cmd := find(t, root, v.CLI)
		for _, name := range []string{"json", "project", "all-projects", "as"} {
			if cmd.InheritedFlags().Lookup(name) == nil {
				t.Errorf("hdis %s does not take --%s", strings.Join(v.CLI, " "), name)
			}
		}
	}
}

// Before cobra there was one usage block for the whole binary, so the only way
// to learn what a verb took was to run it wrong. Every verb now answers --help
// with its own arguments and the long description the MCP door already had.
func TestEveryVerbAnswersItsOwnHelp(t *testing.T) {
	for _, v := range verbs.All {
		root := Root(nil)
		out, err := execute(root, append(append([]string{}, v.CLI...), "--help"))
		if err != nil {
			t.Fatalf("hdis %s --help: %v", strings.Join(v.CLI, " "), err)
		}
		if !strings.Contains(out, v.Short) {
			t.Errorf("hdis %s --help does not carry its own summary:\n%s", strings.Join(v.CLI, " "), out)
		}
		for _, a := range v.Args {
			if !strings.Contains(out, a.Name) {
				t.Errorf("hdis %s --help does not name its %s argument:\n%s",
					strings.Join(v.CLI, " "), a.Name, out)
			}
		}
	}
}

// Completion is what a caller gets from cobra and could not get from the
// hand-written usage. It is listed rather than hidden, because a shell hook
// nobody can discover is one nobody installs.
func TestTheBinaryOffersShellCompletion(t *testing.T) {
	root := Root(nil)
	cmd := find(t, root, []string{"completion"})
	if cmd.Hidden {
		t.Error("the completion command is hidden, so nothing tells an operator it exists")
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if find(t, cmd, []string{shell}) == nil {
			t.Errorf("no completion for %s", shell)
		}
	}
}

// §4.2: an explicit --project is resolved to the §4.1 canonical project, so
// the daemon is handed a path rather than the caller's relative one, whose
// meaning depends on a working directory the daemon does not share.
func TestAnExplicitProjectIsResolvedBeforeItIsSent(t *testing.T) {
	dir := repo(t)
	req, _, err := Request(verb(t, "dispatch"), []string{"--project", dir, "7"})
	if err != nil {
		t.Fatalf("dispatch --project: %v", err)
	}
	if req.Project != dir {
		t.Fatalf("project = %q, want %q", req.Project, dir)
	}
	if req.AllProjects {
		t.Error("--project named one board and the request still asks for every board")
	}
}

// The dispatcher drives every board a worker could be wanted on, so
// --all-projects is what it already does and the flag is the explicit
// spelling of it. Naming one board and every board in the same call is a
// contradiction rather than a precedence puzzle for the daemon to solve.
func TestNamingOneProjectAndEveryProjectIsRefused(t *testing.T) {
	_, _, err := Request(verb(t, "dispatch"), []string{"--project", repo(t), "--all-projects", "7"})
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
		t.Fatalf("both scopes at once = %v (%q), want %q", err, got, want)
	}
}

// With no scope flag the dispatcher reads every board, which is what it did
// before the flag existed and what a fleet-wide daemon has to keep doing.
func TestTheDefaultScopeIsEveryBoard(t *testing.T) {
	req, _, err := Request(verb(t, "dispatch"), []string{"7"})
	if err != nil {
		t.Fatalf("dispatch 7: %v", err)
	}
	if req.Project != "" || !req.AllProjects {
		t.Fatalf("the default scope is not every board: %+v", req)
	}
}

// §4.4 names `parked.list` the one list verb that takes no --all-projects,
// and here the flag selects no reading the verb does not already have: a call
// that names no board reads every board. A flag that selects nothing is
// refused where it is still a flag, rather than accepted and quietly ignored.
func TestParkedListRefusesEveryBoard(t *testing.T) {
	_, _, err := Request(verb(t, "parked.list"), []string{"--all-projects"})
	if got, want := codes.Of(err), codes.Usage; got != want {
		t.Fatalf("hdis parked list --all-projects = %v (%s), want %s", err, got, want)
	}
	// And the default is untouched. Every board is what this daemon answers
	// with when no scope is named, so a refusal read off the RESOLVED request
	// would refuse `hdis parked list` itself: by then the flag and the default
	// are the same request.
	req, _, err := Request(verb(t, "parked.list"), nil)
	if err != nil {
		t.Fatalf("hdis parked list: %v", err)
	}
	if req.Project != "" {
		t.Fatalf("parked list narrowed itself to %q", req.Project)
	}
}

// §3.2: a principal is derived from the calling process, never declared, and
// the ONE exception is a call that wants to act as cron, trigger or plugin.
// So --as takes those three kinds and refuses the two that are derived: a
// caller that could rename itself `agent:<someone else's pane>` would be
// declaring the very fact the rule exists to keep underived.
func TestAsDeclaresOnlyThePrincipalsThatMayBeDeclared(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "wM:p4")
	for _, ok := range []string{"cron:nightly", "trigger:webhook-1", "plugin:hdis"} {
		req, _, err := Request(verb(t, "dispatch"), []string{"--as", ok, "7"})
		if err != nil {
			t.Fatalf("--as %s: %v", ok, err)
		}
		if req.As != ok {
			t.Fatalf("--as %s: request carries %q", ok, req.As)
		}
		if got := req.Caller(); got != ok {
			t.Errorf("--as %s: the caller is recorded as %q", ok, got)
		}
	}
	for _, bad := range []string{"agent:wZ:p9", "human", "none", "cron", "nightly", "wizard:merlin"} {
		if _, _, err := Request(verb(t, "dispatch"), []string{"--as", bad, "7"}); codes.ReasonOf(err) != codes.Invalid {
			t.Errorf("--as %s was accepted, and it is not a principal this door may be handed", bad)
		}
	}
}

// Without --as the principal is the pane the door runs in, exactly as before.
func TestWithoutAsThePrincipalIsStillTheProcessesOwnPane(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "wM:p4")
	req, _, err := Request(verb(t, "dispatch"), []string{"7"})
	if err != nil {
		t.Fatalf("dispatch 7: %v", err)
	}
	if got := req.Caller(); got != "agent:wM:p4" {
		t.Fatalf("caller = %q", got)
	}
}

// §3.6 and §3.7: a CLI invocation is one process per call, so the argv that
// ran IS the deliberate human act §3.7 asks a paneless `human` to point at.
// The CLI door therefore says so on every request, and a paneless call is the
// operator where a paneless server door nobody declared is not. The pane still
// wins (§3.2), and so does an explicit --as.
func TestAPanelessCLIInvocationIsTheOperator(t *testing.T) {
	for name, tc := range map[string]struct {
		pane string
		argv []string
		want string
	}{
		"outside a pane":           {"", []string{"7"}, "human"},
		"inside a pane":            {"wM:p4", []string{"7"}, "agent:wM:p4"},
		"outside a pane with --as": {"", []string{"--as", "cron:nightly", "7"}, "cron:nightly"},
		"inside a pane with --as":  {"wM:p4", []string{"--as", "cron:nightly", "7"}, "cron:nightly"},
	} {
		t.Setenv("HERDR_PANE_ID", tc.pane)
		req, _, err := Request(verb(t, "dispatch"), tc.argv)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !req.Operator {
			t.Errorf("%s: the CLI door sent no human act for its own argv", name)
		}
		if got := req.Caller(); got != tc.want {
			t.Errorf("%s: caller = %q, want %q", name, got, tc.want)
		}
	}
}

// cobra returns help for a parent that is not Runnable BEFORE it validates
// arguments, so NoArgs alone is unreachable on a group: `hdis parked nonsense`
// would read as "no subcommand given", print help on stdout and exit 0, where
// §6.2 and §6.3 promise one document and a failure status. The sibling hit
// this and fixed it with RunE+NoArgs; this door carries the same fix.
func TestAStrayArgumentToAGroupIsARefusalAndNotItsHelp(t *testing.T) {
	root := Root(nil)
	if _, err := execute(root, []string{"parked", "nonsense"}); err == nil {
		t.Fatal("hdis parked nonsense printed help and succeeded")
	}
	root = Root(nil)
	out, err := execute(root, []string{"parked"})
	if err != nil {
		t.Fatalf("hdis parked: %v", err)
	}
	if !strings.Contains(out, "list") || !strings.Contains(out, "resolve") {
		t.Fatalf("hdis parked does not list its subcommands:\n%s", out)
	}
}

// An unknown flag is a caller input error, which §6.3 fixes at USAGE. Before
// cobra it was the flag package's own message and exit 1.
func TestAnUnknownFlagIsAUsageRefusal(t *testing.T) {
	_, _, err := Request(verb(t, "status"), []string{"--wat"})
	if got, want := codes.ReasonOf(err), codes.Invalid; got != want {
		t.Fatalf("--wat = %v (%q), want %q", err, got, want)
	}
}

// find walks a command path from a root, failing when a step of it is missing.
func find(t *testing.T, from *cobra.Command, path []string) *cobra.Command {
	t.Helper()
	at := from
	for _, want := range path {
		next, _, err := at.Find([]string{want})
		if err != nil || next == at || next.Name() != want {
			t.Fatalf("no command %q under %q", want, at.Name())
		}
		at = next
	}
	return at
}

// execute runs one argv through a root and returns what it printed, with both
// streams pointed at the same buffer: a test asserting on help does not care
// which stream cobra chose for it.
func execute(root *cobra.Command, argv []string) (string, error) {
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(argv)
	err := root.Execute()
	return out.String(), err
}

// repo is a real git repository, because §4.1 resolves a project by asking git
// and a fake path would only prove the flag was copied.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// The canonical path, because §4.1 resolves symlinks and macOS hands out
	// a /var temp dir that is one.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return real
}
