package spool

import (
	"errors"
	"fmt"
	"strings"
)

// StoreError identifies a shared spool infrastructure failure.
type StoreError struct {
	Op  string
	Err error
}

// Error formats the store-scoped failure message.
func (e *StoreError) Error() string {
	if e == nil {
		return "spool store error"
	}
	if strings.TrimSpace(e.Op) == "" {
		return fmt.Sprintf("spool store error: %v", e.Err)
	}
	return fmt.Sprintf("spool store %s: %v", strings.TrimSpace(e.Op), e.Err)
}

// Unwrap returns the underlying store failure.
func (e *StoreError) Unwrap() error { return e.Err }

// AsStoreError unwraps err into a StoreError when the failure is store-scoped.
func AsStoreError(err error) (*StoreError, bool) {
	var target *StoreError
	if !errors.As(err, &target) {
		return nil, false
	}
	return target, true
}

// RecordCorruptionError identifies record-scoped corruption handled by spool recovery logic.
type RecordCorruptionError struct {
	RecordID string
	Err      error
}

// Error formats the record-scoped corruption message.
func (e *RecordCorruptionError) Error() string {
	if e == nil {
		return "spool record corruption"
	}
	return fmt.Sprintf("spool record %q is corrupt: %v", e.RecordID, e.Err)
}

// Unwrap returns the underlying corruption cause.
func (e *RecordCorruptionError) Unwrap() error { return e.Err }

// AsRecordCorruptionError unwraps err into a RecordCorruptionError.
func AsRecordCorruptionError(err error) (*RecordCorruptionError, bool) {
	var target *RecordCorruptionError
	if !errors.As(err, &target) {
		return nil, false
	}
	return target, true
}

func wrapStoreError(op string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsStoreError(err); ok {
		return err
	}
	return &StoreError{Op: op, Err: err}
}

func newRecordCorruptionError(recordID string, err error) error {
	if err == nil {
		return nil
	}
	if existing, ok := AsRecordCorruptionError(err); ok {
		if existing.RecordID == "" {
			existing.RecordID = recordID
		}
		return err
	}
	return &RecordCorruptionError{RecordID: recordID, Err: err}
}
