package spool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/observability"
)

const (
	// DefaultPollInterval is the first-release worker loop interval and later
	// becomes the default SPOOL_POLL_INTERVAL_MS value.
	DefaultPollInterval = 1 * time.Second
	// DefaultFinalizeTimeout bounds the post-provider state persistence window
	// after a submit or poll result has already been obtained.
	DefaultFinalizeTimeout = 5 * time.Second
	// DefaultSubmittedTimeout bounds how long a submitted provider operation may
	// remain non-terminal before the worker dead-letters it.
	DefaultSubmittedTimeout = 24 * time.Hour
)

// WorkerConfig defines the background delivery loop timing and retry policy.
type WorkerConfig struct {
	SubmitTimeout    time.Duration
	FinalizeTimeout  time.Duration
	PollInterval     time.Duration
	RetryAttempts    int
	RetryBaseDelay   time.Duration
	SubmittedTimeout time.Duration
	ProviderName     string
	Metrics          *observability.Metrics
}

// Worker owns the single-threaded background delivery loop around the durable spool.
type Worker struct {
	store    Store
	provider email.Provider
	cfg      WorkerConfig
	logger   *slog.Logger
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// NewWorker constructs the background delivery worker.
func NewWorker(logger *slog.Logger, store Store, provider email.Provider, cfg WorkerConfig) (*Worker, error) {
	if store == nil {
		return nil, fmt.Errorf("spool worker store cannot be nil")
	}
	if provider == nil {
		return nil, fmt.Errorf("spool worker provider cannot be nil")
	}
	if cfg.SubmitTimeout <= 0 {
		return nil, fmt.Errorf("spool worker submit timeout must be > 0")
	}
	if cfg.FinalizeTimeout <= 0 {
		return nil, fmt.Errorf("spool worker finalize timeout must be > 0")
	}
	if cfg.PollInterval <= 0 {
		return nil, fmt.Errorf("spool worker poll interval must be > 0")
	}
	if cfg.RetryAttempts < 1 {
		return nil, fmt.Errorf("spool worker retry attempts must be >= 1")
	}
	if cfg.RetryBaseDelay <= 0 {
		return nil, fmt.Errorf("spool worker retry base delay must be > 0")
	}
	if cfg.SubmittedTimeout <= 0 {
		return nil, fmt.Errorf("spool worker submitted timeout must be > 0")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Worker{
		store:    store,
		provider: provider,
		cfg:      cfg,
		logger:   logger,
		now: func() time.Time {
			return time.Now().UTC()
		},
		sleep: sleepWithContext,
	}, nil
}

// Recover runs startup recovery against the durable spool before listeners or
// background delivery begin.
func (w *Worker) Recover(ctx context.Context, now time.Time) (RecoveryResult, error) {
	return w.store.Recover(ctx, now)
}

// Start runs the single-threaded submit/poll loop until shutdown.
func (w *Worker) Start(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		now := w.now().UTC()
		submittedWorked, err := w.processSubmittedOnce(ctx, now)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		queuedWorked, err := w.processQueuedOnce(ctx, now)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}

		if submittedWorked || queuedWorked {
			continue
		}
		if err := w.sleep(ctx, w.cfg.PollInterval); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func (w *Worker) processQueuedOnce(ctx context.Context, now time.Time) (bool, error) {
	rec, ok, err := w.store.ClaimReady(ctx, now)
	if err != nil || !ok {
		return false, err
	}

	callCtx, cancel := context.WithTimeout(ctx, w.cfg.SubmitTimeout)
	result, submitErr := w.provider.Submit(callCtx, rec.Message, rec.ID)
	cancel()

	if submitErr != nil {
		w.cfg.Metrics.ObserveSubmission(w.cfg.ProviderName, submissionOutcomeFromError(submitErr))
		lastErr := w.lastErrorFromProviderError(submitErr, now)
		if lastErr.Temporary && w.submitRetryAllowed(rec) {
			w.cfg.Metrics.IncRetry(observabilityRetryStageSubmit)
			err = w.finalizeRecord("retry queued record", rec.ID, func(finalizeCtx context.Context) error {
				_, err := w.store.MarkRetry(finalizeCtx, rec, now.Add(w.submitRetryDelay(rec.Attempt)), lastErr)
				return err
			})
			return true, err
		}
		err = w.finalizeRecord("dead-letter queued record after submit error", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, lastErr)
			return err
		})
		return true, err
	}

	switch result.State {
	case email.SubmissionStateSucceeded:
		w.cfg.Metrics.ObserveSubmission(w.cfg.ProviderName, string(email.SubmissionStateSucceeded))
		rec.ProviderMessageID = result.ProviderMessageID
		err = w.finalizeRecord("mark queued record succeeded", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkSucceeded(finalizeCtx, rec)
			return err
		})
		return true, err
	case email.SubmissionStateRunning:
		w.cfg.Metrics.ObserveSubmission(w.cfg.ProviderName, string(email.SubmissionStateRunning))
		if result.OperationID == "" {
			err = w.finalizeRecord("dead-letter queued record without operation id", rec.ID, func(finalizeCtx context.Context) error {
				_, err := w.store.MarkDeadLetter(finalizeCtx, rec, &LastError{
					Message:   "provider submit accepted without operation id",
					Provider:  spoolProviderName,
					Temporary: false,
					Timestamp: now,
				})
				return err
			})
			return true, err
		}
		nextAttemptAt := now.Add(w.cfg.PollInterval)
		if result.RetryAfter > 0 {
			nextAttemptAt = now.Add(result.RetryAfter)
		}
		err = w.finalizeRecord("mark queued record submitted", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkSubmitted(finalizeCtx, rec, result, nextAttemptAt)
			return err
		})
		return true, err
	case email.SubmissionStateFailed, email.SubmissionStateCanceled:
		w.cfg.Metrics.ObserveSubmission(w.cfg.ProviderName, string(result.State))
		lastErr := lastErrorFromSubmissionFailure(result.Failure, now, submissionFailureMessage(result.State))
		if lastErr.Temporary && w.submitRetryAllowed(rec) {
			w.cfg.Metrics.IncRetry(observabilityRetryStageSubmit)
			err = w.finalizeRecord("retry queued record after terminal submit state", rec.ID, func(finalizeCtx context.Context) error {
				_, err := w.store.MarkRetry(finalizeCtx, rec, now.Add(w.submitRetryDelay(rec.Attempt)), lastErr)
				return err
			})
			return true, err
		}
		err = w.finalizeRecord("dead-letter queued record after terminal submit state", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, lastErr)
			return err
		})
		return true, err
	default:
		w.cfg.Metrics.ObserveSubmission(w.cfg.ProviderName, observabilityOutcomePermanentError)
		err = w.finalizeRecord("dead-letter queued record after unsupported submit state", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, &LastError{
				Message:   fmt.Sprintf("provider returned unsupported submission state %q", result.State),
				Provider:  spoolProviderName,
				Temporary: false,
				Timestamp: now,
			})
			return err
		})
		return true, err
	}
}

