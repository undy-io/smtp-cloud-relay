package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/observability"
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

func (s *stubStore) NextSubmittedReady(context.Context, time.Time) (spool.Record, bool, error) {
	panic("unexpected NextSubmittedReady call")
}

func (s *stubStore) MarkSubmitted(context.Context, spool.Record, email.SubmissionResult, time.Time) (spool.Record, error) {
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
	_, err := NewHandler(testLogger(), "rewrite", policy, &stubStore{}, 0, nil)
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

func TestHandlerSuccessfulEnqueueIncrementsMetric(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	metrics := observability.NewMetrics(nil)
	handler := mustNewHandlerWithMetrics(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1, metrics)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	body := scrapeRelayMetrics(t, metrics)
	if !strings.Contains(body, "smtp_relay_enqueued_total 1") {
		t.Fatalf("expected enqueue success metric, got:\n%s", body)
	}
	if !strings.Contains(body, "smtp_relay_enqueue_failures_total 0") {
		t.Fatalf("expected zero enqueue failures, got:\n%s", body)
	}
}

func TestHandlerStoreErrorIncrementsEnqueueFailureMetric(t *testing.T) {
	t.Parallel()

	store := &stubStore{
		enqueueFunc: func(context.Context, email.Message) (spool.Record, error) {
			return spool.Record{}, &spool.StoreError{Op: "enqueue", Err: errors.New("disk full")}
		},
	}
	metrics := observability.NewMetrics(nil)
	handler := mustNewHandlerWithMetrics(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1, metrics)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	if _, ok := spool.AsStoreError(err); !ok {
		t.Fatalf("expected StoreError, got %T: %v", err, err)
	}

	body := scrapeRelayMetrics(t, metrics)
	if !strings.Contains(body, "smtp_relay_enqueue_failures_total 1") {
		t.Fatalf("expected enqueue failure metric, got:\n%s", body)
	}
}

func TestHandlerBusyAndSenderPolicyRejectionDoNotIncrementEnqueueFailureMetric(t *testing.T) {
	t.Parallel()

	metrics := observability.NewMetrics(nil)

	store := &stubStore{}
	strictHandler := mustNewHandlerWithMetrics(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyStrict,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}, store, 1, metrics)
	if err := strictHandler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		HeaderFrom:   "blocked@example.org",
		To:           []string{"to@example.com"},
	}); err == nil {
		t.Fatal("expected sender policy error")
	}

	block := make(chan struct{})
	started := make(chan struct{})
	busyStore := &stubStore{
		enqueueFunc: func(ctx context.Context, msg email.Message) (spool.Record, error) {
			close(started)
			<-block
			return spool.Record{ID: "busy", Message: msg}, nil
		},
	}
	busyHandler := mustNewHandlerWithMetrics(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, busyStore, 1, metrics)
	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- busyHandler.HandleMessage(context.Background(), email.Message{
			EnvelopeFrom: "from@example.com",
			To:           []string{"to@example.com"},
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for enqueue start")
	}
	if _, ok := AsBusyError(busyHandler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})); !ok {
		t.Fatal("expected BusyError")
	}
	close(block)
	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("unexpected first handler error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first handler")
	}

	body := scrapeRelayMetrics(t, metrics)
	if !strings.Contains(body, "smtp_relay_enqueue_failures_total 0") {
		t.Fatalf("expected enqueue failures to remain zero, got:\n%s", body)
	}
}

func TestHandlerDuplicateMessagesAreEnqueuedSeparately(t *testing.T) {
	t.Parallel()

	store, err := spool.NewSpoolStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpoolStore() error: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
	}()

	handler := mustNewHandler(t, email.SenderPolicyOptions{Mode: email.SenderPolicyRewrite}, store, 1)
	msg := email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "body",
	}

	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("first HandleMessage() error: %v", err)
	}
	if err := handler.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("second HandleMessage() error: %v", err)
	}

	first, ok, err := store.ClaimReady(context.Background(), time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("first ClaimReady() = (%#v, %t, %v)", first, ok, err)
	}
	second, ok, err := store.ClaimReady(context.Background(), time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("second ClaimReady() = (%#v, %t, %v)", second, ok, err)
	}

	if first.ID == second.ID {
		t.Fatalf("expected duplicate submission to produce distinct records, got shared id %q", first.ID)
	}
	if !reflect.DeepEqual(first.Message, second.Message) {
		t.Fatalf("expected duplicate submission payloads to match:\nfirst: %#v\nsecond: %#v", first.Message, second.Message)
	}
}

func mustNewHandler(t *testing.T, opts email.SenderPolicyOptions, store spool.Store, maxInflight int) *Handler {
	t.Helper()

	policy := mustTestSenderPolicy(t, opts)
	handler, err := NewHandler(testLogger(), string(opts.Mode), policy, store, maxInflight, nil)
	if err != nil {
		t.Fatalf("NewHandler() error: %v", err)
	}
	return handler
}

func mustNewHandlerWithMetrics(t *testing.T, opts email.SenderPolicyOptions, store spool.Store, maxInflight int, metrics *observability.Metrics) *Handler {
	t.Helper()

	policy := mustTestSenderPolicy(t, opts)
	handler, err := NewHandler(testLogger(), string(opts.Mode), policy, store, maxInflight, metrics)
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

func scrapeRelayMetrics(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}
