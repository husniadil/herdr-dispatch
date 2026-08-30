// Package client is the door side of the socket: dial the daemon, start it
// when nothing is listening, send one request and read one answer. Both doors
// reach the daemon through here and hold nothing of their own.
package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
)

// StartTimeout bounds the wait for a daemon this client had to start. An
// invocation that finds no live socket starts one and waits for it, bounded,
// rather than fail.
const StartTimeout = 3 * time.Second

// Client dials the daemon and starts one when none answers.
type Client struct {
	// Bin is the binary to start a daemon from; empty means this one.
	Bin string
	// Timeout bounds the wait for a daemon this client started; zero means
	// StartTimeout.
	Timeout time.Duration
	// NoStart refuses with NotRunning when nothing is listening, rather
	// than starting a daemon. Stop is what it is for, and so is
	// `doctor --no-start`, which asks the same question without answering
	// it by changing it.
	NoStart bool
	// Started is the daemon this client had to bring up, if it brought one
	// up. Nothing here stops it again: it outlives the door on purpose.
	Started *os.Process

	// logFrom is where the log stood when this client started a daemon.
	// Everything past it is what that daemon said on its way up, which is
	// the only account of a daemon that exits before it can be asked.
	logFrom int64
	// exited carries the child's end, once. A daemon that comes up never
	// sends anything here, so the wait below reads it without blocking.
	exited chan *os.ProcessState
}

// Call sends one request and returns the daemon's result.
func (c *Client) Call(req protocol.Request) (json.RawMessage, error) {
	conn, err := c.dialOrStart(req)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "send %s: %v", req.Verb, err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, codes.Errorf(codes.Unavailable, "read the answer to %s: %v", req.Verb, err)
	}
	if resp.Error != nil {
		return nil, &codes.Error{Code: codes.Code(resp.Error.Code), Message: resp.Error.Message}
	}
	return resp.Result, nil
}

// Stream sends one request and hands every answer to fn until the daemon says
// the stream is over or fn returns an error. This is `events --follow` (§8.2),
// and it is the one call with no single answer to wait for.
func (c *Client) Stream(req protocol.Request, fn func(json.RawMessage) error) error {
	conn, err := c.dialOrStart(req)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return codes.Errorf(codes.Unavailable, "send %s: %v", req.Verb, err)
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	for {
		var resp protocol.Response
		if err := dec.Decode(&resp); err != nil {
			// A closed socket is not an ending. The daemon says when a
			// stream is over; anything else that stops the decoder is the
			// daemon going away underneath, and reporting that as a clean
			// finish would have a follower exit having silently stopped
			// watching.
			return codes.Errorf(codes.Unavailable,
				"the daemon stopped streaming %s without saying it had finished: %v", req.Verb, err)
		}
		if resp.Error != nil {
			return &codes.Error{Code: codes.Code(resp.Error.Code), Message: resp.Error.Message}
		}
		if resp.Done {
			return nil
		}
		if err := fn(resp.Result); err != nil {
			return err
		}
	}
}

func (c *Client) dialOrStart(req protocol.Request) (net.Conn, error) {
	path := config.SocketPath()
	if conn, err := net.Dial("unix", path); err == nil {
		return conn, nil
	}
	// The verb's own rule and the caller's, in either order: `stop` never
	// starts a daemon, and `doctor --no-start` is a caller asking whether
	// one is up rather than asking for one.
	if c.NoStart || req.NoStart {
		return nil, codes.Refusef(codes.NotRunning,
			"no hdis daemon is listening on %s", path)
	}
	if err := c.start(); err != nil {
		return nil, err
	}

	// The daemon has to open its store dir, take its lock and bind before it
	// can answer. Backing off rather than spinning keeps a slow machine from
	// being the reason this fails.
	deadline := time.Now().Add(c.timeout())
	for wait := 20 * time.Millisecond; ; wait *= 2 {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(wait)
		if conn, err := net.Dial("unix", path); err == nil {
			return conn, nil
		}
		// A daemon that is already gone is not going to answer, and the
		// timeout would report the wait instead of the reason. Checked
		// after the dial rather than before it, so a daemon that answered
		// and then exited is still the answer this call gets.
		if state := c.childEnd(); state != nil {
			return nil, c.diedBeforeAnswering(path, state)
		}
	}
	if state := c.childEnd(); state != nil {
		return nil, c.diedBeforeAnswering(path, state)
	}
	return nil, codes.Errorf(codes.Unavailable,
		"started a daemon and none answered on %s within %s", path, c.timeout())
}

