package spool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestNewWorkerRejectsNilStore(t *testing.T) {
	t.Parallel()

	_, err := NewWorker(testWorkerLogger(), nil, stubWorkerProvider{}, testWorkerConfig())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestNewWorkerRejectsNilProvider(t *testing.T) {
	t.Parallel()

	_, err := NewWorker(testWorkerLogger(), &stubWorkerStore{}, nil, testWorkerConfig())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWorkerRecoverDelegatesToStore(t *testing.T) {
	t.Parallel()

	want := RecoveryResult{Requeued: []Record{{ID: testRecordID(0x401)}}}
	store := &stubWorkerStore{recoverResult: want}

	worker, err := NewWorker(testWorkerLogger(), store, stubWorkerProvider{}, testWorkerConfig())
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}

	got, err := worker.Recover(context.Background(), time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatalf("Recover() error: %v", err)
	}
	if store.recoverCalls != 1 {
		t.Fatalf("unexpected recover call count: got %d want 1", store.recoverCalls)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected recovery result: %#v", got)
	}
}

func TestWorkerProcessQueuedOnceImmediateSuccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(1), State: StateWorking, Attempt: 0, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSucceededFn: func(_ context.Context, got Record) (Record, error) {
			if got.ProviderMessageID != "msg-1" {
				t.Fatalf("unexpected provider message id: %q", got.ProviderMessageID)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-1"}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markSucceededCalls != 1 {
		t.Fatalf("unexpected MarkSucceeded call count: %d", store.markSucceededCalls)
	}
}

func TestWorkerProcessQueuedOnceImmediateSuccessFinalizesAfterParentCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	rec := Record{ID: testRecordID(0x31), State: StateWorking, Attempt: 0, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSucceededFn: func(ctx context.Context, got Record) (Record, error) {
			if got.ProviderMessageID != "msg-finalize" {
				t.Fatalf("unexpected provider message id: %q", got.ProviderMessageID)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("finalize context should not be canceled: %v", err)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected finalize context deadline")
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(ctx context.Context, _ email.Message, _ string) (email.SubmissionResult, error) {
			cancelParent()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("expected provider context cancellation, got %v", ctx.Err())
			}
			return email.SubmissionResult{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-finalize"}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(parentCtx, now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markSucceededCalls != 1 {
		t.Fatalf("unexpected MarkSucceeded call count: %d", store.markSucceededCalls)
	}
}

func TestWorkerProcessQueuedOnceRunningSubmitMarksSubmitted(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(2), State: StateWorking, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSubmittedFn: func(_ context.Context, got Record, result email.SubmissionResult, next time.Time) (Record, error) {
			if result.OperationID != rec.ID {
				t.Fatalf("unexpected operation id: %q", result.OperationID)
			}
			if result.OperationLocation != "loc-1" {
				t.Fatalf("unexpected operation location: %q", result.OperationLocation)
			}
			if result.ProviderMessageID != "msg-2" {
				t.Fatalf("unexpected provider message id: %q", result.ProviderMessageID)
			}
			if !next.Equal(now.Add(3 * time.Second)) {
				t.Fatalf("unexpected next attempt: %s", next)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{
				State:             email.SubmissionStateRunning,
				OperationID:       rec.ID,
				OperationLocation: "loc-1",
				ProviderMessageID: "msg-2",
				RetryAfter:        3 * time.Second,
			}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markSubmittedCalls != 1 {
		t.Fatalf("unexpected MarkSubmitted call count: %d", store.markSubmittedCalls)
	}
}

func TestWorkerProcessQueuedOnceRunningSubmitFinalizesAfterParentCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	rec := Record{ID: testRecordID(0x32), State: StateWorking, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSubmittedFn: func(ctx context.Context, got Record, result email.SubmissionResult, next time.Time) (Record, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("finalize context should not be canceled: %v", err)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected finalize context deadline")
			}
			if result.OperationID != rec.ID {
				t.Fatalf("unexpected operation id: %q", result.OperationID)
			}
			if !next.Equal(now.Add(2 * time.Second)) {
				t.Fatalf("unexpected next attempt: %s", next)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(ctx context.Context, _ email.Message, _ string) (email.SubmissionResult, error) {
			cancelParent()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("expected provider context cancellation, got %v", ctx.Err())
			}
			return email.SubmissionResult{
				State:       email.SubmissionStateRunning,
				OperationID: rec.ID,
				RetryAfter:  2 * time.Second,
			}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(parentCtx, now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markSubmittedCalls != 1 {
		t.Fatalf("unexpected MarkSubmitted call count: %d", store.markSubmittedCalls)
	}
}

func TestWorkerProcessQueuedOnceTemporarySubmitErrorRequeues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(3), State: StateWorking, Attempt: 0, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markRetryFn: func(_ context.Context, got Record, next time.Time, lastErr *LastError) (Record, error) {
			if !next.Equal(now.Add(testWorkerConfig().RetryBaseDelay)) {
				t.Fatalf("unexpected retry next attempt: %s", next)
			}
			if lastErr == nil || !lastErr.Temporary {
				t.Fatalf("expected temporary last error, got %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{}, stubDeliveryError{temporary: true, statusCode: 503}
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markRetryCalls != 1 {
		t.Fatalf("unexpected MarkRetry call count: %d", store.markRetryCalls)
	}
}

func TestWorkerProcessQueuedOncePermanentSubmitErrorDeadLetters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(4), State: StateWorking, Attempt: 0, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markDeadLetterFn: func(_ context.Context, got Record, lastErr *LastError) (Record, error) {
			if lastErr == nil || lastErr.Temporary {
				t.Fatalf("expected permanent last error, got %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{}, stubDeliveryError{temporary: false, statusCode: 400}
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markDeadLetterCalls != 1 {
		t.Fatalf("unexpected MarkDeadLetter call count: %d", store.markDeadLetterCalls)
	}
}

func TestWorkerProcessSubmittedOnceRunningReschedulesWithoutIncrementingAttempt(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(5), State: StateSubmitted, Attempt: 1, OperationID: "op-5", ProviderMessageID: "msg-old", FirstSubmittedAt: now.Add(-time.Hour)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markRetryFn: func(_ context.Context, got Record, next time.Time, lastErr *LastError) (Record, error) {
			if got.Attempt != 1 {
				t.Fatalf("attempt changed unexpectedly: %d", got.Attempt)
			}
			if got.ProviderMessageID != "msg-new" {
				t.Fatalf("expected updated provider message id, got %q", got.ProviderMessageID)
			}
			if lastErr != nil {
				t.Fatalf("expected nil last error, got %#v", lastErr)
			}
			if !next.Equal(now.Add(5 * time.Second)) {
				t.Fatalf("unexpected next attempt: %s", next)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			return email.SubmissionStatus{State: email.SubmissionStateRunning, RetryAfter: 5 * time.Second, ProviderMessageID: "msg-new"}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processSubmittedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessSubmittedOnceSuccessMarksSucceeded(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(6), State: StateSubmitted, Attempt: 1, OperationID: "op-6", ProviderMessageID: "msg-old", FirstSubmittedAt: now.Add(-time.Hour)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSucceededFn: func(_ context.Context, got Record) (Record, error) {
			if got.ProviderMessageID != "msg-new" {
				t.Fatalf("expected updated provider message id, got %q", got.ProviderMessageID)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			return email.SubmissionStatus{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-new"}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }
	worked, err := worker.processSubmittedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessSubmittedOnceSuccessFinalizesAfterParentCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	rec := Record{ID: testRecordID(0x33), State: StateSubmitted, Attempt: 1, OperationID: "op-33", ProviderMessageID: "msg-old", FirstSubmittedAt: now.Add(-time.Hour)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSucceededFn: func(ctx context.Context, got Record) (Record, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("finalize context should not be canceled: %v", err)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected finalize context deadline")
			}
			if got.ProviderMessageID != "msg-new" {
				t.Fatalf("expected updated provider message id, got %q", got.ProviderMessageID)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(ctx context.Context, _ string) (email.SubmissionStatus, error) {
			cancelParent()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("expected provider context cancellation, got %v", ctx.Err())
			}
			return email.SubmissionStatus{State: email.SubmissionStateSucceeded, ProviderMessageID: "msg-new"}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processSubmittedOnce(parentCtx, now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markSucceededCalls != 1 {
		t.Fatalf("unexpected MarkSucceeded call count: %d", store.markSucceededCalls)
	}
}

func TestWorkerProcessSubmittedOnceTerminalFailureDeadLetters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(7), State: StateSubmitted, Attempt: 1, OperationID: "op-7", FirstSubmittedAt: now.Add(-time.Hour)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markDeadLetterFn: func(_ context.Context, got Record, lastErr *LastError) (Record, error) {
			if lastErr == nil || lastErr.Message != "failed" {
				t.Fatalf("unexpected last error: %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			return email.SubmissionStatus{State: email.SubmissionStateFailed, Failure: &email.SubmissionFailure{Message: "failed", Temporary: true}}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }
	worked, err := worker.processSubmittedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessSubmittedOnceTimeoutDeadLettersWithoutPolling(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 12, 0, 1, 0, time.UTC)
	rec := Record{ID: testRecordID(8), State: StateSubmitted, Attempt: 1, OperationID: "op-8", FirstSubmittedAt: now.Add(-DefaultSubmittedTimeout).Add(-time.Second)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markDeadLetterFn: func(_ context.Context, got Record, lastErr *LastError) (Record, error) {
			if lastErr == nil || lastErr.Temporary {
				t.Fatalf("expected permanent timeout error, got %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			t.Fatal("unexpected Poll call")
			return email.SubmissionStatus{}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }
	worked, err := worker.processSubmittedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessSubmittedOnceTimeoutFinalizesAfterParentCancellation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 12, 12, 0, 1, 0, time.UTC)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	rec := Record{ID: testRecordID(0x34), State: StateSubmitted, Attempt: 1, OperationID: "op-34", FirstSubmittedAt: now.Add(-DefaultSubmittedTimeout).Add(-time.Second)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markDeadLetterFn: func(ctx context.Context, got Record, lastErr *LastError) (Record, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("finalize context should not be canceled: %v", err)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected finalize context deadline")
			}
			if lastErr == nil || lastErr.Temporary {
				t.Fatalf("expected permanent timeout error, got %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			t.Fatal("unexpected Poll call")
			return email.SubmissionStatus{}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processSubmittedOnce(parentCtx, now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if store.markDeadLetterCalls != 1 {
		t.Fatalf("unexpected MarkDeadLetter call count: %d", store.markDeadLetterCalls)
	}
}

func TestWorkerProcessQueuedOnceProviderTimeoutRetries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(9), State: StateWorking, Attempt: 0, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markRetryFn: func(_ context.Context, got Record, _ time.Time, lastErr *LastError) (Record, error) {
			if lastErr == nil || !lastErr.Temporary {
				t.Fatalf("expected temporary timeout error, got %#v", lastErr)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(ctx context.Context, _ email.Message, _ string) (email.SubmissionResult, error) {
			<-ctx.Done()
			return email.SubmissionResult{}, ctx.Err()
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }
	worker.cfg.SubmitTimeout = time.Millisecond

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processQueuedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessSubmittedOnceProviderTimeoutReschedules(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(10), State: StateSubmitted, Attempt: 1, OperationID: "op-10", FirstSubmittedAt: now.Add(-time.Hour)}
	store := &stubWorkerStore{
		nextSubmittedReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markRetryFn: func(_ context.Context, got Record, next time.Time, lastErr *LastError) (Record, error) {
			if lastErr == nil || !lastErr.Temporary {
				t.Fatalf("expected temporary timeout error, got %#v", lastErr)
			}
			if !next.Equal(now.Add(testWorkerConfig().PollInterval)) {
				t.Fatalf("unexpected next attempt: %s", next)
			}
			return got, nil
		},
	}
	provider := stubWorkerProvider{
		pollFn: func(ctx context.Context, _ string) (email.SubmissionStatus, error) {
			<-ctx.Done()
			return email.SubmissionStatus{}, ctx.Err()
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }
	worker.cfg.SubmitTimeout = time.Millisecond

	worked, err := worker.processSubmittedOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("processSubmittedOnce() error: %v", err)
	}
	if !worked {
		t.Fatal("expected work to be performed")
	}
}

func TestWorkerProcessQueuedOnceReturnsFinalizeError(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: testRecordID(0x35), State: StateWorking, Message: email.Message{To: []string{"to@example.com"}}}
	store := &stubWorkerStore{
		claimReadyFn: func(context.Context, time.Time) (Record, bool, error) { return rec, true, nil },
		markSucceededFn: func(context.Context, Record) (Record, error) {
			return Record{}, errors.New("persist failed")
		},
	}
	provider := stubWorkerProvider{
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			return email.SubmissionResult{State: email.SubmissionStateSucceeded}, nil
		},
	}

	worker := mustNewWorker(t, store, provider)
	worker.now = func() time.Time { return now }

	worked, err := worker.processQueuedOnce(context.Background(), now)
	if !worked {
		t.Fatal("expected work to be performed")
	}
	if err == nil || !strings.Contains(err.Error(), `mark queued record succeeded for record "`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkerStartStaysSingleThreadedAndExitsOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var order []string
	var activeCalls int32
	store := &stubWorkerStore{}
	submittedDelivered := false
	queuedDelivered := false
	store.nextSubmittedReadyFn = func(context.Context, time.Time) (Record, bool, error) {
		order = append(order, "nextSubmittedReady")
		if submittedDelivered {
			return Record{}, false, nil
		}
		submittedDelivered = true
		return Record{ID: testRecordID(11), State: StateSubmitted, OperationID: "op-11", FirstSubmittedAt: time.Now().UTC()}, true, nil
	}
	store.markSucceededFn = func(_ context.Context, rec Record) (Record, error) {
		order = append(order, "markSucceeded:"+rec.ID)
		return rec, nil
	}
	store.claimReadyFn = func(context.Context, time.Time) (Record, bool, error) {
		order = append(order, "claimReady")
		if queuedDelivered {
			return Record{}, false, nil
		}
		queuedDelivered = true
		return Record{ID: testRecordID(12), State: StateWorking, Message: email.Message{To: []string{"to@example.com"}}}, true, nil
	}
	provider := stubWorkerProvider{
		pollFn: func(context.Context, string) (email.SubmissionStatus, error) {
			if atomic.AddInt32(&activeCalls, 1) != 1 {
				t.Fatal("provider calls overlapped")
			}
			defer atomic.AddInt32(&activeCalls, -1)
			order = append(order, "poll")
			return email.SubmissionStatus{State: email.SubmissionStateSucceeded}, nil
		},
		submitFn: func(context.Context, email.Message, string) (email.SubmissionResult, error) {
			if atomic.AddInt32(&activeCalls, 1) != 1 {
				t.Fatal("provider calls overlapped")
			}
			defer atomic.AddInt32(&activeCalls, -1)
			order = append(order, "submit")
			return email.SubmissionResult{State: email.SubmissionStateSucceeded}, nil
		},
	}
	worker := mustNewWorker(t, store, provider)
	worker.sleep = func(ctx context.Context, d time.Duration) error {
		order = append(order, "sleep")
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	wantOrder := []string{
		"nextSubmittedReady",
		"poll",
		"markSucceeded:" + testRecordID(11),
		"claimReady",
		"submit",
		"markSucceeded:" + testRecordID(12),
		"nextSubmittedReady",
		"claimReady",
		"sleep",
	}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("unexpected order:\n got %#v\nwant %#v", order, wantOrder)
	}
}

func mustNewWorker(t *testing.T, store Store, provider email.Provider) *Worker {
	t.Helper()
	worker, err := NewWorker(testWorkerLogger(), store, provider, testWorkerConfig())
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}
	return worker
}

func testWorkerConfig() WorkerConfig {
	return WorkerConfig{
		SubmitTimeout:    time.Second,
		FinalizeTimeout:  DefaultFinalizeTimeout,
		PollInterval:     DefaultPollInterval,
		RetryAttempts:    3,
		RetryBaseDelay:   time.Second,
		SubmittedTimeout: DefaultSubmittedTimeout,
	}
}

func testWorkerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubWorkerStore struct {
	claimReadyFn         func(context.Context, time.Time) (Record, bool, error)
	nextSubmittedReadyFn func(context.Context, time.Time) (Record, bool, error)
	markSubmittedFn      func(context.Context, Record, email.SubmissionResult, time.Time) (Record, error)
	markRetryFn          func(context.Context, Record, time.Time, *LastError) (Record, error)
	markSucceededFn      func(context.Context, Record) (Record, error)
	markDeadLetterFn     func(context.Context, Record, *LastError) (Record, error)
	recoverFn            func(context.Context, time.Time) (RecoveryResult, error)
	recoverResult        RecoveryResult
	recoverErr           error
	recoverCalls         int
	markSubmittedCalls   int
	markRetryCalls       int
	markSucceededCalls   int
	markDeadLetterCalls  int
}

func (*stubWorkerStore) Enqueue(context.Context, email.Message) (Record, error) {
	panic("unexpected Enqueue call")
}

func (s *stubWorkerStore) ClaimReady(ctx context.Context, now time.Time) (Record, bool, error) {
	if s.claimReadyFn == nil {
		return Record{}, false, nil
	}
	return s.claimReadyFn(ctx, now)
}

func (s *stubWorkerStore) NextSubmittedReady(ctx context.Context, now time.Time) (Record, bool, error) {
	if s.nextSubmittedReadyFn == nil {
		return Record{}, false, nil
	}
	return s.nextSubmittedReadyFn(ctx, now)
}

func (s *stubWorkerStore) MarkSubmitted(ctx context.Context, rec Record, result email.SubmissionResult, next time.Time) (Record, error) {
	s.markSubmittedCalls++
	if s.markSubmittedFn == nil {
		return rec, nil
	}
	return s.markSubmittedFn(ctx, rec, result, next)
}

func (s *stubWorkerStore) MarkRetry(ctx context.Context, rec Record, next time.Time, lastErr *LastError) (Record, error) {
	s.markRetryCalls++
	if s.markRetryFn == nil {
		return rec, nil
	}
	return s.markRetryFn(ctx, rec, next, lastErr)
}

func (s *stubWorkerStore) MarkSucceeded(ctx context.Context, rec Record) (Record, error) {
	s.markSucceededCalls++
	if s.markSucceededFn == nil {
		return rec, nil
	}
	return s.markSucceededFn(ctx, rec)
}

func (s *stubWorkerStore) MarkDeadLetter(ctx context.Context, rec Record, lastErr *LastError) (Record, error) {
	s.markDeadLetterCalls++
	if s.markDeadLetterFn == nil {
		return rec, nil
	}
	return s.markDeadLetterFn(ctx, rec, lastErr)
}

func (s *stubWorkerStore) Recover(ctx context.Context, now time.Time) (RecoveryResult, error) {
	s.recoverCalls++
	if s.recoverFn != nil {
		return s.recoverFn(ctx, now)
	}
	return s.recoverResult, s.recoverErr
}

type stubWorkerProvider struct {
	submitFn func(context.Context, email.Message, string) (email.SubmissionResult, error)
	pollFn   func(context.Context, string) (email.SubmissionStatus, error)
}

type stubDeliveryError struct {
	temporary  bool
	statusCode int
}

func (e stubDeliveryError) Error() string        { return "delivery failure" }
func (e stubDeliveryError) ProviderName() string { return "stub" }
func (e stubDeliveryError) Temporary() bool      { return e.temporary }
func (e stubDeliveryError) HTTPStatusCode() int  { return e.statusCode }

func (p stubWorkerProvider) Submit(ctx context.Context, msg email.Message, operationID string) (email.SubmissionResult, error) {
	if p.submitFn != nil {
		return p.submitFn(ctx, msg, operationID)
	}
	return email.SubmissionResult{State: email.SubmissionStateSucceeded}, nil
}

func (p stubWorkerProvider) Poll(ctx context.Context, operationID string) (email.SubmissionStatus, error) {
	if p.pollFn != nil {
		return p.pollFn(ctx, operationID)
	}
	return email.SubmissionStatus{State: email.SubmissionStateSucceeded}, nil
}

var _ Store = (*stubWorkerStore)(nil)
var _ email.Provider = stubWorkerProvider{}
