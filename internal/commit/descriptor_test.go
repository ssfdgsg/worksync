package commit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"worksync/internal/digest"
)

func testDescriptor() Descriptor {
	return Descriptor{
		SchemaVersion: 1,
		Project:       "dsh-dev",
		Platform:      Platform{OS: "linux", Architecture: "arm64"},
		Environment: EnvironmentRef{
			Base:  "docker.io/library/node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Image: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		Snapshots: map[string]string{
			"workspace":  "restic:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"dsh-config": "restic:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
		ConfigDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Parent:       "",
		Message:      "configured dsh",
		CreatedAt:    time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
	}
}

func TestCanonicalDigestDeterministic(t *testing.T) {
	d1 := testDescriptor()
	d2 := testDescriptor()
	// shuffle map iteration order by re-inserting in reverse
	for k, v := range d1.Snapshots {
		delete(d2.Snapshots, k)
		d2.Snapshots[k] = v
	}
	b1, err := MarshalCanonical(&d1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := MarshalCanonical(&d2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("canonical JSON not deterministic:\n%s\nvs\n%s", b1, b2)
	}
	dg1, _ := d1.Digest()
	dg2, _ := d2.Digest()
	if dg1 != dg2 {
		t.Errorf("digests differ: %s vs %s", dg1, dg2)
	}
	_ = d1
	if err := dg1.Validate(); err != nil {
		t.Errorf("digest invalid: %v", err)
	}
}

func TestCanonicalJSONSortedKeys(t *testing.T) {
	d := testDescriptor()
	b, err := MarshalCanonical(&d)
	if err != nil {
		t.Fatal(err)
	}
	// Keys of the top-level object must be sorted, in the byte stream itself.
	var keys []string
	dec := json.NewDecoder(strings.NewReader(string(b)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("expected object start, got %v (%v)", tok, err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("expected string key, got %v", tok)
		}
		keys = append(keys, key)
		var val interface{}
		if err := dec.Decode(&val); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] < keys[i-1] {
			t.Errorf("keys not sorted: %v", keys)
		}
	}
	// Canonical JSON must already be compact: json.Compact must be a no-op.
	var compact bytes.Buffer
	if err := json.Compact(&compact, b); err != nil {
		t.Fatal(err)
	}
	if compact.String() != string(b) {
		t.Errorf("canonical JSON not compact: %s", b)
	}
}

func TestParseJSONRoundTrip(t *testing.T) {
	d := testDescriptor()
	b, err := MarshalCanonical(&d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Project != d.Project || parsed.Message != d.Message {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}
	if len(parsed.Snapshots) != 2 {
		t.Errorf("snapshots lost: %+v", parsed.Snapshots)
	}
}

func TestParseJSONRejectsUnknownField(t *testing.T) {
	d := testDescriptor()
	b, _ := MarshalCanonical(&d)
	s := strings.TrimSuffix(string(b), "}") + `,"bogus":1}`
	if _, err := ParseJSON([]byte(s)); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestValidateRejectsBadSnapshotRef(t *testing.T) {
	d := testDescriptor()
	d.Snapshots["workspace"] = "s3://bucket/key"
	if err := d.Validate(); err == nil {
		t.Fatal("expected bad snapshot ref error")
	}
}

func TestValidateRejectsBadParent(t *testing.T) {
	d := testDescriptor()
	d.Parent = "not-a-digest"
	if err := d.Validate(); err == nil {
		t.Fatal("expected bad parent error")
	}
}

func TestDigestStableValue(t *testing.T) {
	d := testDescriptor()
	dg, err := d.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(dg), "sha256:") {
		t.Errorf("digest prefix wrong: %s", dg)
	}
	if len(string(dg)) != len("sha256:")+64 {
		t.Errorf("digest length wrong: %s", dg)
	}
}

var _ = digest.SHA256 // keep import
