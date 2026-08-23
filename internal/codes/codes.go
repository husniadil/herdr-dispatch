// Package codes is the failure vocabulary both doors answer with.
//
// The top-level names are the shared plugin contract's own (§6.3) and nothing
// else: a caller branching on a code reads the same nine words from every
// plugin, and §6.3 forbids a plugin inventing a tenth. What this binary
// refuses for is finer than nine words, so the finer name travels as a
// sub-reason INSIDE the message, which is the one place §6.3 leaves for it.
// Nothing a caller could read before is lost — NO_BASE_PANE is still the first
// word of the sentence — and everything a caller could not branch on before,
// because the code was ours alone, it now can.
package codes

import (
	"errors"
	"fmt"
	"strings"
)

// Code is one of the contract's nine. There are no others.
type Code string

const (
	// Usage is a caller-validatable input error.
	Usage Code = "USAGE"
	// NotFound is a named entity that does not exist in scope.
	NotFound Code = "NOT_FOUND"
	// Unavailable is the daemon, the board, herdr or a required binary not
	// answering.
	Unavailable Code = "UNAVAILABLE"
	// Timeout is a bounded wait that elapsed.
	Timeout Code = "TIMEOUT"
	// Conflict is a state guard that failed: held by someone else, already
	// running, no room.
	Conflict Code = "CONFLICT"
	// Unsupported is the host or Herdr lacking a capability the verb needs.
	Unsupported Code = "UNSUPPORTED"
	// Forbidden is a caller principal that may not do this to this target.
	Forbidden Code = "FORBIDDEN"
	// Denied is the policy gate saying no.
	Denied Code = "DENIED"
	// Unexpected is anything else.
	Unexpected Code = "UNEXPECTED"
)

// exits is the §6.3 exit status of each code.
var exits = map[Code]int{
	Usage:       2,
	NotFound:    3,
	Unavailable: 4,
	Timeout:     5,
	Conflict:    6,
	Unsupported: 7,
	Forbidden:   8,
	Denied:      9,
	Unexpected:  1,
}

// Exit is the process exit status the contract fixes for a code. Anything the
// table does not name is UNEXPECTED's 1, which is what the contract calls
// anything else.
func Exit(code Code) int {
	if e, ok := exits[code]; ok {
		return e
	}
	return exits[Unexpected]
}

// Reason is one of this binary's own sub-reasons. It is never a code: it is
// the first word of the message, and the code beside it is the contract's.
type Reason string

const (
	// Invalid is a request this binary cannot make sense of: an unknown
	// verb, a missing argument, an argument of the wrong kind.
	Invalid Reason = "INVALID"
	// NotReady is a task the board has and will not hand out: already
	// claimed, blocked, or past todo.
	NotReady Reason = "NOT_READY"
	// AtCapacity is MaxWorkers already live.
	AtCapacity Reason = "AT_CAPACITY"
	// NoBasePane is a daemon with no pane to split a worker off, which is
	// every daemon started outside a Herdr pane and given no -pane.
	NoBasePane Reason = "NO_BASE_PANE"
	// AlreadyDispatched is a task this daemon is already driving.
	AlreadyDispatched Reason = "ALREADY_DISPATCHED"
	// AlreadyRunning is a second daemon meeting the first one's lock.
	AlreadyRunning Reason = "ALREADY_RUNNING"
	// NotRunning is a verb that needs a live daemon and found none. Only
	// stop answers with it: every other verb starts one rather than refuse.
	NotRunning Reason = "NOT_RUNNING"
)

// carries maps each sub-reason onto the contract code it is a reason for.
var carries = map[Reason]Code{
	Invalid:    Usage,
	NoBasePane: Unsupported,
	// Every one of these is a state guard that failed, which is what §6.3
	// calls CONFLICT.
	NotReady:          Conflict,
	AtCapacity:        Conflict,
	AlreadyDispatched: Conflict,
	AlreadyRunning:    Conflict,
	NotRunning:        Conflict,
}

// Error is a failure carrying a contract code.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf builds a failure under one of the contract's own codes.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Refusef builds a failure under the contract code a sub-reason belongs to,
// with the sub-reason kept as the first word of the message so a caller that
// was branching on the old name still has it.
func Refusef(reason Reason, format string, a ...any) *Error {
	code, ok := carries[reason]
	if !ok {
		code = Unexpected
	}
	return &Error{Code: code, Message: string(reason) + ": " + fmt.Sprintf(format, a...)}
}

// Of reports the code err carries, at any depth. A failure that carries none
// is Unavailable: everything in this binary that fails without a name of its
// own failed reaching something else.
func Of(err error) Code {
	if err == nil {
		return ""
	}
	var named *Error
	if errors.As(err, &named) {
		return named.Code
	}
	return Unavailable
}

// ReasonOf reports the sub-reason err refuses for, or empty when it carries
// none. The code is what a caller outside this binary branches on; this is how
// a caller inside it tells one CONFLICT from another.
func ReasonOf(err error) Reason {
	var named *Error
	if !errors.As(err, &named) {
		return ""
	}
	for reason := range carries {
		if strings.HasPrefix(named.Message, string(reason)+": ") {
			return reason
		}
	}
	return ""
}
