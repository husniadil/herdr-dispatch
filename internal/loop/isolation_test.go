package loop

import (
	"os"
	"testing"

	"github.com/husniadil/herdr-dispatch/internal/testenv"
)

// TestMain moves this package off the operator's own home for the whole test
// binary, so nothing here can read or write the live fleet's state directory.
//
// It is the whole binary rather than the cases that name a path, because the
// cases that do the damage are the ones that name none: config.StateDir()
// resolves from the environment wherever it is called from. config.StateDir
// panics inside a test binary when it lands under the real home, so a package
// that reaches it without this fails loud instead of writing.
func TestMain(m *testing.M) { os.Exit(testenv.RunIsolated(m)) }
