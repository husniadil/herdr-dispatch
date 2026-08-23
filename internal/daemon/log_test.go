package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A daemon in the foreground writes each line twice: to the file it opened in
// its own state dir, and to the stdout an operator is watching.
func TestOpenLogWritesToBothTheFileAndStdout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hdis.log")
	out := filepath.Join(dir, "stdout")
	stdout, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	w, f, err := OpenLog(path, stdout)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := w.Write([]byte("listening on /s/hdis.sock\n")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{path, out} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if !strings.Contains(string(b), "listening on") {
			t.Errorf("%s holds %q, want the line", p, b)
		}
	}
}

// A daemon a door started already has stdout pointing at the very file it is
// about to open. Teeing there would write every line twice into one file, so
// the file it opened is the only writer.
func TestOpenLogDoesNotDoubleWhenStdoutIsAlreadyTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hdis.log")
	stdout, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	w, f, err := OpenLog(path, stdout)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := w.Write([]byte("stopping\n")); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "stopping"); got != 1 {
		t.Fatalf("the log holds the line %d times: %q", got, b)
	}
}

// A log that cannot be opened is said and survived. A dispatcher that refuses
// to start because it cannot write a file is worse than one that says so on
// the stdout it still has.
func TestOpenLogFallsBackToStdoutWhenTheFileCannotBeOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "hdis.log")
	out := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()

	w, f, err := OpenLog(path, stdout)
	if err == nil {
		t.Fatal("opening a log under a directory that does not exist reported no error")
	}
	if f != nil {
		t.Fatalf("a failed open handed back a file to close: %v", f)
	}
	if _, err := w.Write([]byte("still here\n")); err != nil {
		t.Fatalf("the fallback writer is unusable: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "still here") {
		t.Fatalf("stdout holds %q, want the line", b)
	}
}

// doctor names the log the running daemon actually opened, so an operator
// looking for a spawn decision is told where to read rather than guessing at
// the shell line that started it.
func TestDoctorNamesTheLogTheDaemonOpened(t *testing.T) {
	d, _ := newDaemon(t)
	d.LogPath = filepath.Join(t.TempDir(), "hdis.log")

	if rep := doctorOf(t, d); rep.Log != d.LogPath {
		t.Fatalf("doctor says the log lives at %q, want %q", rep.Log, d.LogPath)
	}
}
