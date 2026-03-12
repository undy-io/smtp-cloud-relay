package spool

import (
	"errors"
	"testing"
)

func TestAsStoreError(t *testing.T) {
	base := errors.New("disk unavailable")
	err := wrapStoreError("load payload", base)

	storeErr, ok := AsStoreError(err)
	if !ok {
		t.Fatalf("expected StoreError, got %T", err)
	}
	if storeErr.Op != "load payload" {
		t.Fatalf("unexpected store op: %q", storeErr.Op)
	}
	if !errors.Is(storeErr, base) {
		t.Fatalf("expected StoreError to unwrap %v", base)
	}
}

func TestAsRecordCorruptionError(t *testing.T) {
	base := errors.New("manifest mismatch")
	err := newRecordCorruptionError(testRecordID(1), base)

	corruptErr, ok := AsRecordCorruptionError(err)
	if !ok {
		t.Fatalf("expected RecordCorruptionError, got %T", err)
	}
	if corruptErr.RecordID != testRecordID(1) {
		t.Fatalf("unexpected record id: %q", corruptErr.RecordID)
	}
	if !errors.Is(corruptErr, base) {
		t.Fatalf("expected RecordCorruptionError to unwrap %v", base)
	}
}
