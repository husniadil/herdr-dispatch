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
	"sync"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/verbs"
	"github.com/husniadil/herdr-dispatch/internal/version"
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
	// LogPath is the log file this daemon opened, as it opened it, and what
	// doctor names. Empty means it could not open one and is writing to
	// stdout alone.
	LogPath string
	// Lock is the one-daemon lock this daemon holds, as Lock opened it.
	// Teardown removes that file and no other: the path is what was
	// opened, never what the state dir names by the time teardown runs.
	Lock *os.File

	// kick wakes the tick early, so an accepted dispatch does not wait out
	// the interval before its worker comes up.
	kick chan struct{}
	// halt carries an operator's stop from the verb to Serve.
	halt chan struct{}
	// answered closes when the stop that asked has its own answer on the
	// wire, which is what Serve waits for before the process goes. Waiting
	// on every open connection instead would hang on a caller that holds
	// one open and asks nothing.
	answered  chan struct{}
	stopOnce  sync.Once
	writeOnce sync.Once
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
		return nil, codes.Refusef(codes.AlreadyRunning,
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

// Serve ticks the loop and answers the socket until ctx ends or stop is
// asked for. Either way it leaves nothing of itself behind.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	d.kick = make(chan struct{}, 1)
	d.halt = make(chan struct{})
	d.answered = make(chan struct{})
	ctx, done := context.WithCancel(ctx)
	defer done()
	ticking := make(chan struct{})
	go func() {
		defer close(ticking)
		d.tick(ctx)
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-d.halt:
			done()
		}
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				select {
				case <-d.halt:
					// The stop that closed the listener is still writing
					// its own answer; leave after it, not during it.
					<-d.answered
				default:
				}
				<-ticking
				d.Cleanup(ln)
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go d.answer(ctx, conn)
	}
}

// Cleanup removes the socket this daemon is listening on and the lock it
// holds. The lock is released by the kernel when the process ends either
// way; removing the file as well keeps a stopped daemon from leaving a path
// behind that says one is still here.
//
// Both paths come from what was opened — the listener's own address, the
// lock file's own name — and never from the state dir read again here. A
// daemon that resolves them at teardown deletes whatever that dir holds by
// then, which is another daemon's socket the moment the dir has moved
// underneath it.
func (d *Daemon) Cleanup(ln net.Listener) {
	paths := []string{}
	if ln != nil {
		if addr, ok := ln.Addr().(*net.UnixAddr); ok && addr.Name != "" {
			paths = append(paths, addr.Name)
		}
	}
	if d.Lock != nil {
		paths = append(paths, d.Lock.Name())
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.logf("remove %s: %v", path, err)
		}
	}
}

// tick runs the loop on its interval, and early whenever a dispatch asks.
//
// A daemon with no pane to split a worker off does not tick at all. Every
// spawn it could reach for would fail on the same missing pane, once per
// interval for as long as it runs, and a log of one error repeated is a log
// nobody reads. Both doors still answer, and dispatch says why with a name.
func (d *Daemon) tick(ctx context.Context) {
	if d.Loop.BasePane == "" {
		d.logf("no base pane: not ticking, and dispatch will refuse with %s", codes.NoBasePane)
		<-ctx.Done()
		return
	}
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
	if req.Verb == "stop" && d.answered != nil {
		d.writeOnce.Do(func() { close(d.answered) })
	}
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
		return nil, codes.Refusef(codes.Invalid, "no verb named %q", req.Verb)
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
	case "stop":
		if d.halt == nil {
			return nil, codes.Refusef(codes.NotRunning, "this daemon is not serving")
		}
		d.logf("stopping: %s asked over %s", req.Caller(), door(req))
		// The board hears nothing about this. A worker mid-task keeps its
		// claim and its lease, and htask times those out on its own; a
		// second writer racing that is the bug, not a courtesy.
		d.stopOnce.Do(func() { close(d.halt) })
		return encode(StopReport{Stopping: true, Socket: config.SocketPath(), PID: os.Getpid()}, nil)
	}
	return nil, codes.Refusef(codes.Invalid, "verb %q is declared and not served", v.Name)
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

