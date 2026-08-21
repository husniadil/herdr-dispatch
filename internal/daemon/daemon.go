// Package daemon is the one process that owns the tick and the bindings.
//
// Both doors are thin clients of it, and neither holds state: an MCP door is
// spawned once per client session, so a binding kept there would be one of
// several disagreeing sets, and the follow-through — the claim-timeout nudge,
// the review announcement, retiring a pane — would run once per door. One
// daemon per user is what makes one live worker per task true across every
// caller.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
)

// SocketMode is what the socket is created with. It is a door onto the
// operator's own panes, and the state dir it sits in is private for the same
// reason.
const SocketMode = 0o600

// Daemon serves the verb table over a socket and ticks the loop behind it.
type Daemon struct {
	Loop  *loop.Loop
	Board *htask.Client
	// Interval is how often the loop ticks when nothing asks it to sooner.
	Interval time.Duration
	// Version is this binary's version, for doctor.
	Version string
	Log     *log.Logger

	// kick wakes the tick early, so an accepted dispatch does not wait out
	// the interval before its worker comes up.
	kick chan struct{}
}

// Lock takes the one-daemon lock, and holds it for as long as the returned
// file is open. The kernel releases it when the process ends, so a daemon
// that crashes leaves nothing behind to clean up.
func Lock() (*os.File, error) {
	if err := config.EnsureStateDir(); err != nil {
		return nil, err
	}
	path := config.LockPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, SocketMode)
	if err != nil {
		return nil, fmt.Errorf("daemon lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, codes.Errorf(codes.AlreadyRunning,
			"another hdis daemon is running: it holds %s", path)
	}
	return f, nil
}

// Listen opens the daemon's socket. A socket file with no daemon behind it is
// replaced: the lock, not the file, is what says whether one is running.
func Listen() (net.Listener, error) {
	if err := config.EnsureStateDir(); err != nil {
		return nil, err
	}
	path := config.SocketPath()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("clear the socket at %s: %w", path, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, SocketMode); err != nil {
		ln.Close()
		return nil, fmt.Errorf("close the socket to other users: %w", err)
	}
	return ln, nil
}

// Serve ticks the loop and answers the socket until ctx ends.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	d.kick = make(chan struct{}, 1)
	ticking := make(chan struct{})
	go func() {
		defer close(ticking)
		d.tick(ctx)
	}()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				<-ticking
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go d.answer(ctx, conn)
	}
}

// tick runs the loop on its interval, and early whenever a dispatch asks.
func (d *Daemon) tick(ctx context.Context) {
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for {
		// A tick that fails is reported and the next one still runs: the
		// board being unreachable for a moment is not a reason to stop
		// dispatching for good.
		if err := d.Loop.Tick(ctx); err != nil && ctx.Err() == nil {
			d.logf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
		case <-t.C:
		}
	}
}

func (d *Daemon) answer(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	var req protocol.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Invalid), Message: "unreadable request: " + err.Error()}})
		return
	}
	result, err := d.Handle(ctx, req)
	if err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: message(err)}})
		return
	}
	d.write(conn, protocol.Response{Result: result})
}

func (d *Daemon) write(conn net.Conn, resp protocol.Response) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		d.logf("answer: %v", err)
	}
}

// Handle runs one verb. It is the whole of what a door may ask for: both
// doors reach the daemon through here and nowhere else.
func (d *Daemon) Handle(ctx context.Context, req protocol.Request) (json.RawMessage, error) {
	v, ok := verbs.ByName(req.Verb)
	if !ok {
		return nil, codes.Errorf(codes.Invalid, "no verb named %q", req.Verb)
	}
	if err := check(v, req.Args); err != nil {
		return nil, err
	}

	switch v.Name {
	case "doctor":
		return encode(d.doctor(ctx))
	case "dispatch":
		ref, _ := req.Args["task"].(string)
		res, err := d.Loop.Dispatch(ctx, ref)
		if err != nil {
			d.logf("dispatch %s for %s over %s: %v", ref, req.Caller(), door(req), err)
			return nil, err
		}
		d.logf("task %d %q reserved for %s over %s", res.Seq, res.Title, req.Caller(), door(req))
		d.wake()
		return encode(res, nil)
	case "status":
		st, err := d.Loop.Status(ctx)
		return encode(st, err)
	}
	return nil, codes.Errorf(codes.Invalid, "verb %q is declared and not served", v.Name)
}

