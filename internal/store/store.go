// Package store is the one thing the dispatcher remembers across restarts:
// the bindings.
//
// A binding — which pane was prompted for which task, when, how often, and
// whether review was already announced — exists nowhere else until the worker
// claims. Everything else the dispatcher acts on is a board fact or a Herdr
// fact, read fresh every tick, and none of it is written here.
//
// The plugin contract's §5.1 store is SQLite. This is a JSON document
// instead, and the README records why: the whole set is a handful of rows
// held in memory anyway, rewritten whole on every change, read by one
// process under the daemon's own lock. A SQLite driver is a large dependency
// for a file that never needs a query, a schema or a second reader, and this
// repo's budget is the standard library until a dependency earns its way in.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/husniadil/herdr-dispatch/internal/decide"
)

// Version is the document's shape. A document from a version this binary
// does not know is refused rather than guessed at.
const Version = 1

// Bindings is the document at Path.
type Bindings struct{ Path string }

// Reservation is one task an on-demand dispatch took and no tick has spawned
// for yet, and the daemon that took it.
//
// Owner is what a restart reads to tell its own stale reservation from a
// live peer's: it is the board principal the reserving daemon writes with,
// which carries that daemon's pane. A reservation with no owner recorded is
// one no restart can attribute, which is the state this exists to end.
type Reservation struct {
	TaskID string
	Owner  string
	At     time.Time
}

// State is everything the dispatcher remembers across a restart: the
// bindings, and the reservations that have not become bindings yet. They
// share one document because it is written whole, so saving either can never
// drop the other.
type State struct {
	Bindings     []decide.Binding
	Reservations []Reservation
}

type document struct {
	Version  int      `json:"version"`
	Bindings []record `json:"bindings"`
	// Reservations is omitted when there are none, so a document this
	// binary writes stays readable to one that predates them.
	Reservations []reservation `json:"reservations,omitempty"`
}

// reservation is one reservation as it is written.
type reservation struct {
	TaskID string `json:"task"`
	Owner  string `json:"owner"`
	AtMS   int64  `json:"at_ms"`
}

// record is one binding as it is written. Times are Unix milliseconds, the
// one form the contract allows a store to hold.
type record struct {
	TaskID       string `json:"task"`
	Pane         string `json:"pane"`
	PromptedAtMS int64  `json:"prompted_at_ms"`
	Prompts      int    `json:"prompts"`
	Notified     bool   `json:"notified"`
	// Kind is the lane the pane was brought up for. It is omitted for a
	// worker, which is the only lane there is; a document written while the
	// verifier lane existed may still carry one, and Load drops it.
	Kind string `json:"kind,omitempty"`
	// Verified says the submission this binding is holding has had its
	// self-review shot.
	Verified bool `json:"verified,omitempty"`
	// Worktree is the checkout the pane works in. The binding is the only
	// record of where it is, so a binding that comes back without it leaves
	// a live worker's tree with nothing naming it: the retire cannot
	// remove it, and a startup reap would take it while the worker is
	// still working in it.
	Worktree string `json:"worktree,omitempty"`
	// Tab is the tab the pane was opened in, so a restart can close it when
	// its last worker leaves.
	Tab string `json:"tab,omitempty"`
	// Branch is where a worker's commits are. The checkout is removed when
	// the pane is retired and the branch is not, so a binding that comes
	// back without it leaves an operator with no name for the work.
	Branch string `json:"branch,omitempty"`
}

// Load reads the bindings. A store nobody has written is an empty set and no
// error; a document that cannot be read is an empty set AND an error, so a
// torn write reaches the operator without keeping the daemon from starting.
func (b *Bindings) Load() (State, error) {
	raw, err := os.ReadFile(b.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read the bindings at %s: %w", b.Path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return State{}, fmt.Errorf("the bindings at %s cannot be read: %w", b.Path, err)
	}
	if doc.Version != Version {
		return State{}, fmt.Errorf("the bindings at %s are version %d and this hdis knows %d",
			b.Path, doc.Version, Version)
	}
	out := make([]decide.Binding, 0, len(doc.Bindings))
	for _, r := range doc.Bindings {
		// A document written while the verifier lane existed may name a
		// verifier pane. Nothing drives one now, so the record is debris
		// rather than a binding this daemon can honour.
		if r.Kind != "" && r.Kind != decide.KindWorker {
			continue
		}
		out = append(out, decide.Binding{
			TaskID:     r.TaskID,
			Pane:       r.Pane,
			PromptedAt: time.UnixMilli(r.PromptedAtMS).UTC(),
			Prompts:    r.Prompts,
			Notified:   r.Notified,
			Kind:       r.Kind,
			Verified:   r.Verified,
			Worktree:   r.Worktree,
			Tab:        r.Tab,
			Branch:     r.Branch,
		})
	}
	held := make([]Reservation, 0, len(doc.Reservations))
	for _, r := range doc.Reservations {
		held = append(held, Reservation{
			TaskID: r.TaskID,
			Owner:  r.Owner,
			At:     time.UnixMilli(r.AtMS).UTC(),
		})
	}
	return State{Bindings: out, Reservations: held}, nil
}

// Save writes the whole set, atomically: a temp file in the same directory,
// flushed, then renamed over the old one. A reader sees the previous
// document or the new one and never half of either, and a crash mid-write
// leaves the previous one intact.
func (b *Bindings) Save(state State) error {
	doc := document{Version: Version, Bindings: make([]record, 0, len(state.Bindings))}
	for _, x := range state.Bindings {
		doc.Bindings = append(doc.Bindings, record{
			TaskID:       x.TaskID,
			Pane:         x.Pane,
			PromptedAtMS: x.PromptedAt.UnixMilli(),
			Prompts:      x.Prompts,
			Notified:     x.Notified,
			// A worker's kind is left off: an absent kind reads as one,
			// which is what every binding is.
			Verified: x.Verified,
			Worktree: x.Worktree,
			Tab:      x.Tab,
			Branch:   x.Branch,
		})
	}
	for _, x := range state.Reservations {
		doc.Reservations = append(doc.Reservations, reservation{
			TaskID: x.TaskID,
			Owner:  x.Owner,
			AtMS:   x.At.UnixMilli(),
		})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode the bindings: %w", err)
	}

	dir := filepath.Dir(b.Path)
	tmp, err := os.CreateTemp(dir, filepath.Base(b.Path)+".*")
	if err != nil {
		return fmt.Errorf("write the bindings in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("close the bindings to other users: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write the bindings to %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush the bindings to %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Rename(name, b.Path); err != nil {
		return fmt.Errorf("put the bindings in place at %s: %w", b.Path, err)
	}
	return nil
}