func (w *Worker) processSubmittedOnce(ctx context.Context, now time.Time) (bool, error) {
	rec, ok, err := w.store.NextSubmittedReady(ctx, now)
	if err != nil || !ok {
		return false, err
	}

	if !rec.FirstSubmittedAt.IsZero() && !rec.FirstSubmittedAt.Add(w.cfg.SubmittedTimeout).After(now) {
		err = w.finalizeRecord("dead-letter submitted record after timeout", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, &LastError{
				Message:   fmt.Sprintf("submitted operation exceeded %s timeout", w.cfg.SubmittedTimeout),
				Provider:  spoolProviderName,
				Temporary: false,
				Timestamp: now,
			})
			return err
		})
		return true, err
	}

	callCtx, cancel := context.WithTimeout(ctx, w.cfg.SubmitTimeout)
	status, pollErr := w.provider.Poll(callCtx, rec.OperationID)
	cancel()

	if pollErr != nil {
		w.cfg.Metrics.ObservePoll(w.cfg.ProviderName, pollOutcomeFromError(pollErr))
		lastErr := w.lastErrorFromProviderError(pollErr, now)
		if lastErr.Temporary {
			w.cfg.Metrics.IncRetry(observabilityRetryStagePoll)
			err = w.finalizeRecord("reschedule submitted record after poll error", rec.ID, func(finalizeCtx context.Context) error {
				_, err := w.store.MarkRetry(finalizeCtx, rec, now.Add(w.cfg.PollInterval), lastErr)
				return err
			})
			return true, err
		}
		err = w.finalizeRecord("dead-letter submitted record after poll error", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, lastErr)
			return err
		})
		return true, err
	}

	if status.ProviderMessageID != "" {
		rec.ProviderMessageID = status.ProviderMessageID
	}

	switch status.State {
	case email.SubmissionStateRunning:
		w.cfg.Metrics.ObservePoll(w.cfg.ProviderName, string(email.SubmissionStateRunning))
		nextAttemptAt := now.Add(w.cfg.PollInterval)
		if status.RetryAfter > 0 {
			nextAttemptAt = now.Add(status.RetryAfter)
		}
		w.cfg.Metrics.IncRetry(observabilityRetryStagePoll)
		err = w.finalizeRecord("reschedule submitted record after running poll status", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkRetry(finalizeCtx, rec, nextAttemptAt, nil)
			return err
		})
		return true, err
	case email.SubmissionStateSucceeded:
		w.cfg.Metrics.ObservePoll(w.cfg.ProviderName, string(email.SubmissionStateSucceeded))
		err = w.finalizeRecord("mark submitted record succeeded", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkSucceeded(finalizeCtx, rec)
			return err
		})
		return true, err
	case email.SubmissionStateFailed, email.SubmissionStateCanceled:
		w.cfg.Metrics.ObservePoll(w.cfg.ProviderName, string(status.State))
		err = w.finalizeRecord("dead-letter submitted record after terminal poll state", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, lastErrorFromSubmissionFailure(status.Failure, now, submissionFailureMessage(status.State)))
			return err
		})
		return true, err
	default:
		w.cfg.Metrics.ObservePoll(w.cfg.ProviderName, observabilityOutcomePermanentError)
		err = w.finalizeRecord("dead-letter submitted record after unsupported poll state", rec.ID, func(finalizeCtx context.Context) error {
			_, err := w.store.MarkDeadLetter(finalizeCtx, rec, &LastError{
				Message:   fmt.Sprintf("provider returned unsupported poll state %q", status.State),
				Provider:  spoolProviderName,
				Temporary: false,
				Timestamp: now,
			})
			return err
		})
		return true, err
	}
}

