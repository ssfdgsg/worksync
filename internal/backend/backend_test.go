package backend

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectAutoLinux(t *testing.T) {
	b, err := Detect("linux", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != KindNative {
		t.Errorf("kind = %s", b.Kind)
	}
	if !b.Caps.Has(CapRootless) || !b.Caps.Has(CapDirectPorts) || !b.Caps.Has(CapHostWorkspaceMount) {
		t.Errorf("linux caps missing: %+v", b.Caps)
	}
}

func TestDetectAutoDarwin(t *testing.T) {
	b, err := Detect("darwin", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != KindMachine {
		t.Errorf("kind = %s", b.Kind)
	}
	if !b.Caps.Has(CapForwardedPorts) {
		t.Errorf("darwin caps wrong: %+v", b.Caps)
	}
	if b.Caps.Has(CapDirectPorts) {
		t.Errorf("darwin machine should not expose direct ports: %+v", b.Caps)
	}
}

func TestDetectAutoUnsupported(t *testing.T) {
	_, err := Detect("windows", "auto")
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Fatalf("want UnsupportedError, got %v", err)
	}
}

func TestDetectExplicitOverride(t *testing.T) {
	if _, err := Detect("linux", "podman-machine"); err == nil {
		t.Fatal("expected machine-on-linux rejection")
	}
	if _, err := Detect("darwin", "native-podman"); err == nil {
		t.Fatal("expected native-on-darwin rejection")
	}
	b, err := Detect("linux", "native-podman")
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != KindNative {
		t.Errorf("kind = %s", b.Kind)
	}
}

func TestDetectUnknownKind(t *testing.T) {
	if _, err := Detect("linux", "qemu"); err == nil || !strings.Contains(err.Error(), "qemu") {
		t.Fatalf("want unknown-backend error, got %v", err)
	}
}

func TestRequire(t *testing.T) {
	b, _ := Detect("linux", "auto")
	if err := b.Require(CapRootless, CapDirectPorts); err != nil {
		t.Errorf("linux should have these caps: %v", err)
	}
	if err := b.Require(CapForwardedPorts); err == nil {
		t.Error("linux should lack forwarded-ports cap")
	}
}
