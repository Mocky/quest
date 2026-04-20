//go:build integration

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestBackupCapableInterfaceAssertion pins the cross-package shape-
// match between backupCapable and modernc.org/sqlite's unexported
// *conn type. If a future driver upgrade renames NewBackup, this test
// fails at CI instead of at 3 a.m. in a pre-migration snapshot abort.
// The assertion happens inside conn.Raw because that is the only
// documented path that exposes the driver-private *conn to consumer
// code.
func TestBackupCapableInterfaceAssertion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer conn.Close()

	if err := conn.Raw(func(driverConn any) error {
		if _, ok := driverConn.(backupCapable); !ok {
			t.Fatalf("modernc.org/sqlite driver connection does not satisfy backupCapable — NewBackup signature may have changed")
		}
		return nil
	}); err != nil {
		t.Fatalf("conn.Raw: %v", err)
	}
}
