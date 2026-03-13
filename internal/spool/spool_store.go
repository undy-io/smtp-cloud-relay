package spool

import (
	"context"
	"fmt"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

const spoolProviderName = "spool"

// DefaultRoot is the pre-configuration spool root used before SPOOL-006 adds env and Helm wiring.
const DefaultRoot = "/var/lib/smtp-cloud-relay/spool"

// SpoolStore coordinates SQLite record metadata and filesystem payload storage.
type SpoolStore struct {
	records  *sqliteRecordStore
	payloads *PayloadStore
	now      func() time.Time
	newID    func() (string, error)
}

var _ Store = (*SpoolStore)(nil)

// NewSpoolStore constructs the top-level hybrid spool store for the provided root path.
func NewSpoolStore(root string) (*SpoolStore, error) {
	store := &SpoolStore{
		now: func() time.Time {
			return time.Now().UTC()
		},
		newID: newUUIDv4,
	}

	records, err := newSQLiteRecordStore(root, func() time.Time {
		return store.now().UTC()
	})
	if err != nil {
		return nil, err
	}

	payloads, err := NewPayloadStore(records.root)
	if err != nil {
		_ = records.close()
		return nil, err
	}

	store.records = records
	store.payloads = payloads
	return store, nil
}

// Close releases the underlying SQLite database handle used by the spool store.
func (s *SpoolStore) Close() error {
	if s == nil || s.records == nil {
		return nil
	}
	return s.records.close()
}

func (s *SpoolStore) Enqueue(ctx context.Context, msg email.Message) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}

	now := s.now().UTC()
	id, err := s.newID()
	if err != nil {
		return Record{}, wrapStoreError("generate record id", err)
	}

	if err := s.payloads.Save(id, msg); err != nil {
		return Record{}, wrapStoreError(fmt.Sprintf("save payload %q", id), err)
	}

	rec := Record{
		ID:            id,
		Message:       msg,
		State:         StateQueued,
		Attempt:       0,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.records.insertRecord(ctx, rec); err != nil {
		_ = s.payloads.Remove(id)
		return Record{}, err
	}
	return rec, nil
}

func (s *SpoolStore) ClaimReady(ctx context.Context, now time.Time) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}

	now = now.UTC()
	for {
		meta, ok, err := s.records.claimReadyMetadata(ctx, now)
		if err != nil || !ok {
			return Record{}, ok, err
		}

		msg, err := s.payloads.Load(meta.ID)
		if err == nil {
			rec := meta.toRecord()
			rec.Message = msg
			return rec, true, nil
		}

		classified := classifyPayloadLoadError(meta.ID, err)
		if corrupt, ok := AsRecordCorruptionError(classified); ok {
			if _, deadErr := s.records.deadLetterCorruptRecord(ctx, meta, corrupt); deadErr != nil {
				return Record{}, false, deadErr
			}
			continue
		}
		if requeueErr := s.records.requeueClaimedRecord(ctx, meta); requeueErr != nil {
			return Record{}, false, requeueErr
		}
		return Record{}, false, classified
	}
}

func (s *SpoolStore) NextSubmittedReady(ctx context.Context, now time.Time) (Record, bool, error) {
	return s.records.nextSubmittedReady(ctx, now.UTC())
}

func (s *SpoolStore) MarkSubmitted(ctx context.Context, rec Record, result email.SubmissionResult, nextAttemptAt time.Time) (Record, error) {
	return s.records.markSubmitted(ctx, rec, result, nextAttemptAt)
}

func (s *SpoolStore) MarkRetry(ctx context.Context, rec Record, nextAttemptAt time.Time, lastErr *LastError) (Record, error) {
	return s.records.markRetry(ctx, rec, nextAttemptAt, lastErr)
}

func (s *SpoolStore) MarkSucceeded(ctx context.Context, rec Record) (Record, error) {
	return s.records.markSucceeded(ctx, rec)
}

func (s *SpoolStore) MarkDeadLetter(ctx context.Context, rec Record, lastErr *LastError) (Record, error) {
	return s.records.markDeadLetter(ctx, rec, lastErr)
}

func (s *SpoolStore) Recover(ctx context.Context, now time.Time) (RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}

	snapshot, err := s.records.recoverMetadata(ctx, now.UTC())
	if err != nil {
		return RecoveryResult{}, err
	}

	result := RecoveryResult{
		Requeued:     make([]Record, 0, len(snapshot.requeued)),
		Submitted:    make([]Record, 0, len(snapshot.submitted)),
		DeadLettered: make([]Record, 0),
	}

	for _, meta := range snapshot.requeued {
		msg, loadErr := s.payloads.Load(meta.ID)
		if loadErr != nil {
			classified := classifyPayloadLoadError(meta.ID, loadErr)
			if corrupt, ok := AsRecordCorruptionError(classified); ok {
				dead, err := s.records.deadLetterCorruptRecord(ctx, meta, corrupt)
				if err != nil {
					return RecoveryResult{}, err
				}
				result.DeadLettered = append(result.DeadLettered, dead)
				continue
			}
			return RecoveryResult{}, wrapStoreError(fmt.Sprintf("load requeued payload %q during recovery", meta.ID), classified)
		}
		rec := meta.toRecord()
		rec.Message = msg
		result.Requeued = append(result.Requeued, rec)
	}

	for _, meta := range snapshot.submitted {
		msg, loadErr := s.payloads.Load(meta.ID)
		if loadErr != nil {
			classified := classifyPayloadLoadError(meta.ID, loadErr)
			if corrupt, ok := AsRecordCorruptionError(classified); ok {
				dead, err := s.records.deadLetterCorruptRecord(ctx, meta, corrupt)
				if err != nil {
					return RecoveryResult{}, err
				}
				result.DeadLettered = append(result.DeadLettered, dead)
				continue
			}
			return RecoveryResult{}, wrapStoreError(fmt.Sprintf("load submitted payload %q during recovery", meta.ID), classified)
		}
		rec := meta.toRecord()
		rec.Message = msg
		result.Submitted = append(result.Submitted, rec)
	}

	result.OrphanedPayloads, err = s.payloads.QuarantineOrphans(snapshot.validIDs)
	if err != nil {
		return RecoveryResult{}, wrapStoreError("quarantine orphan payloads", err)
	}

	return result, nil
}

func classifyPayloadLoadError(recordID string, err error) error {
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		return newRecordCorruptionError(recordID, err)
	}
	return wrapStoreError(fmt.Sprintf("load payload %q", recordID), err)
}

func (s *SpoolStore) updateRecord(ctx context.Context, currentState State, updated Record) error {
	return s.records.updateRecord(ctx, currentState, updated)
}
