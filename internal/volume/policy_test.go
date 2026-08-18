package volume

import (
	"reflect"
	"testing"
)

func TestDefaultCommitSelection(t *testing.T) {
	vols := map[string]Policy{
		"workspace":  Tracked,
		"home":       Persistent,
		"npm-cache":  Cache,
		"dsh-config": Tracked,
		"secret":     Secret,
		"scratch":    Ephemeral,
	}
	sel, err := SelectCommit(vols, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Selection{"dsh-config", "workspace"}
	if !reflect.DeepEqual(sel, want) {
		t.Errorf("SelectCommit = %v, want %v", sel, want)
	}
	push, err := SelectPush(vols, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(push, want) {
		t.Errorf("SelectPush = %v, want %v", push, want)
	}
}

func TestExplicitSelectionOverrides(t *testing.T) {
	vols := map[string]Policy{
		"workspace": Tracked,
		"db":        Persistent,
	}
	// Explicitly include a persistent volume.
	sel, err := SelectCommit(vols, []string{"db", "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	want := Selection{"db", "workspace"}
	if !reflect.DeepEqual(sel, want) {
		t.Errorf("SelectCommit = %v, want %v", sel, want)
	}
	// Explicitly exclude the tracked volume entirely.
	sel2, err := SelectCommit(vols, []string{"db"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sel2, Selection{"db"}) {
		t.Errorf("SelectCommit = %v", sel2)
	}
}

func TestExplicitSelectionRejectsUnknown(t *testing.T) {
	vols := map[string]Policy{"workspace": Tracked}
	if _, err := SelectCommit(vols, []string{"nope"}); err == nil {
		t.Fatal("expected unknown-volume error")
	}
}

func TestExplicitSelectionRejectsCacheAndSecret(t *testing.T) {
	vols := map[string]Policy{"npm": Cache, "tok": Secret}
	if _, err := SelectCommit(vols, []string{"npm"}); err == nil {
		t.Fatal("expected cache rejection")
	}
	if _, err := SelectCommit(vols, []string{"tok"}); err == nil {
		t.Fatal("expected secret rejection")
	}
}

func TestPersistence(t *testing.T) {
	if !Tracked.PersistedAcrossRestarts() || !Persistent.PersistedAcrossRestarts() || !Cache.PersistedAcrossRestarts() {
		t.Error("tracked/persistent/cache should persist")
	}
	if Secret.PersistedAcrossRestarts() || Ephemeral.PersistedAcrossRestarts() {
		t.Error("secret/ephemeral should not persist")
	}
}

func TestPolicyValid(t *testing.T) {
	for _, p := range All {
		if !p.Valid() {
			t.Errorf("%s should be valid", p)
		}
	}
	if Policy("nope").Valid() {
		t.Error("nope should be invalid")
	}
}
