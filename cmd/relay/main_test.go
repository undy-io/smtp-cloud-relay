package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/observability"
	"github.com/undy-io/smtp-cloud-relay/internal/providers"
	smtprelay "github.com/undy-io/smtp-cloud-relay/internal/smtp"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
)

func TestBuildMessageHandlerRejectsInvalidSenderPolicyRegex(t *testing.T) {
	t.Parallel()

	handlerCfg := config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
		SenderAllowedDomains: []string{"re:("},
	}

	_, _, err := buildMessageHandler(handlerCfg, testMainLogger(), &stubRelayStore{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildMessageHandlerRejectsInvalidSenderPolicyGlob(t *testing.T) {
	t.Parallel()

	handlerCfg := config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
		SenderAllowedDomains: []string{"glob:*.*.example.com"},
	}

	_, _, err := buildMessageHandler(handlerCfg, testMainLogger(), &stubRelayStore{}, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildMessageHandlerUsesSpoolStore(t *testing.T) {
	t.Parallel()

	handler, handlerTimeout, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}
	if handler == nil {
		t.Fatal("expected handler, got nil")
	}
	if handlerTimeout != 0 {
		t.Fatalf("unexpected handler timeout: got %s want 0", handlerTimeout)
	}
}

func TestBuildMessageHandlerMapsSenderPolicyError(t *testing.T) {
	t.Parallel()

	handler, _, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "strict",
		SenderAllowedDomains: []string{"allowed.example.com"},
	}, testMainLogger(), &stubRelayStore{}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}

	err = handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "sender@blocked.example.com",
		To:           []string{"to@example.com"},
	})
	assertSMTPError(t, err, 554, gosmtp.EnhancedCode{5, 7, 1})
}

func TestBuildMessageHandlerMapsBusyError(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	started := make(chan struct{})
	handler, _, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(ctx context.Context, msg email.Message) (spool.Record, error) {
			close(started)
			<-block
			return spool.Record{ID: "test-record", Message: msg}, nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}

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

	err = handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	assertSMTPError(t, err, 451, gosmtp.EnhancedCode{4, 3, 2})

	close(block)
	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("unexpected first handler error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first handler to finish")
	}
}

func TestBuildMessageHandlerMapsStoreError(t *testing.T) {
	t.Parallel()

	handler, _, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(context.Context, email.Message) (spool.Record, error) {
			return spool.Record{}, &spool.StoreError{Op: "enqueue", Err: errors.New("disk full")}
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}

	err = handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	assertSMTPError(t, err, 451, gosmtp.EnhancedCode{4, 3, 0})
}

func TestBuildMessageHandlerMapsUnexpectedEnqueueError(t *testing.T) {
	t.Parallel()

	handler, _, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(context.Context, email.Message) (spool.Record, error) {
			return spool.Record{}, errors.New("boom")
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}

	err = handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	assertSMTPError(t, err, 451, gosmtp.EnhancedCode{4, 3, 0})
}

