package daemon

import (
	"fmt"
	"io"
	"os"
)

// LogMode is what the daemon's log file is created with. It is in the same
// private state dir as the socket and the bindings, and it records which
// task went to which pane.
const LogMode = 0o600

// OpenLog says where a daemon's log lines go. It opens path for appending
// and returns a writer onto both that file and stdout, so a daemon started
// in the foreground keeps the lines its operator is watching while the file
// in the state dir holds the same ones afterwards.
//
// The one case that is not both is a daemon a door started: stdout is
// already the very file about to be opened, and teeing there would write
// every line into it twice. The file is compared, not the terminal, because
// what matters is whether the two writers are the same file and not whether
// anyone is looking.
//
// A path that cannot be opened is an error AND a usable writer: the caller
// gets stdout back to say so on. A dispatcher that will not start because it
// cannot write a file is worse than one that logs where it still can.
func OpenLog(path string, stdout *os.File) (io.Writer, *os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, LogMode)
	if err != nil {
		return stdout, nil, fmt.Errorf("daemon log %s: %w", path, err)
	}
	if sameFile(f, stdout) {
		return f, f, nil
	}
	return io.MultiWriter(stdout, f), f, nil
}

// sameFile reports whether two open files are one file. A stat that fails
// answers no: two writers that cannot be compared are treated as two.
func sameFile(a, b *os.File) bool {
	if a == nil || b == nil {
		return false
	}
	ai, err := a.Stat()
	if err != nil {
		return false
	}
	bi, err := b.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}