// StopReport is what stop answers with, before the daemon goes.
type StopReport struct {
	Stopping bool   `json:"stopping"`
	Socket   string `json:"socket"`
	PID      int    `json:"pid"`
}

// DoctorReport is what doctor answers with: enough to say why a dispatch
// would refuse, before one is tried.
type DoctorReport struct {
	Version string `json:"version"`
	// Contract is the plugin contract THIS binary satisfies, which is not
	// the board's: Board.Contract below is htask's own, relayed.
	Contract   string `json:"contract"`
	Socket     string `json:"socket"`
	BasePane   string `json:"base_pane"`
	MaxWorkers int    `json:"max_workers"`
	Interval   string `json:"interval"`
	Workers    int    `json:"workers"`
	// AwaitingReview is how many of those workers are holding a slot while
	// a human decides on what they submitted. They spend nothing and are
	// kept on purpose: a rejection carries on in the same pane.
	AwaitingReview int `json:"awaiting_review"`
	Pending        int `json:"pending"`
	// Bindings is the file the pane-to-task mapping is kept in, and
	// Readopted is how many of them this daemon took back when it started.
	Bindings  string `json:"bindings"`
	Readopted int    `json:"readopted"`
	// Log is the file this daemon opened its log on, which is where an
	// operator reads a spawn decision back. It is what was opened, not what
	// the state dir names now, and it is empty when the open failed and the
	// lines are going to stdout alone.
	Log   string      `json:"log,omitempty"`
	Board BoardHealth `json:"board"`
	// Verify is the verification lane: whether a submission earns a
	// self-review shot in the pane that produced it.
	Verify VerifyHealth `json:"verify"`
	// MinPaneColumns is the width a worker's pane must be readable at, and
	// the reason every worker gets a tab of its own. Herdr reports no column
	// count for a pane, so this is the one place an operator can see the
	// floor their window has to clear.
	MinPaneColumns int `json:"min_pane_columns"`
	// MaxPanesPerTab is how many panes one of this dispatcher's tabs may
	// hold. A tab holds one task, so it bounds the panes ONE task may have
	// rather than keeping two tasks apart. The operator sets it in the
	// config, and this is where they read it back off the running daemon.
	MaxPanesPerTab int `json:"max_panes_per_tab"`
}

// VerifyHealth is the verification lane as doctor reports it. There is
// nothing to name beside the switch: the shot lands in a pane that is
// already up.
type VerifyHealth struct {
	Enabled bool `json:"enabled"`
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
		Version:        d.Version,
		Contract:       version.Contract,
		Socket:         config.SocketPath(),
		BasePane:       d.Loop.BasePane,
		MaxWorkers:     d.Loop.Policy.MaxWorkers,
		Interval:       d.Interval.String(),
		Workers:        len(d.Loop.Bindings()),
		AwaitingReview: d.Loop.AwaitingReview(),
		Pending:        len(d.Loop.Pending()),
		Bindings:       d.Loop.BindingsPath(),
		Log:            d.LogPath,
		Readopted:      d.Loop.Readopted(),
		Verify:         VerifyHealth{Enabled: d.Loop.Config.Verify.Enabled},
		MinPaneColumns: d.Loop.Config.Layout.MinPaneColumns,
		MaxPanesPerTab: d.Loop.Config.Layout.MaxPanesPerTab,
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
			return codes.Refusef(codes.Invalid, "%s takes no argument named %q", v.Name, name)
		}
	}
	for _, a := range v.Args {
		raw, ok := args[a.Name]
		if !ok || raw == nil {
			if a.Required {
				return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
			}
			continue
		}
		s, ok := raw.(string)
		if !ok {
			return codes.Refusef(codes.Invalid, "%s wants %s as a string", v.Name, a.Name)
		}
		if a.Required && s == "" {
			return codes.Refusef(codes.Invalid, "%s needs %s", v.Name, a.Name)
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