func TestBuildMessageHandlerEnqueueAdapterReturnsStoreErrorOnWire(t *testing.T) {
	t.Parallel()

	addr := freeSMTPTCPAddr(t)
	handlerDeadlineCh := make(chan bool, 1)
	handler, handlerTimeout, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(ctx context.Context, _ email.Message) (spool.Record, error) {
			_, hasDeadline := ctx.Deadline()
			handlerDeadlineCh <- hasDeadline
			return spool.Record{}, &spool.StoreError{Op: "enqueue", Err: errors.New("disk full")}
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}
	if handlerTimeout != 0 {
		t.Fatalf("unexpected handler timeout: got %s want 0", handlerTimeout)
	}

	srv, err := smtprelay.NewServer(smtprelay.Config{
		ListenAddr:     addr,
		AllowedCIDRs:   []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:    false,
		RequireTLS:     false,
		HandlerTimeout: handlerTimeout,
	}, testMainLogger(), handler, nil)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	stop := startSMTPServerForMainTest(t, srv)
	defer stop()

	tp := dialSMTPConn(t, addr)
	defer tp.Close()

	sendSMTPMessage(t, tp, "from@example.com", "to@example.com", "Adapter failure", "hello relay")

	_, msg, err := tp.ReadResponse(451)
	if err != nil {
		t.Fatalf("expected enqueue adapter SMTP error response: %v", err)
	}
	if !strings.Contains(msg, "4.3.0") {
		t.Fatalf("expected enhanced status code in response, got %q", msg)
	}
	if !strings.Contains(msg, "temporary relay failure") {
		t.Fatalf("expected enqueue adapter message in response, got %q", msg)
	}

	select {
	case hasDeadline := <-handlerDeadlineCh:
		if hasDeadline {
			t.Fatal("expected enqueue handler context without extra timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for enqueue handler deadline signal")
	}

	tp.PrintfLine("QUIT")
	if _, _, err := tp.ReadResponse(221); err != nil {
		t.Fatalf("quit response: %v", err)
	}
}

func TestBuildMessageHandlerDoesNotAckBeforeEnqueueCommit(t *testing.T) {
	t.Parallel()

	addr := freeSMTPTCPAddr(t)
	started := make(chan struct{})
	release := make(chan struct{})
	handler, handlerTimeout, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(ctx context.Context, msg email.Message) (spool.Record, error) {
			close(started)
			<-release
			return spool.Record{ID: "queued-record", Message: msg}, nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}
	if handlerTimeout != 0 {
		t.Fatalf("unexpected handler timeout: got %s want 0", handlerTimeout)
	}

	srv, err := smtprelay.NewServer(smtprelay.Config{
		ListenAddr:     addr,
		AllowedCIDRs:   []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		RequireAuth:    false,
		RequireTLS:     false,
		HandlerTimeout: handlerTimeout,
	}, testMainLogger(), handler, nil)
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	stop := startSMTPServerForMainTest(t, srv)
	defer stop()

	tp := dialSMTPConn(t, addr)
	defer tp.Close()

	tp.PrintfLine("MAIL FROM:<from@example.com>")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("mail response: %v", err)
	}
	tp.PrintfLine("RCPT TO:<to@example.com>")
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("rcpt response: %v", err)
	}
	tp.PrintfLine("DATA")
	if _, _, err := tp.ReadResponse(354); err != nil {
		t.Fatalf("data response: %v", err)
	}

	tp.PrintfLine("Subject: Delayed Ack")
	tp.PrintfLine("From: from@example.com")
	tp.PrintfLine("To: to@example.com")
	tp.PrintfLine("")
	tp.PrintfLine("hello relay")
	tp.PrintfLine(".")

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for enqueue to start")
	}

	respCh := make(chan error, 1)
	go func() {
		_, _, err := tp.ReadResponse(250)
		respCh <- err
	}()

	select {
	case err := <-respCh:
		t.Fatalf("unexpected SMTP success before enqueue commit completed: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-respCh:
		if err != nil {
			t.Fatalf("expected SMTP success after enqueue commit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP success after enqueue release")
	}

	tp.PrintfLine("QUIT")
	if _, _, err := tp.ReadResponse(221); err != nil {
		t.Fatalf("quit response: %v", err)
	}
}

func TestBuildMessageHandlerMapsCanceledEnqueueToTemporaryFailure(t *testing.T) {
	t.Parallel()

	handler, _, err := buildMessageHandler(config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{
		enqueueFunc: func(ctx context.Context, _ email.Message) (spool.Record, error) {
			<-ctx.Done()
			return spool.Record{}, ctx.Err()
		},
	}, nil)
	if err != nil {
		t.Fatalf("buildMessageHandler() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = handler.HandleMessage(ctx, email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	assertSMTPError(t, err, 451, gosmtp.EnhancedCode{4, 3, 0})
}

func TestBuildRelayHandlerConstructsHandlerWithoutProviders(t *testing.T) {
	t.Parallel()

	handler, err := buildRelayHandler(config.Config{
		DeliveryMode:         "does-not-matter-here",
		SMTPMaxInflightSends: 2,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), &stubRelayStore{}, nil)
	if err != nil {
		t.Fatalf("buildRelayHandler() error: %v", err)
	}
	if handler == nil {
		t.Fatal("expected handler, got nil")
	}
}

func TestBuildMetricsUsesSpoolStoreStateCounts(t *testing.T) {
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

	if _, err := store.Enqueue(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
		TextBody:     "queued",
	}); err != nil {
		t.Fatalf("Enqueue() error: %v", err)
	}

	metrics := buildMetrics(store)
	body := scrapeMetricsText(t, metrics)
	if !strings.Contains(body, `smtp_relay_spool_records{state="queued"} 1`) {
		t.Fatalf("expected queued spool metric, got:\n%s", body)
	}
}

func TestBuildBackgroundDeliveryUsesExplicitRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, worker, err := buildBackgroundDelivery(root, testMainLogger(), testRuntime(), testWorkerConfig())
	if err != nil {
		t.Fatalf("buildBackgroundDelivery() error: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error: %v", err)
		}
	}()

	if worker == nil {
		t.Fatal("expected worker, got nil")
	}
	for _, rel := range []string{"spool.db", "payloads", "payload-orphans", "staging"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}

func TestMainWorkerConfigUsesConfigSpoolPollInterval(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		DeliveryRetryAttempts:    3,
		DeliveryRetryBaseDelayMS: 1000,
		SpoolPollIntervalMS:      2500,
	}
	runtime := testRuntime()

	workerCfg := spool.WorkerConfig{
		SubmitTimeout:    runtime.SendTimeout,
		FinalizeTimeout:  spool.DefaultFinalizeTimeout,
		PollInterval:     time.Duration(cfg.SpoolPollIntervalMS) * time.Millisecond,
		RetryAttempts:    cfg.DeliveryRetryAttempts,
		RetryBaseDelay:   time.Duration(cfg.DeliveryRetryBaseDelayMS) * time.Millisecond,
		SubmittedTimeout: spool.DefaultSubmittedTimeout,
		ProviderName:     cfg.DeliveryMode,
	}

	if workerCfg.PollInterval != 2500*time.Millisecond {
		t.Fatalf("unexpected worker PollInterval: %s", workerCfg.PollInterval)
	}
	if workerCfg.ProviderName != "" {
		t.Fatalf("unexpected zero ProviderName: %q", workerCfg.ProviderName)
	}
}

