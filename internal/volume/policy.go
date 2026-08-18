// Package volume defines volume policies and commit/push selection
// semantics per design §10.
package volume

import (
	"fmt"
	"sort"
)

// Policy is one of the five volume lifecycle policies (design §10 table).
type Policy string

const (
	// Tracked volumes persist locally, are committed by default and pushed
	// by default: workspace, important config.
	Tracked Policy = "tracked"
	// Persistent volumes persist locally but are not committed or pushed by
	// default: databases, optional durable data.
	Persistent Policy = "persistent"
	// Cache volumes persist locally, never committed or pushed: node_modules,
	// package/build caches.
	Cache Policy = "cache"
	// Secret volumes exist only while the container runs, never committed or
	// pushed: tokens, keys, credentials.
	Secret Policy = "secret"
	// Ephemeral volumes are scratch space, dropped on stop.
	Ephemeral Policy = "ephemeral"
)

// All lists every valid policy.
var All = []Policy{Tracked, Persistent, Cache, Secret, Ephemeral}

// Valid reports whether p is one of the five policies.
func (p Policy) Valid() bool {
	switch p {
	case Tracked, Persistent, Cache, Secret, Ephemeral:
		return true
	}
	return false
}

// CommittedByDefault reports whether the policy enters a commit when no
// explicit commit.volumes override exists.
func (p Policy) CommittedByDefault() bool { return p == Tracked }

// PushedByDefault reports whether the policy is pushed by default.
func (p Policy) PushedByDefault() bool { return p == Tracked }

// PersistedAcrossRestarts reports whether the volume survives container
// stop/start.
func (p Policy) PersistedAcrossRestarts() bool {
	switch p {
	case Tracked, Persistent, Cache:
		return true
	}
	return false
}

// Selection is a set of volume names chosen for commit or push.
type Selection []string

// SelectCommit computes which volumes enter a commit. When explicit is
// non-empty it overrides the default policy-based selection (design §10:
// "Project Spec 可以通过 commit.volumes 显式覆盖默认选择"). Error results
// from explicit names that do not exist.
func SelectCommit(volumes map[string]Policy, explicit []string) (Selection, error) {
	if len(explicit) > 0 {
		return normalizeExplicit(volumes, explicit)
	}
	var sel Selection
	for name, p := range volumes {
		if p.CommittedByDefault() {
			sel = append(sel, name)
		}
	}
	sort.Strings(sel)
	return sel, nil
}

// SelectPush computes which volumes are pushed with a commit. Only tracked
// volumes push by default; an explicit list may narrow or widen this.
func SelectPush(volumes map[string]Policy, explicit []string) (Selection, error) {
	if len(explicit) > 0 {
		return normalizeExplicit(volumes, explicit)
	}
	var sel Selection
	for name, p := range volumes {
		if p.PushedByDefault() {
			sel = append(sel, name)
		}
	}
	sort.Strings(sel)
	return sel, nil
}

func normalizeExplicit(volumes map[string]Policy, explicit []string) (Selection, error) {
	seen := map[string]bool{}
	var sel Selection
	for _, name := range explicit {
		p, ok := volumes[name]
		if !ok {
			return nil, fmt.Errorf("unknown volume %q", name)
		}
		if p == Secret || p == Cache {
			return nil, fmt.Errorf("volume %q has policy %s and cannot be committed", name, p)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate volume %q in selection", name)
		}
		seen[name] = true
		sel = append(sel, name)
	}
	sort.Strings(sel)
	return sel, nil
}