// childEnd is how the daemon this client started ended, or nil while it is
// still running.
func (c *Client) childEnd() *os.ProcessState {
	if c.exited == nil {
		return nil
	}
	select {
	case state := <-c.exited:
		// Put it back: the answer is read more than once, and a state read
		// out of the channel is gone.
		c.exited <- state
		return state
	default:
		return nil
	}
}

// diedBeforeAnswering is the failure that used to read as a timeout. A daemon
// that refuses to start — no config document is the one an operator meets
// first — exits before it binds, and the door that started it is the only
// place the reason can still be read: the child's output went to the log, and
// an operator who is being told about a socket has no reason to look there.
func (c *Client) diedBeforeAnswering(path string, state *os.ProcessState) error {
	said := c.childOutput()
	if said == "" {
		said = fmt.Sprintf("it wrote nothing to %s", config.LogPath())
	}
	return codes.Errorf(codes.Unavailable,
		"started a daemon and it exited %s before answering on %s: %s",
		ended(state), path, said)
}

// ended reads a process state the way an operator would say it.
func ended(state *os.ProcessState) string {
	if code := state.ExitCode(); code >= 0 {
		return fmt.Sprintf("with status %d", code)
	}
	// A signal has no exit code, and ProcessState prints it as
	// "signal: killed".
	return state.String()
}

// childOutputBudget bounds what a failure repeats from the log: enough for
// the sentence a refusal ends on, and never a whole startup's worth of lines
// pasted into an error.
const (
	childOutputBudget = 1 << 10
	childOutputLines  = 5
)

// childOutput is what the daemon this client started appended to the log,
// bounded to the last few lines. The log is the daemon's own file rather than
// a pipe on purpose: a pipe the door closes on its way out would take a
// long-lived daemon's stderr with it, and this is read exactly once, after a
// child that is already gone.
//
// A second daemon starting at the same moment appends to the same file, so
// what is read here is the window rather than provably one process's lines.
// That is worth it: the alternative reported no reason at all.
func (c *Client) childOutput() string {
	f, err := os.Open(config.LogPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= c.logFrom {
		return ""
	}
	from := c.logFrom
	if info.Size()-from > childOutputBudget {
		from = info.Size() - childOutputBudget
	}
	buf := make([]byte, info.Size()-from)
	n, err := f.ReadAt(buf, from)
	if n == 0 && err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n")
	if len(lines) > childOutputLines {
		lines = lines[len(lines)-childOutputLines:]
	}
	// One line, because this is a sentence an operator reads on stderr and
	// a machine caller reads inside a JSON message.
	return strings.TrimSpace(strings.Join(lines, " | "))
}

// start brings a daemon up, detached: it outlives the door that started it,
// and its log goes to a file because it has no terminal to write to.
func (c *Client) start() error {
	bin := c.Bin
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return codes.Errorf(codes.Unavailable, "no daemon is running and this binary cannot name itself: %v", err)
		}
		bin = exe
	}
	if err := config.EnsureStateDir(); err != nil {
		return err
	}
	logFile, err := os.OpenFile(config.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("daemon log %s: %w", config.LogPath(), err)
	}
	defer logFile.Close()
	// Where this child's own output begins. Read before it is started, so a
	// failure repeats what THIS daemon said rather than the tail of the last
	// one's run.
	if info, err := logFile.Stat(); err == nil {
		c.logFrom = info.Size()
	}

	cmd := exec.Command(bin, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Its own session, so closing the pane that started it does not take it
	// with them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return codes.Errorf(codes.Unavailable, "start a daemon from %s: %v", bin, err)
	}
	c.Started = cmd.Process
	// Nothing waits for it, so let the kernel reap it rather than leaving a
	// zombie behind this door — and keep how it ended, because a daemon that
	// exits before it binds is a reason this door owes its caller.
	c.exited = make(chan *os.ProcessState, 1)
	exited := c.exited
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState
	}()
	return nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return StartTimeout
}
