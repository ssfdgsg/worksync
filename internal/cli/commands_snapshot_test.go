package cli

import (
	"testing"
)

// TestCheckpointExportFlagAlias: checkpoint export accepts both -o and
// --output (the user hit --output being silently ignored and the archive
// landing in the default checkpoints dir instead of the requested path).
func TestCheckpointExportFlagAlias(t *testing.T) {
	if got := checkpointExportDest([]string{"-o", "/tmp/a.oci"}); got != "/tmp/a.oci" {
		t.Errorf("-o parse = %q", got)
	}
	if got := checkpointExportDest([]string{"--output", "/tmp/b.oci"}); got != "/tmp/b.oci" {
		t.Errorf("--output parse = %q", got)
	}
	if got := checkpointExportDest([]string{"--output=/tmp/c.oci"}); got != "" {
		t.Errorf("no equals-form support expected, got %q", got)
	}
	if got := checkpointExportDest([]string{}); got != "" {
		t.Errorf("no flags = %q", got)
	}
}
