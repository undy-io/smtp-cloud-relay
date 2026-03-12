package spool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestNewSQLiteStoreCreatesSchemaAndIndexes(t *testing.T) {
	store := newSQLiteTestStore(t)

	for _, path := range []string{
		filepath.Join(store.root, spoolDBFileName),
		filepath.Join(store.root, payloadsDirName),
		filepath.Join(store.root, payloadOrphansDirName),
		filepath.Join(store.root, stagingDirName),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error: %v", path, err)
		}
	}

	for _, name := range []string{"records", "idx_records_ready", "idx_records_operation_id"} {
		var count int
		if err := store.db.QueryRow(`
			SELECT COUNT(*)
			FROM sqlite_master
			WHERE name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("QueryRow(%q) error: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("expected sqlite object %q to exist, count=%d", name, count)
		}
	}

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version;`).Scan(&version); err != nil {
		t.Fatalf("QueryRow(user_version) error: %v", err)
	}
	if version != spoolSchemaVersion {
		t.Fatalf("unexpected sqlite user_version: %d", version)
	}
}

func TestNewSQLiteStoreReopensCurrentV1Schema(t *testing.T) {
	store := newSQLiteTestStore(t)
	root := store.root
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	reopened, err := NewSQLiteStore(root)
	if err != nil {
		t.Fatalf("NewSQLiteStore() reopen error: %v", err)
	}
	defer reopened.Close()
}

func TestNewSQLiteStoreRejectsSymlinkedRootWithoutTouchingTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	root := filepath.Join(base, "spool-link")
	if err := os.Symlink(target, root); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to fail for symlinked root")
	}
	for _, name := range []string{payloadsDirName, payloadOrphansDirName, stagingDirName, spoolDBFileName} {
		if _, statErr := os.Stat(filepath.Join(target, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected symlink target to remain untouched for %q, stat err=%v", name, statErr)
		}
	}
}

func TestSQLiteStoreEnqueueWritesPayloadAndRow(t *testing.T) {
	store := newSQLiteTestStore(t)
	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.newID = sequenceIDs("00000000-0000-4000-8000-000000000001")

	msg := email.Message{
		EnvelopeFrom: "envelope@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "text",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello")},
		},
	}

	rec, err := store.Enqueue(context.Background(), msg)
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if rec.ID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("unexpected record id: %q", rec.ID)
	}
	if rec.State != StateQueued {
		t.Fatalf("unexpected state: %q", rec.State)
	}

	meta := readSQLiteMetadata(t, store, rec.ID)
	if meta.State != StateQueued || meta.Attempt != 0 {
		t.Fatalf("unexpected stored metadata: %#v", meta)
	}
	if got, err := store.payloads.Load(rec.ID); err != nil {
		t.Fatalf("payload Load() error: %v", err)
	} else if got.TextBody != "text" || len(got.Attachments) != 1 {
		t.Fatalf("unexpected stored payload: %#v", got)
	}
}