func TestMainWorkerConfigUsesDeliveryModeAsProviderName(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		DeliveryMode:             "ses",
		DeliveryRetryAttempts:    3,
		DeliveryRetryBaseDelayMS: 1000,
		SpoolPollIntervalMS:      1000,
	}
	runtime := testRuntime()

	workerCfg := spool.WorkerConfig{
		SubmitTimeout:    runtime.SendTimeout,
		FinalizeTimeout:  spool.DefaultFinalizeTimeout,
		PollInterval:     time.Duration(cfg.SpoolPollIntervalMS) * time.Millisecond,
		RetryAttempts:    cfg.DeliveryRetryAttempts,
		RetryBaseDelay:   time.Duration(cfg.DeliveryRetryBaseDelayMS) * time.Millisecond,
		SubmittedTimeout: spool.DefaultSubmittedTimeout,
		ProviderName:     cfg.DeliveryMode,
	}

	if workerCfg.ProviderName != "ses" {
		t.Fatalf("unexpected ProviderName: %q", workerCfg.ProviderName)
	}
}

func TestRunStartupRecoverySurfacesFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("recover failed")
	_, err := runStartupRecovery(context.Background(), testMainLogger(), stubRecoverer{err: wantErr}, time.Now().UTC())
	if !errors.Is(err, wantErr) {
		t.Fatalf("unexpected error: got %v want %v", err, wantErr)
	}
}

func TestProcessReadinessRequiresSMTPAndRecovery(t *testing.T) {
	t.Parallel()

	var readiness processReadiness
	if readiness.Ready() {
		t.Fatal("expected readiness to start false")
	}

	readiness.setRecoveryReady(true)
	if readiness.Ready() {
		t.Fatal("expected readiness false until smtp is ready")
	}

	readiness.setRecoveryReady(false)
	readiness.setSMTPReady(true)
	if readiness.Ready() {
		t.Fatal("expected readiness false until recovery is ready")
	}

	readiness.setRecoveryReady(true)
	if !readiness.Ready() {
		t.Fatal("expected readiness true when both conditions are ready")
	}
}

func TestObservabilityReadyzRequiresRecoveryAndSMTPReady(t *testing.T) {
	t.Parallel()

	addr := freeSMTPTCPAddr(t)
	readiness := &processReadiness{}
	server := observability.NewServer(observability.ServerConfig{
		Addr:    addr,
		ReadyFn: readiness.Ready,
		Metrics: observability.NewMetrics(nil),
	}, testMainLogger())

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Fatalf("Shutdown() error: %v", err)
		}
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("ListenAndServe() error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for observability server shutdown")
		}
	}()

	baseURL := "http://" + addr
	waitForHTTPStatus(t, baseURL+"/healthz", http.StatusOK)
	waitForHTTPStatus(t, baseURL+"/readyz", http.StatusServiceUnavailable)

	readiness.setRecoveryReady(true)
	waitForHTTPStatus(t, baseURL+"/readyz", http.StatusServiceUnavailable)

	readiness.setSMTPReady(true)
	waitForHTTPStatus(t, baseURL+"/readyz", http.StatusOK)
}

func testMainLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testRuntime() providers.Runtime {
	return providers.Runtime{
		Provider:    stubDeliveryProvider{},
		SendTimeout: time.Second,
	}
}

func testWorkerConfig() spool.WorkerConfig {
	return spool.WorkerConfig{
		SubmitTimeout:    time.Second,
		FinalizeTimeout:  spool.DefaultFinalizeTimeout,
		PollInterval:     spool.DefaultPollInterval,
		RetryAttempts:    3,
		RetryBaseDelay:   time.Second,
		SubmittedTimeout: spool.DefaultSubmittedTimeout,
		ProviderName:     "noop",
	}
}

