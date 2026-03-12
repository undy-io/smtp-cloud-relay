package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
)

type stubStore struct {
	mu           sync.Mutex
	enqueueFunc  func(context.Context, email.Message) (spool.Record, error)
	enqueueCount int
	lastMessage  email.Message
}

func (s *stubStore) Enqueue(ctx context.Context, msg email.Message) (spool.Record, error) {
	s.mu.Lock()
	s.enqueueCount++
	s.lastMessage = msg
	enqueueFunc := s.enqueueFunc
	s.mu.Unlock()
	if enqueueFunc != nil {
		return enqueueFunc(ctx, msg)
	}
	return spool.Record{ID: "test-record", Message: msg}, nil
}

func (s *stubStore) ClaimReady(context.Context, time.Time) (spool.Record, bool, error) {
	panic("unexpected ClaimReady call")
}

func (s *stubStore) MarkSubmitted(context.Context, spool.Record, string, string, time.Time) (spool.Record, error) {
	panic("unexpected MarkSubmitted call")
}

func (s *stubStore) MarkRetry(context.Context, spool.Record, time.Time, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkRetry call")
}

func (s *stubStore) MarkSucceeded(context.Context, spool.Record) (spool.Record, error) {
	panic("unexpected MarkSucceeded call")
}

func (s *stubStore) MarkDeadLetter(context.Context, spool.Record, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkDeadLetter call")
}

func (s *stubStore) Recover(context.Context, time.Time) (spool.RecoveryResult, error) {
	panic("unexpected Recover call")
}

func (s *stubStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueCount
}

func (s *stubStore) message() email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastMessage
}

func TestNewHandlerRejectsInvalidMaxInflight(t *testing.T) {
	t.Parallel()

	policy := mustTestSenderPolicy(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite})
	_, err := NewHandler(testLogger(), policy, &stubStore{}, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandlerStrictPolicyRejectsBeforeEnqueue(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	handler := mustNewHandler(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyStrict,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}, store, 1)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "sender@blocked.example.com",
		To:           []string{"to@example.com"},
	})
	if _, ok := email.AsSenderPolicyError(err); !ok {
		t.Fatalf("expected sender policy error, got %T: %v", err, err)
	}
	if store.count() != 0 {
		t.Fatalf("expected store not to be called, count=%d", store.count())
	}
}

func TestHandlerRewriteEnqueuesPolicyNormalizedMessage(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	handler := mustNewHandler(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}, store, 1)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		ReplyTo: []string{
			"reply@allowed.example.com",
			"other@allowed.example.com",
		},
		To: []string{"to@example.com"},
	})
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	msg := store.message()
	if len(msg.ReplyTo) != 1 || msg.ReplyTo[0] != "reply@allowed.example.com" {
		t.Fatalf("unexpected enqueued reply-to: %#v", msg.ReplyTo)
	}
	if msg.HeaderFrom != "header@example.com" {
		t.Fatalf("unexpected enqueued header from: %q", msg.HeaderFrom)
	}
}

func TestHandlerInflightSaturationReturnsBusyError(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	started := make(chan struct{})
	store := &stubStore{
		enqueueFunc: func(ctx context.Context, msg email.Message) (spool.Record, error) {
			close(started)
			<-block
			return spool.Record{ID: "test-record", Message: msg}, nil
		},
	}
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- handler.HandleMessage(context.Background(), email.Message{
			EnvelopeFrom: "from@example.com",
			To:           []string{"to@example.com"},
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first enqueue to start")
	}

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	if busyErr, ok := AsBusyError(err); !ok {
		t.Fatalf("expected BusyError, got %T: %v", err, err)
	} else if busyErr.Limit != 1 {
		t.Fatalf("unexpected BusyError limit: %d", busyErr.Limit)
	}

	close(block)
	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("unexpected first handler error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first enqueue to finish")
	}
}

func TestHandlerReturnsStoreError(t *testing.T) {
	t.Parallel()

	store := &stubStore{
		enqueueFunc: func(context.Context, email.Message) (spool.Record, error) {
			return spool.Record{}, &spool.StoreError{Op: "enqueue", Err: errors.New("disk full")}
		},
	}
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	if _, ok := spool.AsStoreError(err); !ok {
		t.Fatalf("expected StoreError, got %T: %v", err, err)
	}
}

func TestHandlerSuccessfulEnqueueReturnsNil(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "body",
	})
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("expected one enqueue, got %d", store.count())
	}
	if store.message().TextBody != "body" {
		t.Fatalf("unexpected enqueued message: %#v", store.message())
	}
}

func mustNewHandler(t *testing.T, opts email.SenderPolicyOptions, store spool.Store, maxInflight int) *Handler {
	t.Helper()

	policy := mustTestSenderPolicy(t, opts)
	handler, err := NewHandler(testLogger(), policy, store, maxInflight)
	if err != nil {
		t.Fatalf("NewHandler() error: %v", err)
	}
	return handler
}

func mustTestSenderPolicy(t *testing.T, opts email.SenderPolicyOptions) email.SenderPolicy {
	t.Helper()

	policy, err := email.NewSenderPolicy(opts)
	if err != nil {
		t.Fatalf("NewSenderPolicy() error: %v", err)
	}
	return policy
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
