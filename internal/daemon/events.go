package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/codes"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/protocol"
	"github.com/husniadil/herdr-dispatch/internal/store"
)

// EventsReport is what `events` answers with when it is not following: the
// slice of the trail that was asked for, and how many that was.
type EventsReport struct {
	Events []store.Event `json:"events"`
	Count  int           `json:"count"`
}

// FollowPoll is how often a follower re-reads the trail when nothing has
// woken it. Every write wakes it directly, so this is the backstop that keeps
// a missed wake-up from being a stream that never speaks again.
const FollowPoll = time.Second

// filter reads the §8.2 selection out of a request's arguments.
func filter(args map[string]any) store.EventFilter {
	f := store.EventFilter{}
	if since, _ := args["since"].(string); since != "" {
		// A number is a millisecond and anything else is an id. An id is
		// this plugin's own `ev-<ms>-<hex>`, which never parses as one.
		if ms, err := strconv.ParseInt(since, 10, 64); err == nil {
			f.SinceMS = ms
		} else {
			f.SinceID = since
		}
	}
	switch limit := args["limit"].(type) {
	case float64:
		f.Limit = int(limit)
	case int:
		f.Limit = limit
	}
	return f
}

// events is one bounded read of the trail.
func (d *Daemon) events(req protocol.Request) (EventsReport, error) {
	evs, err := d.Loop.Events(filter(req.Args))
	if err != nil {
		return EventsReport{}, err
	}
	return EventsReport{Events: evs, Count: len(evs)}, nil
}

// stream is `events --follow`, the subscription primitive of §8.2. There is no
// push bus in this revision: it is a read of the trail, woken by a write.
//
// A limit ends the stream on purpose, with Done, so a bounded follower can
// tell a stream that finished from a daemon that died.
func (d *Daemon) stream(ctx context.Context, req protocol.Request, conn interface{ Close() error }, enc *json.Encoder) {
	f := filter(req.Args)
	left, bounded := f.Limit, f.Limit > 0
	// The limit bounds the STREAM rather than each read of it: a follower
	// asking for ten events wants the next ten however they arrive.
	f.Limit = 0

	woken := d.watch()
	defer d.unwatch(woken)
	poll := time.NewTicker(FollowPoll)
	defer poll.Stop()
	for {
		evs, err := d.Loop.Events(f)
		if err != nil {
			enc.Encode(protocol.Response{Error: &protocol.Failure{
				Code: string(codes.Of(err)), Message: message(err)}})
			return
		}
		for _, ev := range evs {
			raw, merr := json.Marshal(ev)
			if merr != nil {
				continue
			}
			if err := enc.Encode(protocol.Response{Result: raw}); err != nil {
				// The follower went away. Nothing is owed to a closed
				// connection.
				return
			}
			// The next read resumes strictly after what was just written,
			// by id, so a wake-up that arrives mid-write repeats nothing.
			f.SinceID, f.SinceMS = ev.ID, 0
			if bounded {
				if left--; left == 0 {
					enc.Encode(protocol.Response{Done: true})
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			enc.Encode(protocol.Response{Done: true})
			return
		case <-woken:
		case <-poll.C:
		}
	}
}

// watchers is every live `events --follow`, each with a channel it is woken
// on. A wake-up is news and never a payload: the stream re-reads the trail,
// so a wake-up that is dropped because one is already pending says exactly
// what a second one would.
type watchers struct {
	mu   sync.Mutex
	live map[chan struct{}]struct{}
}

func (w *watchers) add() chan struct{} {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live == nil {
		w.live = map[chan struct{}]struct{}{}
	}
	w.live[ch] = struct{}{}
	return ch
}

func (w *watchers) remove(ch chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.live, ch)
}

func (w *watchers) wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.live {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (d *Daemon) watch() chan struct{}     { return d.followers.add() }
func (d *Daemon) unwatch(ch chan struct{}) { d.followers.remove(ch) }

// Emitted is what the loop calls after every event it writes. It fires the
// §8.3 hook and wakes every follower, in that order, and neither can fail the
// write: it has already happened.
func (d *Daemon) Emitted(ev store.Event) {
	d.runHook(ev)
	d.followers.wake()
}

// runHook fires the configured event hook, detached with all three stdio
// closed (§8.3). A hook that fails MUST NOT fail the write that caused it, so
// nothing here is waited on and nothing is returned.
func (d *Daemon) runHook(ev store.Event) {
	hook := d.Loop.Config.OnEvent
	if len(hook) == 0 {
		return
	}
	cmd := exec.Command(hook[0], hook[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	// Its own session, so a hook outlives the tick that fired it and takes
	// nothing with it when the daemon goes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(),
		config.HookEventVar+"="+ev.Name,
		config.HookEntityVar+"="+ev.Entity,
		config.HookIDVar+"="+ev.EntityID,
		config.HookProjectVar+"="+ev.Project,
		config.HookActorVar+"="+ev.Actor,
		config.HookKindVar+"="+ev.Kind,
		config.HookAtVar+"="+fmt.Sprint(ev.AtMS),
	)
	if detail, err := json.Marshal(ev.Detail); err == nil && ev.Detail != nil {
		// The one key beyond §8.3's list, and the reason is the same one
		// §8.3 gives for `detail`: what a hook wants to act on — which pane,
		// which task number, why a prompt was refused — is in there and
		// nowhere else in the environment.
		cmd.Env = append(cmd.Env, config.HookDetailVar+"="+string(detail))
	}
	if err := cmd.Start(); err != nil {
		d.logf("the event hook %s did not run for %s: %v", hook[0], ev.Name, err)
		return
	}
	// Nothing waits for it, so let the kernel reap it rather than leaving a
	// zombie behind the tick.
	go cmd.Wait()
}
