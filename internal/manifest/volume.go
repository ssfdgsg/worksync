package manifest

import (
	"fmt"
	"path/filepath"
)

// Validate checks the rules of design §12.2 for a single volume entry.
func (v *VolumeSpec) Validate() error {
	if v.Target == "" || !filepath.IsAbs(v.Target) {
		return fmt.Errorf("target must be an absolute container path")
	}
	if v.Policy == "" {
		return fmt.Errorf("policy is required (tracked|persistent|cache|secret|ephemeral)")
	}
	if !v.Policy.Valid() {
		return fmt.Errorf("policy %q is not valid (tracked|persistent|cache|secret|ephemeral)", v.Policy)
	}
	if v.Source != nil {
		switch v.Source.Type {
		case "", "managed":
			v.Source.Type = "managed"
		case "host":
			if v.Source.Path == "" {
				return fmt.Errorf("source.type host requires a path")
			}
		default:
			return fmt.Errorf("source.type %q is not valid (host|managed)", v.Source.Type)
		}
	}
	return nil
}