func TestSQLiteStoreEnqueueCleansPayloadWhenInsertFails(t *testing.T) {
	store := newSQLiteTestStore(t)
	recordID := testRecordID(1)
	store.newID = sequenceIDs(recordID)

	if err := store.db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := store.Enqueue(context.Background(), email.Message{To: []string{"to@example.com"}, TextBody: "text"})
	if err == nil {
		t.Fatal("expected Enqueue() to fail after closing database")
	}
	if _, statErr := os.Stat(store.payloads.payloadDir(recordID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected payload cleanup, stat err=%v", statErr)
	}
}

func TestSQLiteStoreClaimReadyOrdersRowsAndDeadLettersBrokenPayload(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)

	store.now = func() time.Time { return base }
	brokenID := testRecordID(1)
	validID := testRecordID(2)
	futureID := testRecordID(3)
	store.newID = sequenceIDs(brokenID, validID, futureID)
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"broken@example.com"}, TextBody: "broken"}); err != nil {
		t.Fatalf("Enqueue(broken) error: %v", err)
	}

	store.now = func() time.Time { return base.Add(1 * time.Minute) }
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"valid@example.com"}, TextBody: "valid"}); err != nil {
		t.Fatalf("Enqueue(valid) error: %v", err)
	}

	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	futureRec, err := store.Enqueue(context.Background(), email.Message{To: []string{"future@example.com"}, TextBody: "future"})
	if err != nil {
		t.Fatalf("Enqueue(future) error: %v", err)
	}
	if err := store.updateRecord(context.Background(), StateQueued, Record{
		ID:            futureRec.ID,
		Message:       futureRec.Message,
		State:         StateQueued,
		Attempt:       futureRec.Attempt,
		NextAttemptAt: base.Add(10 * time.Minute),
		CreatedAt:     futureRec.CreatedAt,
		UpdatedAt:     base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("updateRecord(future) error: %v", err)
	}

	manifestPath := filepath.Join(store.payloads.payloadDir(brokenID), messageFileName)
	if err := os.WriteFile(manifestPath, []byte("{not-json"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	store.now = func() time.Time { return base.Add(3 * time.Minute) }
	rec, ok, err := store.ClaimReady(context.Background(), base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("ClaimReady() error: %v", err)
	}
	if !ok {
		t.Fatal("expected ClaimReady() to return a valid record")
	}
	if rec.ID != validID {
		t.Fatalf("unexpected claimed record: %q", rec.ID)
	}

	broken := readSQLiteMetadata(t, store, brokenID)
	if broken.State != StateDeadLetter {
		t.Fatalf("expected broken record to be dead-lettered, got %q", broken.State)
	}
	if broken.LastError == nil || broken.LastError.Provider != spoolProviderName {
		t.Fatalf("unexpected broken last error: %#v", broken.LastError)
	}

	future := readSQLiteMetadata(t, store, futureID)
	if future.State != StateQueued {
		t.Fatalf("expected future record to remain queued, got %q", future.State)
	}
}

func TestSQLiteStoreClaimReadyRequeuesTransientPayloadLoadFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	store.newID = sequenceIDs(testRecordID(1))

	rec, err := store.Enqueue(context.Background(), email.Message{To: []string{"to@example.com"}, TextBody: "text"})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}

	manifestPath := filepath.Join(store.payloads.payloadDir(rec.ID), messageFileName)
	if err := os.Chmod(manifestPath, 0); err != nil {
		t.Fatalf("Chmod() error: %v", err)
	}

	claimed, ok, err := store.ClaimReady(context.Background(), base)
	if err == nil {
		t.Fatal("expected ClaimReady() to fail")
	}
	if ok {
		t.Fatalf("expected no claimed record, got %#v", claimed)
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}

	meta := readSQLiteMetadata(t, store, rec.ID)
	if meta.State != StateQueued {
		t.Fatalf("expected record to be requeued, got %q", meta.State)
	}
	if meta.Attempt != 0 {
		t.Fatalf("unexpected attempt after transient failure: %d", meta.Attempt)
	}

	if err := os.Chmod(manifestPath, spoolFileMode); err != nil {
		t.Fatalf("Chmod() restore error: %v", err)
	}
	claimed, ok, err = store.ClaimReady(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("ClaimReady() after transient failure = (%#v, %t, %v)", claimed, ok, err)
	}
	if claimed.ID != rec.ID {
		t.Fatalf("unexpected claimed record after retry: %q", claimed.ID)
	}
}

