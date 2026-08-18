// Package refs implements the mutable Ref object with compare-and-swap
// semantics (design §15).
package refs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"worksync/internal/digest"
)

// Ref is a mutable name pointing at a commit digest (design §15).
type Ref struct {
	Commit    string    `json:"commit"`
	Previous  string    `json:"previous,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ErrConflict is returned when a compare-and-swap observes that the remote
// (or local) ref has moved since it was last read (design §15: no silent
// overwrite).
var ErrConflict = errors.New("ref conflict: ref has moved; pull first or tag a new ref")

// ErrInvalid is returned for malformed ref JSON.
var ErrInvalid = errors.New("invalid ref")

// New creates a ref pointing at commit with no parent.
func New(commit string, now time.Time) (Ref, error) {
	r := Ref{Commit: commit, UpdatedAt: now.UTC()}
	if err := r.Validate(); err != nil {
		return Ref{}, err
	}
	return r, nil
}

// Advance returns next with Previous linked to current, failing with
// ErrConflict unless current.Commit equals expectedCommit (the value the
// caller last observed). When expectedCommit is "" the caller asserts the ref
// did not exist before.
func Advance(current Ref, expectedCommit, nextCommit string, now time.Time) (Ref, error) {
	if current.Commit != expectedCommit {
		return Ref{}, fmt.Errorf("%w: saw %q, expected %q", ErrConflict, current.Commit, expectedCommit)
	}
	next := Ref{Commit: nextCommit, Previous: current.Commit, UpdatedAt: now.UTC()}
	if err := next.Validate(); err != nil {
		return Ref{}, err
	}
	return next, nil
}

// CanFastForward reports whether current can advance to next without
// conflict, i.e. next's parent chain reaches current.
func CanFastForward(current, next Ref) bool {
	return next.Previous == current.Commit
}

// Validate checks that commit is a well-formed digest.
func (r Ref) Validate() error {
	if r.Commit == "" {
		return fmt.Errorf("%w: empty commit", ErrInvalid)
	}
	if _, err := digest.Parse(r.Commit); err != nil {
		return fmt.Errorf("%w: commit %q: %v", ErrInvalid, r.Commit, err)
	}
	if r.Previous != "" {
		if _, err := digest.Parse(r.Previous); err != nil {
			return fmt.Errorf("%w: previous %q: %v", ErrInvalid, r.Previous, err)
		}
	}
	return nil
}

// MarshalJSON implements json.Marshaler with validation.
func (r Ref) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type alias Ref
	return json.Marshal(alias(r))
}

// UnmarshalJSON implements json.Unmarshaler with strict field checking.
func (r *Ref) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	type alias Ref
	var a alias
	if err := dec.Decode(&a); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	*r = Ref(a)
	return r.Validate()
}

// ParseJSON decodes and validates a ref from its JSON representation.
func ParseJSON(b []byte) (Ref, error) {
	var r Ref
	if err := r.UnmarshalJSON(b); err != nil {
		return Ref{}, err
	}
	return r, nil
}