func scrapeMetricsText(t *testing.T, metrics *observability.Metrics) string {
	t.Helper()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

func assertSMTPError(t *testing.T, err error, wantCode int, wantEnhanced gosmtp.EnhancedCode) {
	t.Helper()

	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) {
		t.Fatalf("expected *gosmtp.SMTPError, got %T: %v", err, err)
	}
	if smtpErr.Code != wantCode {
		t.Fatalf("unexpected SMTP code: got %d want %d", smtpErr.Code, wantCode)
	}
	if smtpErr.EnhancedCode != wantEnhanced {
		t.Fatalf("unexpected enhanced code: got %v want %v", smtpErr.EnhancedCode, wantEnhanced)
	}
}

type stubRelayStore struct {
	mu          sync.Mutex
	enqueueFunc func(context.Context, email.Message) (spool.Record, error)
}

func (s *stubRelayStore) Enqueue(ctx context.Context, msg email.Message) (spool.Record, error) {
	s.mu.Lock()
	enqueueFunc := s.enqueueFunc
	s.mu.Unlock()
	if enqueueFunc != nil {
		return enqueueFunc(ctx, msg)
	}
	return spool.Record{ID: "test-record", Message: msg}, nil
}

func (*stubRelayStore) ClaimReady(context.Context, time.Time) (spool.Record, bool, error) {
	panic("unexpected ClaimReady call")
}

func (*stubRelayStore) NextSubmittedReady(context.Context, time.Time) (spool.Record, bool, error) {
	panic("unexpected NextSubmittedReady call")
}

func (*stubRelayStore) MarkSubmitted(context.Context, spool.Record, email.SubmissionResult, time.Time) (spool.Record, error) {
	panic("unexpected MarkSubmitted call")
}

func (*stubRelayStore) MarkRetry(context.Context, spool.Record, time.Time, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkRetry call")
}

func (*stubRelayStore) MarkSucceeded(context.Context, spool.Record) (spool.Record, error) {
	panic("unexpected MarkSucceeded call")
}

func (*stubRelayStore) MarkDeadLetter(context.Context, spool.Record, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkDeadLetter call")
}

func (*stubRelayStore) Recover(context.Context, time.Time) (spool.RecoveryResult, error) {
	panic("unexpected Recover call")
}

type stubDeliveryProvider struct{}

func (stubDeliveryProvider) Submit(context.Context, email.Message, string) (email.SubmissionResult, error) {
	return email.SubmissionResult{State: email.SubmissionStateSucceeded}, nil
}

func (stubDeliveryProvider) Poll(context.Context, string) (email.SubmissionStatus, error) {
	return email.SubmissionStatus{State: email.SubmissionStateSucceeded}, nil
}

type stubRecoverer struct {
	err error
}

func (s stubRecoverer) Recover(context.Context, time.Time) (spool.RecoveryResult, error) {
	return spool.RecoveryResult{}, s.err
}

func freeSMTPTCPAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("tcp listen unavailable in this test environment: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func startSMTPServerForMainTest(t *testing.T, srv *smtprelay.Server) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("server failed before ready: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("timed out waiting for server readiness")
	}

	return func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("server shutdown error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for server shutdown")
		}
	}
}

func dialSMTPConn(t *testing.T, addr string) *textproto.Conn {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}

	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadResponse(220); err != nil {
		tp.Close()
		t.Fatalf("read greeting: %v", err)
	}

	tp.PrintfLine("EHLO localhost")
	if _, _, err := tp.ReadResponse(250); err != nil {
		tp.Close()
		t.Fatalf("ehlo response: %v", err)
	}

	return tp
}

func sendSMTPMessage(t *testing.T, tp *textproto.Conn, from, to, subject, body string) {
	t.Helper()

	tp.PrintfLine("MAIL FROM:<%s>", from)
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("mail response: %v", err)
	}
	tp.PrintfLine("RCPT TO:<%s>", to)
	if _, _, err := tp.ReadResponse(250); err != nil {
		t.Fatalf("rcpt response: %v", err)
	}
	tp.PrintfLine("DATA")
	if _, _, err := tp.ReadResponse(354); err != nil {
		t.Fatalf("data response: %v", err)
	}

	tp.PrintfLine("Subject: %s", subject)
	tp.PrintfLine("From: %s", from)
	tp.PrintfLine("To: %s", to)
	tp.PrintfLine("")
	tp.PrintfLine("%s", body)
	tp.PrintfLine(".")
}

func waitForHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to return %d", url, want)
}
