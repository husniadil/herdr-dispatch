package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// read is `events` through the same Handle both doors reach.
func read(t *testing.T, d *Daemon, args map[string]any) EventsReport {
	t.Helper()
	raw, err := d.Handle(context.Background(), protocol.Request{Verb: "events", Args: args})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var rep EventsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("events: %v", err)
	}
	return rep
}

// The verb answers the trail the daemon's own work wrote, on the door both
// surfaces reach.
func TestEventsAnswersTheTrail(t *testing.T) {
	d, _ := newDaemon(t)
	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rep := read(t, d, nil)
	if rep.Count != 1 || rep.Events[0].Name != "dispatch.task.reserved" {
		t.Fatalf("events answered %+v", rep)
	}
}

// A consumer that resumes from the last id it saw is handed what came after
// it and never that event again.
func TestEventsResumesAfterAnID(t *testing.T) {
	d, _ := newDaemon(t)
	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	first := read(t, d, nil).Events[0]
	if rep := read(t, d, map[string]any{"since": first.ID}); rep.Count != 0 {
		t.Fatalf("resuming from the last event handed back %+v", rep.Events)
	}
}

// An id the trail no longer carries is refused under a contract code rather
// than answered with the whole window, which a resuming consumer would take
// for the tail of its own stream.
func TestEventsRefusesAnIDTheTrailHasRotatedPast(t *testing.T) {
	d, _ := newDaemon(t)
	_, err := d.Handle(context.Background(), protocol.Request{
		Verb: "events", Args: map[string]any{"since": "ev-0000000000001-deadbeef"}})
	if err == nil {
		t.Fatal("an unknown since id was accepted")
	}
	if got := codes.Of(err); got != codes.Usage {
		t.Fatalf("refusal came back as %s, want %s", got, codes.Usage)
	}
}

// A limit is a page of the trail, and a page over is not an error.
func TestEventsTakesALimitAsAWholeNumber(t *testing.T) {
	d, _ := newDaemon(t)
	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if rep := read(t, d, map[string]any{"limit": float64(1)}); rep.Count != 1 {
		t.Fatalf("a limit of one answered %d events", rep.Count)
	}
	_, err := d.Handle(context.Background(), protocol.Request{
		Verb: "events", Args: map[string]any{"limit": "twenty"}})
	if err == nil {
		t.Fatal("a limit that is not a number was accepted")
	}
}

// §8.2: --follow is a subscription. An event written after the stream opened
// reaches it without the caller asking again.
func TestFollowingHandsOverAnEventWrittenAfterItOpened(t *testing.T) {
	d, _ := newDaemon(t)
	d.Loop.OnEvent = d.Emitted

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	left, right := socketPair(t)
	go d.stream(ctx, protocol.Request{Verb: "events", Args: map[string]any{"limit": float64(1)}}, right, json.NewEncoder(right))

	go func() {
		// The stream is opened first and the event written after it, which
		// is the whole of what a subscription promises.
		time.Sleep(20 * time.Millisecond)
		d.Loop.Dispatch(context.Background(), "01AAA", "")
	}()

	dec := json.NewDecoder(left)
	// Well inside FollowPoll, so what is being pinned is the WAKE-UP a write
	// sends and not the backstop poll behind it.
	left.SetReadDeadline(time.Now().Add(FollowPoll / 2))
	var resp protocol.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("the stream said nothing: %v", err)
	}
	var ev store.Event
	if err := json.Unmarshal(resp.Result, &ev); err != nil {
		t.Fatalf("the stream wrote %s", resp.Result)
	}
	if ev.Name != "dispatch.task.reserved" {
		t.Fatalf("the stream wrote %+v", ev)
	}
	// A bounded stream says it is over rather than just closing: at the
	// socket a finished stream and a dead daemon are the same thing.
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("the stream ended without saying so: %v", err)
	}
	if !resp.Done {
		t.Fatalf("the stream's last word was %+v, want done", resp)
	}
}