func TestSQLiteStoreTransitionMethodsPreserveAttemptSemantics(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	store.newID = sequenceIDs(testRecordID(1))

	_, err := store.Enqueue(context.Background(), email.Message{To: []string{"to@example.com"}, TextBody: "text"})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	working, ok, err := store.ClaimReady(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("ClaimReady() = (%#v, %t, %v)", working, ok, err)
	}

	store.now = func() time.Time { return base.Add(1 * time.Minute) }
	submitted, err := store.MarkSubmitted(context.Background(), working, "op-1", "loc-1", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("MarkSubmitted() error: %v", err)
	}
	if submitted.Attempt != 1 {
		t.Fatalf("unexpected submitted attempt: %d", submitted.Attempt)
	}

	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	retriedSubmitted, err := store.MarkRetry(context.Background(), submitted, base.Add(3*time.Minute), &LastError{Message: "poll again", Provider: "acs", Temporary: true})
	if err != nil {
		t.Fatalf("MarkRetry(submitted) error: %v", err)
	}
	if retriedSubmitted.Attempt != 1 || retriedSubmitted.State != StateSubmitted {
		t.Fatalf("unexpected submitted retry result: %#v", retriedSubmitted)
	}

	store.now = func() time.Time { return base.Add(3 * time.Minute) }
	succeeded, err := store.MarkSucceeded(context.Background(), retriedSubmitted)
	if err != nil {
		t.Fatalf("MarkSucceeded() error: %v", err)
	}
	if succeeded.Attempt != 1 || succeeded.State != StateSucceeded {
		t.Fatalf("unexpected success result: %#v", succeeded)
	}

	store.newID = sequenceIDs(testRecordID(2))
	store.now = func() time.Time { return base.Add(4 * time.Minute) }
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"to2@example.com"}, TextBody: "text"}); err != nil {
		t.Fatalf("second Enqueue() error: %v", err)
	}
	workingTwo, ok, err := store.ClaimReady(context.Background(), base.Add(4*time.Minute))
	if err != nil || !ok {
		t.Fatalf("second ClaimReady() = (%#v, %t, %v)", workingTwo, ok, err)
	}

	store.now = func() time.Time { return base.Add(5 * time.Minute) }
	retriedWorking, err := store.MarkRetry(context.Background(), workingTwo, base.Add(6*time.Minute), &LastError{Message: "retry", Provider: "ses", Temporary: true})
	if err != nil {
		t.Fatalf("MarkRetry(working) error: %v", err)
	}
	if retriedWorking.Attempt != 1 || retriedWorking.State != StateQueued {
		t.Fatalf("unexpected working retry result: %#v", retriedWorking)
	}

	claimedAgain, ok, err := store.ClaimReady(context.Background(), base.Add(7*time.Minute))
	if err != nil || !ok {
		t.Fatalf("third ClaimReady() = (%#v, %t, %v)", claimedAgain, ok, err)
	}
	store.now = func() time.Time { return base.Add(8 * time.Minute) }
	dead, err := store.MarkDeadLetter(context.Background(), claimedAgain, &LastError{Message: "permanent", Provider: "ses"})
	if err != nil {
		t.Fatalf("MarkDeadLetter() error: %v", err)
	}
	if dead.Attempt != 2 || dead.State != StateDeadLetter {
		t.Fatalf("unexpected dead-letter result: %#v", dead)
	}
}

func TestSQLiteStoreRecoverRequeuesSubmittedDeadLettersBrokenAndQuarantinesOrphans(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)

	store.now = func() time.Time { return base }
	workingID := testRecordID(1)
	brokenSubmittedID := testRecordID(2)
	validSubmittedID := testRecordID(3)
	orphanID := testRecordID(4)
	store.newID = sequenceIDs(workingID, brokenSubmittedID, validSubmittedID)
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"working@example.com"}, TextBody: "working"}); err != nil {
		t.Fatalf("Enqueue(working-valid) error: %v", err)
	}
	working, ok, err := store.ClaimReady(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("ClaimReady(working-valid) = (%#v, %t, %v)", working, ok, err)
	}

	store.now = func() time.Time { return base.Add(1 * time.Minute) }
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"submitted-broken@example.com"}, TextBody: "broken"}); err != nil {
		t.Fatalf("Enqueue(submitted-broken) error: %v", err)
	}
	brokenSubmittedWorking, ok, err := store.ClaimReady(context.Background(), base.Add(1*time.Minute))
	if err != nil || !ok {
		t.Fatalf("ClaimReady(submitted-broken) = (%#v, %t, %v)", brokenSubmittedWorking, ok, err)
	}
	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	brokenSubmitted, err := store.MarkSubmitted(context.Background(), brokenSubmittedWorking, "op-broken", "loc-broken", base.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("MarkSubmitted(submitted-broken) error: %v", err)
	}

	store.now = func() time.Time { return base.Add(3 * time.Minute) }
	if _, err := store.Enqueue(context.Background(), email.Message{To: []string{"submitted-valid@example.com"}, TextBody: "valid"}); err != nil {
		t.Fatalf("Enqueue(submitted-valid) error: %v", err)
	}
	validSubmittedWorking, ok, err := store.ClaimReady(context.Background(), base.Add(3*time.Minute))
	if err != nil || !ok {
		t.Fatalf("ClaimReady(submitted-valid) = (%#v, %t, %v)", validSubmittedWorking, ok, err)
	}
	store.now = func() time.Time { return base.Add(4 * time.Minute) }
	_, err = store.MarkSubmitted(context.Background(), validSubmittedWorking, "op-valid", "loc-valid", base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("MarkSubmitted(submitted-valid) error: %v", err)
	}

	if err := store.payloads.Remove(brokenSubmitted.ID); err != nil {
		t.Fatalf("Remove(brokenSubmitted payload) error: %v", err)
	}
	if err := store.payloads.Save(orphanID, email.Message{To: []string{"orphan@example.com"}, TextBody: "orphan"}); err != nil {
		t.Fatalf("Save(orphan payload) error: %v", err)
	}

	store.now = func() time.Time { return base.Add(10 * time.Minute) }
	result, err := store.Recover(context.Background(), base.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}

	if len(result.Requeued) != 1 || result.Requeued[0].ID != workingID {
		t.Fatalf("unexpected requeued records: %#v", result.Requeued)
	}
	if len(result.Submitted) != 1 || result.Submitted[0].ID != validSubmittedID {
		t.Fatalf("unexpected submitted records: %#v", result.Submitted)
	}
	if len(result.DeadLettered) != 1 || result.DeadLettered[0].ID != brokenSubmittedID {
		t.Fatalf("unexpected dead-lettered records: %#v", result.DeadLettered)
	}
	if len(result.OrphanedPayloads) != 1 || filepath.Base(result.OrphanedPayloads[0]) != orphanID {
		t.Fatalf("unexpected orphaned payloads: %#v", result.OrphanedPayloads)
	}

	requeuedMeta := readSQLiteMetadata(t, store, working.ID)
	if requeuedMeta.State != StateQueued {
		t.Fatalf("expected working record to be requeued, got %q", requeuedMeta.State)
	}
	if !requeuedMeta.NextAttemptAt.Equal(base.Add(10 * time.Minute)) {
		t.Fatalf("unexpected requeued next attempt: %s", requeuedMeta.NextAttemptAt)
	}

	brokenMeta := readSQLiteMetadata(t, store, brokenSubmittedID)
	if brokenMeta.State != StateDeadLetter || brokenMeta.LastError == nil || brokenMeta.LastError.Provider != spoolProviderName {
		t.Fatalf("unexpected broken submitted metadata: %#v", brokenMeta)
	}

	validMeta := readSQLiteMetadata(t, store, validSubmittedID)
	if validMeta.State != StateSubmitted {
		t.Fatalf("expected valid submitted record to remain submitted, got %q", validMeta.State)
	}
}

