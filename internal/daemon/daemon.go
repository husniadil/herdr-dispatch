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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/decide"
	"github.com/husniadil/herdr-dispatch/internal/gate"
	"github.com/husniadil/herdr-dispatch/internal/herdrclient"
	"github.com/husniadil/herdr-dispatch/internal/htask"
	"github.com/husniadil/herdr-dispatch/internal/loop"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/proxy"
	"github.com/husniadil/herdr-dispatch/internal/store"
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
	// followers is every live `events --follow`, woken by each event the
	// loop writes.
	followers watchers
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
// A daemon with no pane to split a worker off does not run the loop. Every
// spawn it could reach for would fail on the same missing pane, once per
// interval for as long as it runs, and a log of one error repeated is a log
// nobody reads. Both doors still answer, and dispatch says why with a name.
//
// What it does instead is ask Herdr for a pane to adopt, once per interval.
// A daemon Herdr's plugin manager started at boot has no pane to inherit and
// none in its config, and the screen it will work on may not exist yet when
// it comes up; asking again is what turns that from a daemon refusing
// forever into one that starts working the moment there is somewhere to work.
func (d *Daemon) tick(ctx context.Context) {
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	said := false
	for {
		// A tick that fails is reported and the next one still runs: the
		// board being unreachable for a moment is not a reason to stop
		// dispatching for good.
		if d.Loop.EnsureBase(ctx) == "" {
			if !said {
				d.logf("no base pane: not ticking, and dispatch will refuse with %s until one can be adopted", codes.NoBasePane)
				said = true
			}
		} else if err := d.Loop.Tick(ctx); err != nil && ctx.Err() == nil {
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
	// `events --follow` is the one call that does not answer once: it holds
	// the connection and writes one Response per event (§8.2).
	if req.Verb == "events" && req.Follow {
		d.stream(ctx, req, conn, json.NewEncoder(conn))
		return
	}
	result, err := d.Handle(ctx, req)
	if err != nil {
		d.write(conn, protocol.Response{Error: &protocol.Failure{
			Code: string(codes.Of(err)), Message: message(err), ParkedID: codes.ParkedOf(err)}})
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
	if err := d.pass(v, req); err != nil {
		return nil, err
	}
	return d.serve(ctx, v, req)
}

// scope names the boards a call was answered from, so the log records what
// the caller asked for rather than leaving it to be inferred from a path that
// happens to be empty.
func scope(req protocol.Request) string {
	if req.Project != "" {
		return "on " + req.Project
	}
	if req.AllProjects {
		return "across every board"
	}
	return "across every board by default"
}

// serve runs one verb that has already been checked and passed the gate. It
// is separate from Handle because §9.3 re-runs a resolved verb here and MUST
// NOT put it through the gate again: the resolution is the decision the gate
// deferred, and a second ask would park it forever.
func (d *Daemon) serve(ctx context.Context, v verbs.Verb, req protocol.Request) (json.RawMessage, error) {
	switch v.Name {
	case "doctor":
		return encode(d.doctor(ctx, req))
	case "dispatch":
		ref, _ := req.Args["task"].(string)
		// §4.2: the door resolved --project to a canonical path already, so
		// what arrives here is a board to look on or nothing at all.
		res, err := d.Loop.Dispatch(ctx, ref, req.Project)
		if err != nil {
			d.logf("dispatch %s for %s over %s: %v", ref, req.Caller(), door(req), err)
			return nil, err
		}
		d.logf("task %d %q reserved for %s over %s, %s", res.Seq, res.Title, req.Caller(), door(req), scope(req))
		d.wake()
		return encode(res, nil)
	case "status":
		st, err := d.Loop.Status(ctx)
		return encode(st, err)
	case "dump":
		// One read of the set, not three: three would let a tick land
		// between them and print a document no process ever held. And empty
		// lists rather than nulls — a reader has to be able to tell "none"
		// from "this daemon could not say", and a JSON null says the second
		// while meaning the first.
		held := d.Loop.Dump()
		rep := DumpReport{
			Version:      store.Version,
			Path:         d.Loop.BindingsPath(),
			Bindings:     append([]decide.Binding{}, held.Bindings...),
			Reservations: append([]store.Reservation{}, held.Reservations...),
			Parked:       append([]store.Parked{}, held.Parked...),
			Events:       append([]store.Event{}, held.Events...),
		}
		return encode(rep, nil)
	case "events":
		return encode(d.events(req))
	case "parked.list":
		held := d.Loop.Parked()
		return encode(ParkedReport{Parked: held, Count: len(held)}, nil)
	case "parked.resolve":
		return encode(d.resolveParked(ctx, req))
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
	Contract string `json:"contract"`
	// Principal is who the daemon records THIS very call as (§3.2, §3.7),
	// derived by the door and never declared by the caller. §7.5 rests its
	// declaration on this line being here: a doctor call through a declared
	// door answers `human` and one through an undeclared door answers
	// nobody in particular, which is how an operator checks which of their
	// registrations speak for them.
	Principal string `json:"principal"`
	Socket    string `json:"socket"`
	// StateDir and ConfigDir are the two directories §10.3 makes doctor
	// print. An operator reading a stale binding or an override that is not
	// taking effect needs to know WHICH pair of directories this daemon
	// resolved, and the environment that decides them is the daemon's, not
	// the caller's.
	StateDir   string `json:"state_dir"`
	ConfigDir  string `json:"config_dir"`
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
	// Gate is the §9 policy gate: whether one is configured, what it is,
	// which verbs pass through it, and how many calls it has parked and
	// nobody has decided. An operator whose dispatch came back DENIED reads
	// here first.
	Gate GateHealth `json:"gate"`
	// Events is the §8 trail: how many events this daemon still holds, and
	// the hook each one is handed to. A hook that is configured and never
	// fires is indistinguishable from no hook at all at the call site, so
	// whether one is configured is a fact only doctor can give.
	Events EventsHealth `json:"events"`
	// Herdr is what §11.2's feature detection found: the protocol Herdr
	// reported, how many requests and events it listed, and any capability
	// this binary needs that it did not. §10.3 makes doctor print the Herdr
	// schema it saw, and it is the one place an operator can tell "this
	// Herdr cannot do it" from "this plugin did not ask".
	Herdr HerdrHealth `json:"herdr"`
	// Proxy is the codex provider's launcher: whether it is installed, up,
	// and which account it routes through. A down proxy is step zero of a
	// codex spawn failing, and without this line the first an operator
	// hears of it is that failure.
	Proxy ProxyHealth `json:"proxy"`
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

// GateHealth is the §9 policy gate as doctor reports it. §9.2 makes an
// unconfigured gate allow, which is indistinguishable at the call site from a
// configured one that allows — so whether one is configured at all is a fact
// only doctor can give.
type GateHealth struct {
	Configured bool     `json:"configured"`
	Command    []string `json:"command,omitempty"`
	// Verbs is the §9.4 list, so an operator writing a policy reads the
	// names it will be asked about off the running daemon.
	Verbs []string `json:"verbs"`
	// Parked is how many deferrals are waiting on the operator, or were
	// resolved and then failed. Both want a human.
	Parked int `json:"parked"`
}

// EventsHealth is the §8 event trail as doctor reports it.
type EventsHealth struct {
	// Trail is how many events are held, of the Max the trail keeps.
	Trail int `json:"trail"`
	Max   int `json:"max"`
	// Hook is the §8.3 command every event is handed to, empty when none is
	// configured.
	Hook []string `json:"hook,omitempty"`
}

// HerdrHealth is the §11.2 schema read, as doctor reports it.
type HerdrHealth struct {
	// Detected says the schema was read. False with an empty Missing list
	// means nothing has asked Herdr yet or the ask failed — which is not
	// the same as a Herdr that offers nothing.
	Detected bool `json:"detected"`
	// Protocol is reported and decided on by nothing: §11.2 calls pinning
	// an exact protocol number a contract violation.
	Protocol int `json:"protocol,omitempty"`
	Requests int `json:"requests,omitempty"`
	Events   int `json:"events,omitempty"`
	// Missing is every capability this binary needs that Herdr did not
	// list. The verbs that need one refuse with UNSUPPORTED naming it.
	Missing []string `json:"missing"`
	Error   string   `json:"error,omitempty"`
}

// needs is every Herdr request this binary asks for, which is what doctor
// checks the schema against. It is the adapter's own list, so what doctor
// reports missing is exactly what a verb refuses with UNSUPPORTED.
var needs = herdrclient.Needs

// ProxyHealth is the codex provider's launcher as doctor reports it. Not
// installed and down are kept apart: the first affects nothing until a codex
// profile is launched, the second breaks one that is — which is what Profiles
// says, so the claude path is never reported as an outage it is not in.
type ProxyHealth struct {
	// Binary is what would be run, as the config names it.
	Binary string `json:"binary"`
	// Profiles is every configured profile that launches through it, sorted.
	// Empty means no worker this dispatcher launches touches the proxy.
	Profiles []string `json:"profiles"`
	// Installed says the binary resolved at all; Reachable says its daemon
	// answered.
	Installed bool `json:"installed"`
	Reachable bool `json:"reachable"`
	// Account is the stored account it routes through, when it answered.
	Account string `json:"account,omitempty"`
	// Error is the proxy's own words for why it gave no answer.
	Error string `json:"error,omitempty"`
	// Quota is what the account behind it has already spent. Reachability
	// and quota are different questions with the same answer for an
	// operator whose fleet has stopped, so doctor answers both here.
	Quota QuotaHealth `json:"quota"`
}

// QuotaHealth is the proxy's quota as doctor reports it, plus the refusal a
// codex spawn would meet right now. An operator whose fleet has stopped reads
// the reason here rather than by trying a dispatch and reading the message.
type QuotaHealth struct {
	// Known is whether there is a ceiling to read at all. A metered key has
	// none, and a proxy that could not be asked left it unread; both gate
	// nothing.
	Known bool `json:"known"`
	// LimitReached is the proxy's own flag for an account that cannot pay.
	LimitReached bool `json:"limit_reached"`
	// UsedPercent is the fullest of the serving account's windows.
	UsedPercent float64 `json:"used_percent"`
	// MaxUsedPercent is the configured threshold. Zero is unset, which is
	// no threshold.
	MaxUsedPercent int `json:"max_used_percent"`
	// Account and Plan name whose quota this is.
	Account string `json:"account,omitempty"`
	Plan    string `json:"plan,omitempty"`
	// Refusal is why a codex spawn would be refused now, in the words the
	// dispatch verb would use. Empty means one would be allowed.
	Refusal string `json:"refusal,omitempty"`
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
func (d *Daemon) doctor(ctx context.Context, req protocol.Request) (DoctorReport, error) {
	rep := DoctorReport{
		Version:        d.Version,
		Contract:       version.Contract,
		Principal:      req.Caller(),
		Socket:         config.SocketPath(),
		StateDir:       config.StateDir(),
		ConfigDir:      config.ConfigDir(),
		BasePane:       d.Loop.Base(),
		MaxWorkers:     d.Loop.Policy.MaxWorkers,
		Interval:       d.Interval.String(),
		Workers:        len(d.Loop.Bindings()),
		AwaitingReview: d.Loop.AwaitingReview(),
		Pending:        len(d.Loop.Pending()),
		Bindings:       d.Loop.BindingsPath(),
		Log:            d.LogPath,
		Readopted:      d.Loop.Readopted(),
		Verify:         VerifyHealth{Enabled: d.Loop.Config.Verify.Enabled},
		Gate: GateHealth{
			Configured: d.Policy().Configured(),
			Command:    d.Policy().Command(),
			Verbs:      verbs.GatedVerbs(),
			Parked:     len(d.Loop.Parked()),
		},
		MinPaneColumns: d.Loop.Config.Layout.MinPaneColumns,
		MaxPanesPerTab: d.Loop.Config.Layout.MaxPanesPerTab,
	}
	rep.Events = d.eventsHealth()
	rep.Herdr = d.herdrHealth(ctx)
	rep.Proxy = d.proxyHealth(ctx)
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

// eventsHealth is the §8 trail as doctor reports it. A trail that cannot be
// read is reported as empty: doctor answers rather than fails, and an
// operator reading zero against a running dispatcher has the same question a
// refusal would have raised.
func (d *Daemon) eventsHealth() EventsHealth {
	out := EventsHealth{Max: store.MaxEvents}
	if d.Loop == nil {
		return out
	}
	out.Hook = d.Loop.Config.OnEvent
	if trail, err := d.Loop.Events(store.EventFilter{}); err == nil {
		out.Trail = len(trail)
	}
	return out
}

// herdrHealth is §11.2 as doctor reports it. The schema is read at daemon
// start and cached, so this normally answers off what was already seen; when
// nothing has read one yet — a daemon whose start-time read failed — it asks,
// because doctor exists to answer before a verb is tried.
func (d *Daemon) herdrHealth(ctx context.Context) HerdrHealth {
	if d.Loop == nil || d.Loop.Herdr == nil {
		return HerdrHealth{Missing: []string{}}
	}
	schema, err := d.Loop.Herdr.Schema(ctx)
	if err != nil {
		return HerdrHealth{Missing: []string{}, Error: err.Error()}
	}
	out := HerdrHealth{
		Detected: true,
		Protocol: schema.Protocol,
		Requests: len(schema.Requests),
		Events:   len(schema.Events),
		Missing:  []string{},
	}
	for _, want := range needs {
		if !schema.Has(want) {
			out.Missing = append(out.Missing, want)
		}
	}
	return out
}

// proxyHealth asks the proxy whether it is up. doctor never fails, so a proxy
// that is down or absent is the answer here rather than an error: it is
// exactly the state an operator ran doctor to learn.
func (d *Daemon) proxyHealth(ctx context.Context) ProxyHealth {
	out := ProxyHealth{Binary: config.DefaultProxy, Profiles: []string{}}
	if d.Loop == nil {
		return out
	}
	if d.Loop.Config.Proxy.Bin != "" {
		out.Binary = d.Loop.Config.Proxy.Bin
	}
	for name, p := range d.Loop.Config.Profiles {
		if p.Provider == config.ProviderCodex {
			out.Profiles = append(out.Profiles, name)
		}
	}
	sort.Strings(out.Profiles)
	if d.Loop.Spawn == nil || d.Loop.Spawn.Proxy == nil {
		return out
	}
	st, err := d.Loop.Spawn.Proxy.Status(ctx)
	if err != nil {
		out.Installed = !errors.Is(err, proxy.ErrNotInstalled)
		out.Error = err.Error()
		return out
	}
	out.Installed, out.Reachable, out.Account = true, true, st.Account
	// Asked only where a profile launches through it: a claude-only fleet
	// spends no account here, and there is nothing to report.
	if len(out.Profiles) > 0 {
		q := d.Loop.Quota(ctx)
		out.Quota = QuotaHealth{
			Known:          q.Known,
			LimitReached:   q.LimitReached,
			UsedPercent:    q.UsedPercent,
			MaxUsedPercent: d.Loop.Policy.MaxUsedPercent,
			Account:        q.Account,
			Plan:           q.Plan,
			Refusal:        d.Loop.QuotaRefusal(q),
		}
	}
	return out
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
		if err := CheckArg(v, a, raw); err != nil {
			return err
		}
	}
	return nil
}

// checkArg holds one argument to the type the registry declares for it. Both
// doors walk the same table, so it is one function rather than the same
// switch written twice.
// CheckArg is checkArg, exported for the MCP door, which publishes the schema
// this checks against and so must check the same way.
func CheckArg(v verbs.Verb, a verbs.Arg, raw any) error {
	switch a.Type {
	case verbs.Bool:
		if _, ok := raw.(bool); !ok {
			return codes.Refusef(codes.Invalid, "%s wants %s as true or false", v.Name, a.Name)
		}
	case verbs.Int:
		// A JSON number arrives as a float64 whichever door sent it, and a
		// count with a fraction on it is not a count.
		n, ok := raw.(float64)
		if !ok {
			if i, isInt := raw.(int); isInt {
				n, ok = float64(i), true
			}
		}
		if !ok || n != float64(int(n)) {
			return codes.Refusef(codes.Invalid, "%s wants %s as a whole number", v.Name, a.Name)
		}
	default:
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

// Policy is the §9 gate as this daemon is configured for it. It is built per
// call from the config the loop holds: the gate is a command name and a
// timeout, so there is nothing to keep alive between calls, and reading it
// here means a reloaded config takes effect without a second copy to keep in
// step.
func (d *Daemon) Policy() *gate.Gate {
	if d.Loop == nil {
		return gate.New(nil)
	}
	return gate.New(d.Loop.Config.GateCommand)
}

// pass is §9.1: every verb that changes the world goes through one gate
// before doing anything. A verb with no §9.4 name passes nothing, and the
// registry makes that a decision written down beside the verb rather than an
// omission.
//
// The subject is the caller as the daemon records it — the pane a door runs
// in, `human` for a CLI invocation (§3.6) and for a door started with the
// §7.5 declaration, and the `none` of §3.7 for a caller with neither. This binary
// derives no principal and grants nothing for a pane (§3.4), so what the gate
// is told is what the daemon knows, and never more.
func (d *Daemon) pass(v verbs.Verb, req protocol.Request) error {
	if v.Gated == "" {
		return nil
	}
	target, _ := req.Args["task"].(string)
	res := d.Policy().Check(gate.Request{Subject: req.Caller(), Verb: v.Gated, Target: target})
	switch res.Decision {
	case gate.Deny:
		d.logf("the policy gate refused %s for %s: %s", v.Gated, req.Caller(), res.Reason)
		return codes.Errorf(codes.Denied, "the policy gate refused %s: %s", v.Gated, res.Reason)
	case gate.Defer:
		// §9.3: park it. The call is recorded, not performed, and the
		// caller is told DENIED with the row to name.
		id, err := d.Loop.Park(store.Parked{
			Subject: req.Caller(),
			Verb:    v.Gated,
			Target:  target,
			Payload: req.Args,
			// The scope travels with the call: it is not an argument, and a
			// re-run without it would look on boards the caller never named.
			Project:     req.Project,
			AllProjects: req.AllProjects,
			Reason:      res.Reason,
		})
		if err != nil {
			return err
		}
		d.logf("the policy gate parked %s for %s as %s: %s", v.Gated, req.Caller(), id, res.Reason)
		return codes.Parked(id, "the policy gate parked %s for the operator: %s", v.Gated, res.Reason)
	}
	return nil
}

// DumpReport is §5.8: the whole store in one document, with the file it is
// written to named, so a reader who wants it without this binary knows where
// to look.
type DumpReport struct {
	Version      int                 `json:"version"`
	Path         string              `json:"path"`
	Bindings     []decide.Binding    `json:"bindings"`
	Reservations []store.Reservation `json:"reservations"`
	Parked       []store.Parked      `json:"parked"`
	// Events is the §8.1 trail. It is part of the store, so §5.8's "the
	// whole store" includes it: a reader who wants the document without
	// this binary should not have to know that one list was held back.
	Events []store.Event `json:"events"`
}

// ParkedReport is what parked_list answers with.
type ParkedReport struct {
	Parked []store.Parked `json:"parked"`
	Count  int            `json:"count"`
}

// ParkedResolution says what became of one parked action.
type ParkedResolution struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Result is what the re-run verb answered, absent when the action was
	// refused or the verb failed.
	Result json.RawMessage `json:"result,omitempty"`
}

// resolveParked is the operator overruling the gate. §3.7 makes that advice an
// agent confirms rather than a refusal this door makes, so any caller reaches
// it — and the row records WHO, because §9.3 re-runs the verb under the
// ORIGINAL subject and the trail would otherwise name only the caller the gate
// stopped and no one who decided it could proceed.
func (d *Daemon) resolveParked(ctx context.Context, req protocol.Request) (ParkedResolution, error) {
	id, _ := req.Args["id"].(string)
	reject, _ := req.Args["reject"].(bool)
	state := store.ParkedResolved
	if reject {
		state = store.ParkedRefused
	}
	// The move is the one-winner check and it happens BEFORE the verb runs:
	// putting it after left a window in which two resolves both read the row
	// as waiting and both ran the verb, with the side effect really happening
	// twice and the loser told CONFLICT for work that had already committed.
	was, err := d.Loop.ClaimParked(id, state, req.Caller())
	if err != nil {
		return ParkedResolution{}, err
	}
	if reject {
		d.logf("%s refused the parked %s (%s)", req.Caller(), was.Verb, id)
		return ParkedResolution{ID: id, State: state}, nil
	}
	verb, ok := verbFromGated(was.Verb)
	if !ok {
		return ParkedResolution{}, codes.Refusef(codes.Invalid,
			"parked verb %q is not a verb of this plugin", was.Verb)
	}
	// §9.3: the verb re-runs under the subject the gate stopped, never the
	// resolver's. The gate is not consulted again — the resolution IS the
	// decision it deferred, and asking a second time would park it forever.
	rerun := protocol.Request{
		Verb: verb.Name, Args: was.Payload, Door: req.Door,
		Project: was.Project, AllProjects: was.AllProjects,
	}
	if pane, found := strings.CutPrefix(was.Subject, "agent:"); found {
		rerun.Pane = pane
	}
	if rerun.Args == nil {
		rerun.Args = map[string]any{}
	}
	d.logf("%s resolved the parked %s (%s), re-running it as %s", req.Caller(), was.Verb, id, was.Subject)
	out, err := d.serve(ctx, verb, rerun)
	if err != nil {
		// The decision stands; the verb did not run. Say why, in the verb's
		// own words, and leave the row saying so.
		d.Loop.FailParked(id, message(err))
		return ParkedResolution{}, err
	}
	return ParkedResolution{ID: id, State: state, Result: out}, nil
}

// verbFromGated maps a §9.4 gate name back to the verb it belongs to.
func verbFromGated(gated string) (verbs.Verb, bool) {
	for _, v := range verbs.All {
		if v.Gated != "" && v.Gated == gated {
			return v, true
		}
	}
	return verbs.Verb{}, false
}