// A stream the daemon ends says so, so a follower can tell a finished stream
// from a daemon that died under it.
func TestAStreamEndsWithDoneWhenTheDaemonGoes(t *testing.T) {
	d, _ := newDaemon(t)
	ctx, stop := context.WithCancel(context.Background())
	left, right := socketPair(t)
	done := make(chan struct{})
	go func() {
		d.stream(ctx, protocol.Request{Verb: "events"}, right, json.NewEncoder(right))
		close(done)
	}()
	stop()
	<-done

	left.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp protocol.Response
	if err := json.NewDecoder(left).Decode(&resp); err != nil {
		t.Fatalf("the stream wrote nothing on its way out: %v", err)
	}
	if !resp.Done {
		t.Fatalf("the stream's last word was %+v, want done", resp)
	}
}

// §8.3: the hook runs for every event, with the event in its environment.
func TestTheHookRunsForEveryEventCarryingIt(t *testing.T) {
	d, _ := newDaemon(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "fired")
	hook := filepath.Join(dir, "hook.sh")
	script := "#!/bin/sh\nprintf '%s %s %s %s %s\\n' \"$HDIS_EVENT\" \"$HDIS_ENTITY\" \"$HDIS_ID\" \"$HDIS_ACTOR\" \"$HDIS_PROJECT\" >> " + out + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	d.Loop.Config.OnEvent = []string{hook}
	d.Loop.OnEvent = d.Emitted

	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	said := waitForFile(t, out)
	if !strings.HasPrefix(said, "dispatch.task.reserved task 01AAA ") {
		t.Fatalf("the hook was handed %q", said)
	}
	if !strings.Contains(said, "/src/p") {
		t.Fatalf("the hook was not told the project: %q", said)
	}
}

// §8.3: a hook that fails MUST NOT fail the write that caused it.
func TestAHookThatCannotRunDoesNotFailTheWrite(t *testing.T) {
	d, _ := newDaemon(t)
	d.Loop.Config.OnEvent = []string{filepath.Join(t.TempDir(), "no-such-hook")}
	d.Loop.OnEvent = d.Emitted

	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("the dispatch failed because its hook could not run: %v", err)
	}
	if rep := read(t, d, nil); rep.Count != 1 {
		t.Fatalf("the event was lost with the hook: %+v", rep)
	}
}

// doctor answers whether a hook is configured at all, which is the one fact a
// call site cannot show: a hook that never fires and no hook look the same.
func TestDoctorNamesTheEventHookAndTheTrail(t *testing.T) {
	d, _ := newDaemon(t)
	d.Loop.Config.OnEvent = []string{"/bin/true"}
	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rep := d.eventsHealth()
	if rep.Trail != 1 || rep.Max != store.MaxEvents {
		t.Fatalf("doctor says the trail is %+v", rep)
	}
	if len(rep.Hook) != 1 || rep.Hook[0] != "/bin/true" {
		t.Fatalf("doctor says the hook is %v", rep.Hook)
	}
}

// The hook is a config key like the gate's, and an empty word in it is a
// command nobody can run.
func TestAnEmptyWordInTheHookIsRefused(t *testing.T) {
	_, err := config.Parse([]byte("default = \"w\"\non_event = [\"\"]\n[profiles.w]\nprovider = \"claude\"\n"))
	if err == nil || !strings.Contains(err.Error(), "on_event") {
		t.Fatalf("a hook with an empty word parsed as %v", err)
	}
}

// socketPair is a connected pair of Unix sockets, so a stream can be driven
// against a real connection rather than a buffer: the stream writes until the
// far end goes away, which only a socket can be made to do.
func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "socketpair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatalf("socketpair: %v", err)
		}
		u, ok := c.(*net.UnixConn)
		if !ok {
			t.Fatalf("socketpair gave a %T", c)
		}
		t.Cleanup(func() { u.Close() })
		return u
	}
	return conn(fds[0]), conn(fds[1])
}

// waitForFile is the hook's own output, waited for: the hook is detached on
// purpose, so nothing here can join it.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing was written to %s", path)
	return ""
}

// §5.8 is the WHOLE store, and the trail is part of it: a reader who wants
// the document without this binary should not have to know one list was held
// back.
func TestDumpCarriesTheTrail(t *testing.T) {
	d, _ := newDaemon(t)
	if _, err := d.Loop.Dispatch(context.Background(), "01AAA", ""); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	raw, err := d.Handle(context.Background(), protocol.Request{Verb: "dump"})
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	var rep DumpReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(rep.Events) != 1 || rep.Events[0].Name != "dispatch.task.reserved" {
		t.Fatalf("dump carries %+v", rep.Events)
	}
}
