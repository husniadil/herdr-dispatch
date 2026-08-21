// Package codes is the small vocabulary of named failures the daemon answers
// with. Both doors repeat the code as it is: a caller reads a name it can
// branch on rather than an exit status or a sentence it would have to parse.
package codes

import (
	"errors"
	"fmt"
)

// Code names one way a verb can refuse.
type Code string

const (
	// Unavailable is the daemon, the board or herdr not answering.
	Unavailable Code = "UNAVAILABLE"
	// Invalid is a request this binary cannot make sense of: an unknown
	// verb, a missing argument, an argument of the wrong kind.
	Invalid Code = "INVALID"
	// NotFound is a task the board does not have.
	NotFound Code = "NOT_FOUND"
	// NotReady is a task the board has and will not hand out: already
	// claimed, blocked, or past todo.
	NotReady Code = "NOT_READY"
	// AtCapacity is MaxWorkers already live.
	AtCapacity Code = "AT_CAPACITY"
	// NoBasePane is a daemon with no pane to split a worker off, which is
	// every daemon started outside a Herdr pane and given no -pane.
	NoBasePane Code = "NO_BASE_PANE"
	// AlreadyDispatched is a task this daemon is already driving.
	AlreadyDispatched Code = "ALREADY_DISPATCHED"
	// AlreadyRunning is a second daemon meeting the first one's lock.
	AlreadyRunning Code = "ALREADY_RUNNING"
)

// Error is a failure with a name on it.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf builds a named failure.
func Errorf(code Code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
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