// wake asks the tick to run now rather than at the end of the interval. It
// never blocks: a kick already waiting is the same news.
func (d *Daemon) wake() {
	if d.kick == nil {
		return
	}
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// DoctorReport is what doctor answers with: enough to say why a dispatch
// would refuse, before one is tried.
type DoctorReport struct {
	Version    string      `json:"version"`
	Socket     string      `json:"socket"`
	BasePane   string      `json:"base_pane"`
	MaxWorkers int         `json:"max_workers"`
	Interval   string      `json:"interval"`
	Workers    int         `json:"workers"`
	Pending    int         `json:"pending"`
	Board      BoardHealth `json:"board"`
}

// BoardHealth is the board's own report of itself, or why it gave none.
type BoardHealth struct {
	Reachable      bool   `json:"reachable"`
	Version        string `json:"version,omitempty"`
	Contract       string `json:"contract,omitempty"`
	HerdrReachable bool   `json:"herdr_reachable"`
	Error          string `json:"error,omitempty"`
}

// doctor never fails. It is what an operator runs when something else already
// refused, and a board that is down is the answer rather than an obstacle to
// giving one.
func (d *Daemon) doctor(ctx context.Context) (DoctorReport, error) {
	rep := DoctorReport{
		Version:    d.Version,
		Socket:     config.SocketPath(),
		BasePane:   d.Loop.BasePane,
		MaxWorkers: d.Loop.Policy.MaxWorkers,
		Interval:   d.Interval.String(),
		Workers:    len(d.Loop.Bindings()),
		Pending:    len(d.Loop.Pending()),
	}
	board, err := d.Board.Doctor(ctx)
	if err != nil {
		rep.Board.Error = err.Error()
		return rep, nil
	}
	rep.Board = BoardHealth{
		Reachable:      board.SocketLive,
		Version:        board.Version,
		Contract:       board.Contract,
		HerdrReachable: board.HerdrReachable,
	}
	if !board.SocketLive {
		rep.Board.Error = "the board's daemon is not answering on " + board.Binary
	}
	return rep, nil
}

// check refuses a request the verb table does not describe: a required
// argument missing, an argument of the wrong kind, or one the verb never
// declared. A door that sends an argument the daemon drops silently is how a
// caller ends up believing something it asked for happened.
func check(v verbs.Verb, args map[string]any) error {
	declared := make(map[string]verbs.Arg, len(v.Args))
	for _, a := range v.Args {
		declared[a.Name] = a
	}
	for name := range args {
		if _, ok := declared[name]; !ok {
			return codes.Errorf(codes.Invalid, "%s takes no argument named %q", v.Name, name)
		}
	}
	for _, a := range v.Args {
		raw, ok := args[a.Name]
		if !ok || raw == nil {
			if a.Required {
				return codes.Errorf(codes.Invalid, "%s needs %s", v.Name, a.Name)
			}
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return codes.Errorf(codes.Invalid, "%s wants %s as a string", v.Name, a.Name)
		}
		if a.Required && s == "" {
			return codes.Errorf(codes.Invalid, "%s needs %s", v.Name, a.Name)
		}
	}
	return nil
}

func encode(v any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("answer: %w", err)
	}
	return b, nil
}

// message is the sentence a caller reads, without the code repeated in it.
func message(err error) string {
	var named *codes.Error
	if errors.As(err, &named) {
		return named.Message
	}
	return err.Error()
}

func door(req protocol.Request) string {
	if req.Door == "" {
		return "an unnamed door"
	}
	return "the " + req.Door
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
