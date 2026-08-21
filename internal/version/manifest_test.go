package version

import (
	"os"
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
