package spool

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

// Worker owns the background delivery lifecycle around the durable spool.
type Worker struct {
	store         Store
	provider      email.Provider
	submitTimeout time.Duration
	logger        *slog.Logger
}

// NewWorker constructs the background delivery worker shell used before the
// steady-state submit and poll loop is implemented.
func NewWorker(logger *slog.Logger, store Store, provider email.Provider, submitTimeout time.Duration) (*Worker, error) {
	if store == nil {
		return nil, fmt.Errorf("spool worker store cannot be nil")
	}
	if provider == nil {
		return nil, fmt.Errorf("spool worker provider cannot be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Worker{
		store:         store,
		provider:      provider,
		submitTimeout: submitTimeout,
		logger:        logger,
	}, nil
}

// Recover runs startup recovery against the durable spool before listeners or
// background delivery begin.
func (w *Worker) Recover(ctx context.Context, now time.Time) (RecoveryResult, error) {
	return w.store.Recover(ctx, now)
}

// Start owns the background worker lifecycle. In SPOOL-005A it only stays
// alive until shutdown; steady-state submit and poll behavior is added later.
func (w *Worker) Start(ctx context.Context) error {
	<-ctx.Done()
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