func TestSQLiteStoreRecoverReturnsTransientPayloadLoadFailure(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	store.newID = sequenceIDs(testRecordID(1))

	rec, err := store.Enqueue(context.Background(), email.Message{To: []string{"working@example.com"}, TextBody: "working"})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	working, ok, err := store.ClaimReady(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("ClaimReady() = (%#v, %t, %v)", working, ok, err)
	}

	manifestPath := filepath.Join(store.payloads.payloadDir(rec.ID), messageFileName)
	if err := os.Chmod(manifestPath, 0); err != nil {
		t.Fatalf("Chmod() error: %v", err)
	}

	_, err = store.Recover(context.Background(), base.Add(1*time.Minute))
	if err == nil {
		t.Fatal("expected Recover() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}

	meta := readSQLiteMetadata(t, store, working.ID)
	if meta.State != StateQueued {
		t.Fatalf("expected working record to remain requeued, got %q", meta.State)
	}
	if meta.LastError != nil {
		t.Fatalf("expected no last error after transient recovery failure, got %#v", meta.LastError)
	}
}

func TestSQLiteStoreClaimReadyMissingPayloadRootReturnsStoreError(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	store.newID = sequenceIDs(testRecordID(1))

	rec, err := store.Enqueue(context.Background(), email.Message{To: []string{"to@example.com"}, TextBody: "text"})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	if err := os.RemoveAll(store.payloads.payloadsRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}

	claimed, ok, err := store.ClaimReady(context.Background(), base)
	if err == nil {
		t.Fatal("expected ClaimReady() to fail")
	}
	if ok {
		t.Fatalf("expected no claimed record, got %#v", claimed)
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	meta := readSQLiteMetadata(t, store, rec.ID)
	if meta.State != StateQueued {
		t.Fatalf("expected record to remain queued after store error, got %q", meta.State)
	}
	if meta.LastError != nil {
		t.Fatalf("expected no last error after store error, got %#v", meta.LastError)
	}
}

func TestSQLiteStoreRecoverMissingPayloadRootReturnsStoreError(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	store.newID = sequenceIDs(testRecordID(1))

	rec, err := store.Enqueue(context.Background(), email.Message{To: []string{"working@example.com"}, TextBody: "working"})
	if err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}
	working, ok, err := store.ClaimReady(context.Background(), base)
	if err != nil || !ok {
		t.Fatalf("ClaimReady() = (%#v, %t, %v)", working, ok, err)
	}
	if err := os.RemoveAll(store.payloads.payloadsRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}

	_, err = store.Recover(context.Background(), base.Add(1*time.Minute))
	if err == nil {
		t.Fatal("expected Recover() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	meta := readSQLiteMetadata(t, store, rec.ID)
	if meta.State != StateQueued {
		t.Fatalf("expected working record to stay requeued after store error, got %q", meta.State)
	}
	if meta.LastError != nil {
		t.Fatalf("expected no last error after store error, got %#v", meta.LastError)
	}
}

func TestNewSQLiteStoreRejectsWeakCurrentVersionSchema(t *testing.T) {
	root := t.TempDir()
	db := newWeakSchemaSQLiteDatabase(t, root, spoolSchemaVersion)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to reject weak current-version schema")
	}
	if !strings.Contains(err.Error(), "delete the spool directory and restart") {
		t.Fatalf("expected unsupported-local-schema error, got %v", err)
	}
}

func TestNewSQLiteStoreRejectsUnversionedExistingSchema(t *testing.T) {
	root := t.TempDir()
	db := newWeakSchemaSQLiteDatabase(t, root, 0)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to reject unversioned existing schema")
	}
	if !strings.Contains(err.Error(), "delete the spool directory and restart") {
		t.Fatalf("expected unsupported-local-schema error, got %v", err)
	}
}

func TestNewSQLiteStoreRejectsWrongReadyIndexDefinition(t *testing.T) {
	root := t.TempDir()
	db := newCurrentSchemaSQLiteDatabase(t, root, spoolSchemaVersion,
		`CREATE INDEX idx_records_ready ON records(next_attempt_at_ms, state, created_at_ms, id);`,
		recordsOperationIndexSchema,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to reject wrong ready-index definition")
	}
	if !strings.Contains(err.Error(), "delete the spool directory and restart") {
		t.Fatalf("expected unsupported-local-schema error, got %v", err)
	}
}

func TestNewSQLiteStoreRejectsWrongOperationIndexDefinition(t *testing.T) {
	root := t.TempDir()
	db := newCurrentSchemaSQLiteDatabase(t, root, spoolSchemaVersion,
		recordsReadyIndexSchema,
		`CREATE UNIQUE INDEX idx_records_operation_id ON records(operation_id);`,
	)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to reject wrong operation-index definition")
	}
	if !strings.Contains(err.Error(), "delete the spool directory and restart") {
		t.Fatalf("expected unsupported-local-schema error, got %v", err)
	}
}

func TestNewSQLiteStoreRejectsMissingRequiredIndex(t *testing.T) {
	root := t.TempDir()
	db := newCurrentSchemaSQLiteDatabase(t, root, spoolSchemaVersion,
		recordsReadyIndexSchema,
		"",
	)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	_, err := NewSQLiteStore(root)
	if err == nil {
		t.Fatal("expected NewSQLiteStore() to reject missing required index")
	}
	if !strings.Contains(err.Error(), "delete the spool directory and restart") {
		t.Fatalf("expected unsupported-local-schema error, got %v", err)
	}
}

func TestSQLiteSchemaRejectsInvalidRecords(t *testing.T) {
	store := newSQLiteTestStore(t)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		id      any
		state   string
		attempt int
		nextMS  int64
	}{
		{name: "null id", id: nil, state: string(StateQueued), attempt: 0, nextMS: base.UnixMilli()},
		{name: "padded id", id: "  spaced  ", state: string(StateQueued), attempt: 0, nextMS: base.UnixMilli()},
		{name: "invalid state", id: "bad-state", state: "bogus", attempt: 0, nextMS: base.UnixMilli()},
		{name: "negative attempt", id: "negative-attempt", state: string(StateQueued), attempt: -1, nextMS: base.UnixMilli()},
		{name: "zero next attempt", id: "zero-next-attempt", state: string(StateQueued), attempt: 0, nextMS: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.db.Exec(`
				INSERT INTO records (
					id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
					last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
					created_at_ms, updated_at_ms
				) VALUES (?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, ?, ?)`,
				tc.id,
				tc.state,
				tc.attempt,
				tc.nextMS,
				base.UnixMilli(),
				base.UnixMilli(),
			)
			if err == nil {
				t.Fatal("expected INSERT to fail")
			}
		})
	}
}

