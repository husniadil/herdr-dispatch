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

type document struct {
	Version  int      `json:"version"`
	Bindings []record `json:"bindings"`
}

// record is one binding as it is written. Times are Unix milliseconds, the
// one form the contract allows a store to hold.
type record struct {
	TaskID       string `json:"task"`
	Pane         string `json:"pane"`
	PromptedAtMS int64  `json:"prompted_at_ms"`
	Prompts      int    `json:"prompts"`
	Notified     bool   `json:"notified"`
	// Kind is which lane the pane was brought up for. It is omitted for a
	// worker, so a document this binary writes stays readable to one that
	// predates the verification lane.
	Kind string `json:"kind,omitempty"`
	// Verified says a verifier has already been brought up for the
	// submission a worker's binding is holding.
	Verified bool `json:"verified,omitempty"`
}

// Load reads the bindings. A store nobody has written is an empty set and no
// error; a document that cannot be read is an empty set AND an error, so a
// torn write reaches the operator without keeping the daemon from starting.
func (b *Bindings) Load() ([]decide.Binding, error) {
	raw, err := os.ReadFile(b.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the bindings at %s: %w", b.Path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the bindings at %s cannot be read: %w", b.Path, err)
	}
	if doc.Version != Version {
		return nil, fmt.Errorf("the bindings at %s are version %d and this hdis knows %d",
			b.Path, doc.Version, Version)
	}
	out := make([]decide.Binding, 0, len(doc.Bindings))
	for _, r := range doc.Bindings {
		out = append(out, decide.Binding{
			TaskID:     r.TaskID,
			Pane:       r.Pane,
			PromptedAt: time.UnixMilli(r.PromptedAtMS).UTC(),
			Prompts:    r.Prompts,
			Notified:   r.Notified,
			Kind:       r.Kind,
			Verified:   r.Verified,
		})
	}
	return out, nil
}

// kindOf writes a verifier's kind and leaves a worker's off: an absent kind
// is a worker, which is what every binding was before the lane existed.
func kindOf(b decide.Binding) string {
	if b.IsVerifier() {
		return decide.KindVerifier
	}
	return ""
}

// Save writes the whole set, atomically: a temp file in the same directory,
// flushed, then renamed over the old one. A reader sees the previous
// document or the new one and never half of either, and a crash mid-write
// leaves the previous one intact.
func (b *Bindings) Save(bindings []decide.Binding) error {
	doc := document{Version: Version, Bindings: make([]record, 0, len(bindings))}
	for _, x := range bindings {
		doc.Bindings = append(doc.Bindings, record{
			TaskID:       x.TaskID,
			Pane:         x.Pane,
			PromptedAtMS: x.PromptedAt.UnixMilli(),
			Prompts:      x.Prompts,
			Notified:     x.Notified,
			Kind:         kindOf(x),
			Verified:     x.Verified,
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