func (w *Worker) finalizeRecord(action string, recordID string, fn func(context.Context) error) error {
	finalizeCtx, cancel := context.WithTimeout(context.Background(), w.cfg.FinalizeTimeout)
	defer cancel()

	if err := fn(finalizeCtx); err != nil {
		return fmt.Errorf("%s for record %q: %w", action, recordID, err)
	}
	return nil
}

func (w *Worker) submitRetryAllowed(rec Record) bool {
	return rec.Attempt < w.cfg.RetryAttempts
}

func (w *Worker) submitRetryDelay(currentAttempt int) time.Duration {
	shift := currentAttempt
	if shift < 0 {
		shift = 0
	}
	if shift > 30 {
		shift = 30
	}
	return w.cfg.RetryBaseDelay * time.Duration(1<<shift)
}

const (
	observabilityRetryStageSubmit   = "submit"
	observabilityRetryStagePoll     = "poll"
	observabilityOutcomePermanentError = "permanent_error"
)

func submissionOutcomeFromError(err error) string {
	if deliveryErr, ok := email.AsDeliveryError(err); ok {
		if deliveryErr.Temporary() {
			return "temporary_error"
		}
		return observabilityOutcomePermanentError
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "temporary_error"
	}
	return observabilityOutcomePermanentError
}

func pollOutcomeFromError(err error) string {
	return submissionOutcomeFromError(err)
}

func (w *Worker) lastErrorFromProviderError(err error, now time.Time) *LastError {
	if deliveryErr, ok := email.AsDeliveryError(err); ok {
		return &LastError{
			Message:   deliveryErr.Error(),
			Provider:  deliveryErr.ProviderName(),
			Temporary: deliveryErr.Temporary(),
			Timestamp: now,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &LastError{
			Message:   err.Error(),
			Provider:  spoolProviderName,
			Temporary: true,
			Timestamp: now,
		}
	}
	return &LastError{
		Message:   err.Error(),
		Provider:  spoolProviderName,
		Temporary: false,
		Timestamp: now,
	}
}

func lastErrorFromSubmissionFailure(failure *email.SubmissionFailure, now time.Time, fallback string) *LastError {
	if failure == nil {
		return &LastError{Message: fallback, Provider: spoolProviderName, Temporary: false, Timestamp: now}
	}
	message := failure.Message
	if message == "" {
		message = fallback
	}
	return &LastError{
		Message:   message,
		Provider:  spoolProviderName,
		Temporary: failure.Temporary,
		Timestamp: now,
	}
}

func submissionFailureMessage(state email.SubmissionState) string {
	switch state {
	case email.SubmissionStateCanceled:
		return "provider canceled submitted operation"
	default:
		return "provider failed submitted operation"
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
