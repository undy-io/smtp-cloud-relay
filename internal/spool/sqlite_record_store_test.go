package spool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSQLiteRecordStoreCreatesSchemaAndIndexes(t *testing.T) {
	store := newSQLiteRecordTestStore(t)

	for _, name := range []string{spoolDBFileName, "records", "idx_records_ready", "idx_records_operation_id"} {
		var count int
		query := `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`
		if name == spoolDBFileName {
			if _, err := os.Stat(filepath.Join(store.root, name)); err != nil {
				t.Fatalf("stat spool db %q: %v", name, err)
			}
			continue
		}
		if err := store.db.QueryRow(query, name).Scan(&count); err != nil {
			t.Fatalf("QueryRow(%q) error: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("expected sqlite object %q to exist, count=%d", name, count)
		}
	}
}

func TestSQLiteRecordStoreMetadataFlowDoesNotNeedPayloadStore(t *testing.T) {
	store := newSQLiteRecordTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)

	rec := Record{
		ID:            testRecordID(1),
		State:         StateQueued,
		Attempt:       0,
		NextAttemptAt: base,
		CreatedAt:     base,
		UpdatedAt:     base,
	}
	if err := store.insertRecord(context.Background(), rec); err != nil {
		t.Fatalf("insertRecord() error: %v", err)
	}

	claimed, ok, err := store.claimReadyMetadata(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("claimReadyMetadata() = (%#v, %t, %v)", claimed, ok, err)
	}
	if claimed.ID != rec.ID || claimed.State != StateWorking {
		t.Fatalf("unexpected claimed metadata: %#v", claimed)
	}

	snapshot, err := store.recoverMetadata(context.Background(), base.Add(time.Minute))
	if err != nil {
		t.Fatalf("recoverMetadata() error: %v", err)
	}
	if len(snapshot.requeued) != 1 || snapshot.requeued[0].ID != rec.ID {
		t.Fatalf("unexpected requeued metadata: %#v", snapshot.requeued)
	}
	if len(snapshot.validIDs) != 1 {
		t.Fatalf("unexpected validIDs: %#v", snapshot.validIDs)
	}
}

func newSQLiteRecordTestStore(t *testing.T) *sqliteRecordStore {
	t.Helper()
	store, err := newSQLiteRecordStore(t.TempDir(), func() time.Time {
		return time.Now().UTC()
	})
	if err != nil {
		t.Fatalf("newSQLiteRecordStore() error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.close()
	})
	return store
}