func TestScanMetadataRejectsNonCanonicalIDWithoutTrimming(t *testing.T) {
	root := t.TempDir()
	db := newWeakSchemaSQLiteDatabase(t, root, spoolSchemaVersion)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	insertWeakRecord(t, db, "  padded-id  ", string(StateQueued), 0, base.UnixMilli(), base.UnixMilli(), base.UnixMilli())

	row := db.QueryRow(`
		SELECT
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		FROM records
		WHERE id = ?`, "  padded-id  ")

	if _, err := scanMetadata(row); err == nil {
		t.Fatal("expected scanMetadata() to reject non-canonical id")
	}
}

func TestQueryAllIDsRejectsNonCanonicalIDWithoutTrimming(t *testing.T) {
	root := t.TempDir()
	db := newWeakSchemaSQLiteDatabase(t, root, spoolSchemaVersion)
	base := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	insertWeakRecord(t, db, "  padded-id  ", string(StateQueued), 0, base.UnixMilli(), base.UnixMilli(), base.UnixMilli())

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn() error: %v", err)
	}
	defer conn.Close()

	if _, err := queryAllIDs(context.Background(), conn); err == nil {
		t.Fatal("expected queryAllIDs() to reject non-canonical id")
	}
}

func newSQLiteTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func readSQLiteMetadata(t *testing.T, store *SQLiteStore, id string) recordMetadata {
	t.Helper()
	row := store.db.QueryRow(`
		SELECT
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		FROM records
		WHERE id = ?`, id)
	meta, err := scanMetadata(row)
	if err != nil {
		t.Fatalf("scanMetadata(%q) error: %v", id, err)
	}
	return meta
}

