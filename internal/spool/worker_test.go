package spool

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestNewWorkerRejectsNilStore(t *testing.T) {
	t.Parallel()

	_, err := NewWorker(testWorkerLogger(), nil, stubWorkerProvider{}, time.Second)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestNewWorkerRejectsNilProvider(t *testing.T) {
	t.Parallel()

	_, err := NewWorker(testWorkerLogger(), &stubWorkerStore{}, nil, time.Second)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWorkerRecoverDelegatesToStore(t *testing.T) {
	t.Parallel()

	want := RecoveryResult{
		Requeued: []Record{{ID: testRecordID(0x401)}},
	}
	store := &stubWorkerStore{recoverResult: want}

	worker, err := NewWorker(testWorkerLogger(), store, stubWorkerProvider{}, time.Second)
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
	if len(got.Requeued) != 1 || got.Requeued[0].ID != want.Requeued[0].ID {
		t.Fatalf("unexpected recovery result: %#v", got)
	}
}

func TestWorkerStartExitsCleanlyOnCancellation(t *testing.T) {
	t.Parallel()

	worker, err := NewWorker(testWorkerLogger(), &stubWorkerStore{}, stubWorkerProvider{}, time.Second)
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Start(ctx)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker shutdown")
	}
}

func TestWorkerStartDoesNotCallProviderSubmitOrPoll(t *testing.T) {
	t.Parallel()

	worker, err := NewWorker(testWorkerLogger(), &stubWorkerStore{}, panicWorkerProvider{}, time.Second)
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Start(ctx)
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker shutdown")
	}
}

func testWorkerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubWorkerStore struct {
	recoverResult RecoveryResult
	recoverErr    error
	recoverCalls  int
}

func (*stubWorkerStore) Enqueue(context.Context, email.Message) (Record, error) {
	panic("unexpected Enqueue call")
}

func (*stubWorkerStore) ClaimReady(context.Context, time.Time) (Record, bool, error) {
	panic("unexpected ClaimReady call")
}

func (*stubWorkerStore) MarkSubmitted(context.Context, Record, string, string, time.Time) (Record, error) {
	panic("unexpected MarkSubmitted call")
}

func (*stubWorkerStore) MarkRetry(context.Context, Record, time.Time, *LastError) (Record, error) {
	panic("unexpected MarkRetry call")
}

func (*stubWorkerStore) MarkSucceeded(context.Context, Record) (Record, error) {
	panic("unexpected MarkSucceeded call")
}

func (*stubWorkerStore) MarkDeadLetter(context.Context, Record, *LastError) (Record, error) {
	panic("unexpected MarkDeadLetter call")
}

func (s *stubWorkerStore) Recover(context.Context, time.Time) (RecoveryResult, error) {
	s.recoverCalls++
	return s.recoverResult, s.recoverErr
}

type stubWorkerProvider struct{}

func (stubWorkerProvider) Send(context.Context, email.Message) error { return nil }

func (stubWorkerProvider) Submit(context.Context, email.Message, string) (email.SubmissionResult, error) {
	return email.SubmissionResult{State: email.SubmissionStateSucceeded}, nil
}

func (stubWorkerProvider) Poll(context.Context, string) (email.SubmissionStatus, error) {
	return email.SubmissionStatus{State: email.SubmissionStateSucceeded}, nil
}

type panicWorkerProvider struct{}

func (panicWorkerProvider) Send(context.Context, email.Message) error {
	panic("unexpected Send call")
}

func (panicWorkerProvider) Submit(context.Context, email.Message, string) (email.SubmissionResult, error) {
	panic("unexpected Submit call")
}

func (panicWorkerProvider) Poll(context.Context, string) (email.SubmissionStatus, error) {
	panic("unexpected Poll call")
}

var _ Store = (*stubWorkerStore)(nil)
var _ email.Provider = stubWorkerProvider{}
var _ email.Provider = panicWorkerProvider{}

func TestWorkerRecoverReturnsStoreError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("recover failed")
	store := &stubWorkerStore{recoverErr: wantErr}
	worker, err := NewWorker(testWorkerLogger(), store, stubWorkerProvider{}, time.Second)
	if err != nil {
		t.Fatalf("NewWorker() error: %v", err)
	}

	_, err = worker.Recover(context.Background(), time.Now().UTC())
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: got %v want %v", err, wantErr)
	}
}
