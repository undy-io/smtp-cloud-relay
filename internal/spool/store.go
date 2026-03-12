package spool

import (
	"context"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

// Store is the durable spool contract used by future relay and worker code.
type Store interface {
	Enqueue(ctx context.Context, msg email.Message) (Record, error)
	ClaimReady(ctx context.Context, now time.Time) (Record, bool, error)
	MarkSubmitted(ctx context.Context, rec Record, operationID, operationLocation string, nextAttemptAt time.Time) (Record, error)
	MarkRetry(ctx context.Context, rec Record, nextAttemptAt time.Time, lastErr *LastError) (Record, error)
	MarkSucceeded(ctx context.Context, rec Record) (Record, error)
	MarkDeadLetter(ctx context.Context, rec Record, lastErr *LastError) (Record, error)
	Recover(ctx context.Context, now time.Time) (RecoveryResult, error)
}