var _ interface {
	Scan(dest ...any) error
} = (*sql.Row)(nil)

func sequenceIDs(ids ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index >= len(ids) {
			return "", context.DeadlineExceeded
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func newWeakSchemaSQLiteDatabase(t *testing.T, root string, userVersion int) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqliteDriverName, filepath.Join(root, spoolDBFileName))
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err := db.Exec(`
		CREATE TABLE records (
			id TEXT PRIMARY KEY,
			state TEXT NOT NULL,
			attempt INTEGER NOT NULL,
			next_attempt_at_ms INTEGER NOT NULL,
			operation_id TEXT,
			operation_location TEXT,
			last_error_message TEXT,
			last_error_provider TEXT,
			last_error_temporary INTEGER,
			last_error_timestamp_ms INTEGER,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL
		);
	`); err != nil {
		t.Fatalf("create legacy schema error: %v", err)
	}
	if _, err := db.Exec(recordsReadyIndexSchema); err != nil {
		t.Fatalf("create legacy ready index error: %v", err)
	}
	if _, err := db.Exec(recordsOperationIndexSchema); err != nil {
		t.Fatalf("create legacy operation index error: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d;", userVersion)); err != nil {
		t.Fatalf("set sqlite user_version error: %v", err)
	}
	return db
}

func newCurrentSchemaSQLiteDatabase(t *testing.T, root string, userVersion int, readyIndexSQL, operationIndexSQL string) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqliteDriverName, filepath.Join(root, spoolDBFileName))
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err := db.Exec(recordsTableSchema); err != nil {
		t.Fatalf("create current schema error: %v", err)
	}
	for _, stmt := range []string{readyIndexSQL, operationIndexSQL} {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create current schema index error: %v", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d;", userVersion)); err != nil {
		t.Fatalf("set sqlite user_version error: %v", err)
	}
	return db
}

func insertWeakRecord(t *testing.T, db *sql.DB, id, state string, attempt int, nextAttemptMS, createdAtMS, updatedAtMS int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO records (
			id, state, attempt, next_attempt_at_ms, operation_id, operation_location,
			last_error_message, last_error_provider, last_error_temporary, last_error_timestamp_ms,
			created_at_ms, updated_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		state,
		attempt,
		nextAttemptMS,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		createdAtMS,
		updatedAtMS,
	); err != nil {
		t.Fatalf("insert weak schema record error: %v", err)
	}
}
