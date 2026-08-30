package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/client"
	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/daemon"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
)

// managedState is a state dir a service manager has marked and no daemon is
// listening in.
func managedState(t *testing.T, manager string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(config.EnvPrefix+"STATE_DIR", dir)
	t.Setenv(config.EnvPrefix+"CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, config.ManagedFile), []byte(manager+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// doctor is the one call with an answer when there is no daemon to ask, and
// under a marker that answer is who owns the daemon rather than a refusal:
// an operator told only NOT_RUNNING goes diagnosing a socket.
func TestDoctorAnswersWithTheManagerWhenAManagedDaemonIsDown(t *testing.T) {
	managedState(t, "dev.herdr.hdis")

	// Bin is a binary that exits at once, so a start that was not refused
	// would fail loudly rather than leave a daemon behind.
	raw, err := Ask(&client.Client{Bin: "/usr/bin/true"}, protocol.Request{Verb: "doctor", Door: Door})
	if err != nil {
		t.Fatalf("doctor against a managed daemon that is down: %v", err)
	}
	var rep daemon.DoctorReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("doctor json: %v", err)
	}
	if rep.DaemonAnswering {
		t.Error("the report says a daemon answered, and none is listening")
	}
	if got, want := rep.Managed, "dev.herdr.hdis"; got != want {
		t.Errorf("managed = %q, want %q", got, want)
	}
	if got, want := rep.Socket, config.SocketPath(); got != want {
		t.Errorf("socket = %q, want %q", got, want)
	}

	var out bytes.Buffer
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("printing the managed report: %v", err)
	}
	for what, want := range map[string]string{
		"the manager": "dev.herdr.hdis",
		"the marker":  config.ManagedPath(),
		"the daemon":  "not answering",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the doctor line does not carry %s (looked for %q):\n%s", what, want, out.String())
		}
	}
}

// Every other verb is refused, and the refusal is the envelope §6.2 fixes.
func TestEveryOtherVerbIsRefusedUnderTheMarker(t *testing.T) {
	managedState(t, "dev.herdr.hdis")

	_, err := Ask(&client.Client{Bin: "/usr/bin/true"}, protocol.Request{Verb: "status", Door: Door})
	if err == nil {
		t.Fatal("status answered with no daemon and the marker in place")
	}
	if got, want := codes.Of(err), codes.Conflict; got != want {
		t.Errorf("status = %v (%q), want %q", err, got, want)
	}
	var out bytes.Buffer
	if err := WriteError(err, &out); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("failure envelope: %v", err)
	}
	if got, want := envelope.Error.Code, string(codes.Conflict); got != want {
		t.Errorf("envelope code = %q, want %q", got, want)
	}
	if !strings.Contains(envelope.Error.Message, "dev.herdr.hdis") {
		t.Errorf("the envelope does not name the manager: %q", envelope.Error.Message)
	}
}

// A daemon that IS answering prints its own report, marker or none: the
// managed line is one more fact, not a different document.
func TestALiveReportStillPrintsEverythingItAlwaysDid(t *testing.T) {
	managedState(t, "dev.herdr.hdis")

	raw, err := json.Marshal(daemon.DoctorReport{
		Version: "0.10.3", Contract: "0.10.1", Socket: config.SocketPath(),
		Managed: "dev.herdr.hdis", DaemonAnswering: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Write("doctor", raw, false, &out); err != nil {
		t.Fatalf("printing a live report: %v", err)
	}
	for _, want := range []string{"hdis 0.10.3 on ", "contract", "managed     dev.herdr.hdis"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the live report lost %q:\n%s", want, out.String())
		}
	}
}
