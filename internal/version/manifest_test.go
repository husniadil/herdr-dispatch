package version

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The manifest is what Herdr installs, and nothing in the build reads it, so
// it drifts silently unless something holds it to the binary it describes.
func TestTheManifestNamesThisPluginAtThisVersion(t *testing.T) {
	m := manifest(t)
	if got := field(m, "id"); got != "herdr-dispatch" {
		t.Errorf("manifest id %q, want herdr-dispatch", got)
	}
	if got := field(m, "version"); got != Version {
		t.Errorf("manifest version %q, want %q, the version the binary prints", got, Version)
	}
}

// Herdr starts the plugin through [[startup]] and has no shutdown hook, so
// stop and restart are the only route to turning the daemon off.
func TestTheManifestCarriesStartupStopAndRestart(t *testing.T) {
	m := manifest(t)
	if !strings.Contains(m, "[[build]]") {
		t.Error("the manifest has no [[build]] section, so an install compiles nothing")
	}
	if !strings.Contains(m, "[[startup]]") {
		t.Error("the manifest has no [[startup]] section, so nothing starts the daemon")
	}
	for _, id := range []string{"stop", "restart"} {
		if !regexp.MustCompile(`(?m)^id = "` + id + `"$`).MatchString(m) {
			t.Errorf("the manifest declares no action %q", id)
		}
	}
	for _, script := range []string{"start.sh", "stop.sh", "restart.sh"} {
		if _, err := os.Stat("../../scripts/" + script); err != nil {
			t.Errorf("scripts/%s: %v", script, err)
		}
	}
}

func manifest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../herdr-plugin.toml")
	if err != nil {
		t.Fatalf("the manifest Herdr installs is missing: %v", err)
	}
	return string(b)
}

// field reads a top-level key. The manifest's top table comes first, so the
// first match is the plugin's own value rather than a section's.
func field(m, key string) string {
	match := regexp.MustCompile(`(?m)^` + key + ` = "([^"]*)"$`).FindStringSubmatch(m)
	if match == nil {
		return ""
	}
	return match[1]
}

// §13.4: a plugin declares the contract revision it satisfies, and a reader
// comparing three sibling binaries should get that answer from the same verb
// on each. `hdis version` printed the plugin version alone while both siblings
// printed the plugin name and the revision beside it, so the one binary whose
// declaration had never been swept was also the one that made you run a second
// verb to see it.
//
// The binary is built and run rather than the strings compared: what a reader
// gets is stdout, and a constant that never reaches it declares nothing.
func TestVersionPrintsTheContractBesideTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "hdis")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/hdis")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("hdis version: %v", err)
	}
	line := strings.TrimSpace(string(out))
	for what, want := range map[string]string{
		"the plugin version":   Version,
		"the contract (§13.4)": Contract,
		"which plugin it is":   "herdr-dispatch",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("`hdis version` does not print %s: %q", what, line)
		}
	}

	raw, err := exec.Command(bin, "version", "--json").Output()
	if err != nil {
		t.Fatalf("hdis version --json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("--json did not answer a document: %q", raw)
	}
	for k, want := range map[string]string{
		"version": Version, "contract": Contract, "plugin": "herdr-dispatch",
	} {
		if got[k] != want {
			t.Errorf("--json %s = %q, want %q", k, got[k], want)
		}
	}
}

// An absence is not a decision until it is written down, and a comment nothing
// reads is exactly what a later edit drops without noticing. The manifest
// declares no [[panes]] on purpose: the questions a pane would answer are
// already answered by Herdr's sidebar, the board, and `hdis status`. Pin both
// halves, because a bare "No [[panes]]." with the reason gone is the drift.
func TestTheManifestWritesDownWhyThereIsNoPane(t *testing.T) {
	m := manifest(t)
	if regexp.MustCompile(`(?m)^\[\[panes\]\]$`).MatchString(m) {
		t.Fatal("the manifest declares a [[panes]] section, so the comment saying there is none is now a lie")
	}
	if !strings.Contains(m, "# No [[panes]].") {
		t.Fatal("the manifest omits [[panes]] and says nothing about why")
	}
	for _, reason := range []string{"hdis status", "base_pane", "max_workers", "pending", "workers"} {
		if !strings.Contains(m, reason) {
			t.Errorf("the [[panes]] absence does not name %q as what answers instead", reason)
		}
	}
}
