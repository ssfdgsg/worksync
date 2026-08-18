package refs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func c(n int) string {
	s := strings.Repeat("0123456789abcdef", 4)[:62]
	return s + fmt.Sprintf("%02d", n)
}

func dg(prefix string) string { return "sha256:" + prefix }

func TestNewAndAdvance(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r1, err := New(dg(c(1)), now)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Previous != "" {
		t.Errorf("first ref should have no previous, got %q", r1.Previous)
	}
	r2, err := Advance(r1, r1.Commit, dg(c(2)), now)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Previous != r1.Commit {
		t.Errorf("previous = %q, want %q", r2.Previous, r1.Commit)
	}
	if !CanFastForward(r1, r2) {
		t.Error("expected fast-forward")
	}
}

func TestAdvanceConflict(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r1, _ := New(dg(c(1)), now)
	r2, _ := New(dg(c(2)), now)
	// We last observed r1, but the ref moved to r2 elsewhere.
	_, err := Advance(r2, r1.Commit, dg(c(3)), now)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestAdvanceFromNonexistent(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	empty := Ref{}
	r, err := Advance(empty, "", dg(c(4)), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Previous != "" {
		t.Errorf("previous should be empty, got %q", r.Previous)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	r, _ := New(dg(c(5)), now)
	b, err := r.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Commit != r.Commit {
		t.Errorf("commit = %q, want %q", parsed.Commit, r.Commit)
	}
	if !parsed.UpdatedAt.Equal(r.UpdatedAt) {
		t.Errorf("updatedAt = %v, want %v", parsed.UpdatedAt, r.UpdatedAt)
	}
}

func TestJSONRejectsUnknownField(t *testing.T) {
	b := `{"commit":"sha256:` + c(1) + `","updatedAt":"2026-08-17T12:00:00Z","force":true}`
	if _, err := ParseJSON([]byte(b)); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestValidateRejectsBadDigest(t *testing.T) {
	if _, err := New("sha256:zz", time.Now()); err == nil {
		t.Fatal("expected bad digest error")
	}
	if _, err := New("plain", time.Now()); err == nil {
		t.Fatal("expected missing-algorithm error")
	}
}
