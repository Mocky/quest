package store

import "testing"

// TestMigrationSequenceContiguous is the Layer 1 tripwire for the
// embedded migration set. The runner already enforces contiguity, a
// numeric head at SupportedSchemaVersion, and NNN_ prefix parsing —
// this test pins those invariants so a malformed or mis-numbered
// migration fails at `make test`, not at runtime.
func TestMigrationSequenceContiguous(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatalf("no migrations loaded")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migrations[%d].version = %d, want %d", i, m.version, i+1)
		}
		if m.sql == "" {
			t.Errorf("migrations[%d] has empty SQL body", i)
		}
		if m.label == "" {
			t.Errorf("migrations[%d] has empty label", i)
		}
	}
	top := ms[len(ms)-1].version
	if top != SupportedSchemaVersion {
		t.Fatalf("highest migration version = %d, SupportedSchemaVersion = %d (mismatch)", top, SupportedSchemaVersion)
	}
}
