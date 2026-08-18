// Package backend selects the platform adapter and exposes its capability
// set (design §7, §19). Backends return capabilities; missing capabilities
// fail before execution.
package backend

import (
	"fmt"
	"runtime"
)

// Kind identifies a backend implementation.
type Kind string

const (
	// KindNative is the Linux native rootless Podman backend (§7.2).
	KindNative Kind = "native-podman"
	// KindMachine is the macOS Podman Machine backend (§7.3).
	KindMachine Kind = "podman-machine"
)

// Selector values accepted in runtime.backend and --backend.
const (
	SelectorAuto          = "auto"
	SelectorNative        = "native-podman"
	SelectorPodmanMachine = "podman-machine"
)

// Capability names a behavior a backend can provide (§7, §11, §19).
type Capability string

const (
	// CapRootless: runs rootless Podman.
	CapRootless Capability = "rootless"
	// CapHostWorkspaceMount: host directories can be mounted into the
	// container (bind mounts / machine volume sharing).
	CapHostWorkspaceMount Capability = "host-workspace-mount"
	// CapKeepID: --userns=keep-id is available.
	CapKeepID Capability = "userns-keep-id"
	// CapDirectPorts: ports publish directly on the host (Linux native).
	CapDirectPorts Capability = "ports-direct"
	// CapForwardedPorts: ports are forwarded through the VM (Podman
	// Machine); user still reaches listen:published.
	CapForwardedPorts Capability = "ports-forwarded"
)

// Capabilities is a set of capability names.
type Capabilities map[Capability]bool

// Has reports whether the capability is present.
func (c Capabilities) Has(cap Capability) bool { return c[cap] }

// Backend describes the selected platform adapter.
type Backend struct {
	Kind     Kind
	Name     string
	Caps     Capabilities
	DataRoot string // backend data root (§9.2)

	// MachineName is set for podman-machine; the name of the VM.
	MachineName string
}

// Require fails with a descriptive error if any capability is missing
// (design §19: "Backend 返回能力集合,缺失能力在执行前失败").
func (b Backend) Require(caps ...Capability) error {
	for _, c := range caps {
		if !b.Caps.Has(c) {
			return fmt.Errorf("backend %s lacks required capability %s", b.Kind, c)
		}
	}
	return nil
}

// UnsupportedError reports platforms v0 does not support.
type UnsupportedError struct{ GOOS string }

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("worksync v0 does not support %s; use Linux (native-podman) or macOS (podman-machine)", e.GOOS)
}

// Detect resolves the backend for the given platform and selector.
// selector is one of auto|native-podman|podman-machine (design §7.1).
func Detect(goos, selector string) (Backend, error) {
	if selector == "" || selector == SelectorAuto {
		switch goos {
		case "linux":
			selector = SelectorNative
		case "darwin":
			selector = SelectorPodmanMachine
		default:
			return Backend{}, &UnsupportedError{GOOS: goos}
		}
	}
	switch selector {
	case SelectorNative:
		if goos != "linux" {
			return Backend{}, fmt.Errorf("native-podman backend requires Linux (running on %s)", goos)
		}
		return Backend{
			Kind: KindNative,
			Name: "Linux native rootless Podman",
			Caps: Capabilities{
				CapRootless:           true,
				CapHostWorkspaceMount: true,
				CapKeepID:             true,
				CapDirectPorts:        true,
			},
		}, nil
	case SelectorPodmanMachine:
		if goos != "darwin" {
			return Backend{}, fmt.Errorf("podman-machine backend requires macOS (running on %s)", goos)
		}
		return Backend{
			Kind: KindMachine,
			Name: "macOS Podman Machine",
			Caps: Capabilities{
				CapRootless:           true,
				CapHostWorkspaceMount: true,
				CapKeepID:             true,
				CapForwardedPorts:     true,
			},
		}, nil
	default:
		return Backend{}, fmt.Errorf("unknown backend %q", selector)
	}
}

// AutoDetect resolves the backend for the current process platform.
func AutoDetect(selector string) (Backend, error) {
	return Detect(runtime.GOOS, selector)
}
