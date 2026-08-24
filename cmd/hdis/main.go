// Command hdis is the dispatcher: it watches the htask board for ready work,
// brings a worker pane up in Herdr for each ready task, delivers the task's
// goal, tracks the worker, and stops at review.
//
// One binary is the daemon and both doors. `hdis daemon` (or `hdis run`) owns
// the tick and the bindings; `hdis <verb>` and `hdis mcp` are thin clients of
// it, and start one when none is running.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/codes"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("hdis: ")

	// --json has to be known BEFORE cobra parses, because cobra's own parse
	// failures are among the failures that must answer with one document
	// (§6.2). At that moment the flag exists only in argv.
	asJSON := cli.WantsJSON(os.Args[1:])

	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	// One failure, one report, one stream (§6.2): with --json the envelope
	// IS the report and it goes to stdout, otherwise a sentence goes to
	// stderr and stdout stays empty. The status is the one §6.3 fixes for
	// the code, so a caller scripting three sibling plugins reads the same
	// number from each. Cobra's own failures — an unknown flag, an unknown
	// subcommand — are caller input errors, which the contract fixes at
	// USAGE, and codes.Of gives an unnamed error UNEXPECTED, so they are
	// named here rather than left to fall through.
	err = cli.AsRefusal(err)
	code := codes.Of(err)
	if asJSON {
		cli.WriteError(err, os.Stdout)
	} else {
		fmt.Fprintf(os.Stderr, "hdis: %s\n", err)
	}
	os.Exit(codes.Exit(code))
}
