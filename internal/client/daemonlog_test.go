package client

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/config"
)

// safeBuffer collects a child's stdout while the test reads it from another
// goroutine, which is what the race detector is watching for here.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startDaemon runs the real binary as an operator would, with its output
// going nowhere near the log file: whatever lands there, the daemon put
// there itself.
func startDaemon(t *testing.T) *safeBuffer {
	t.Helper()
	bin := build(t)
	world(t)

	out := &safeBuffer{}
	cmd := exec.Command(bin, "daemon", "-interval", "1h")
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the daemon: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	return out
}

// waitFor polls until want shows up in read(), so a test never races the
// daemon's own startup.
func waitFor(t *testing.T, want string, read func() string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := read()
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %q and never saw it in:\n%s", want, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A daemon an operator started with no redirection at all writes its log
// into its own state dir, and the startup line is in it. Take the open away
// and the file is never created.
func TestADaemonStartedWithNoRedirectionWritesItsLogIntoItsStateDir(t *testing.T) {
	startDaemon(t)

	waitFor(t, "listening on", func() string {
		b, err := os.ReadFile(config.LogPath())
		if err != nil {
			return ""
		}
		return string(b)
	})
}

// The same lines still reach stdout, so an operator watching the daemon in
// the foreground loses nothing by the file being opened.
func TestADaemonInTheForegroundStillWritesToStdout(t *testing.T) {
	out := startDaemon(t)

	waitFor(t, "listening on", out.String)
}
