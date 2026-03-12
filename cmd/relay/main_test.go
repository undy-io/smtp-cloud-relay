package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
)

type stubDeliveryError struct {
	temporary  bool
	statusCode int
}

func (e stubDeliveryError) Error() string        { return "delivery failure" }
func (e stubDeliveryError) ProviderName() string { return "stub" }
func (e stubDeliveryError) Temporary() bool      { return e.temporary }
func (e stubDeliveryError) HTTPStatusCode() int  { return e.statusCode }

func TestNewDirectSendMessageHandlerMapsDeliveryErrors(t *testing.T) {
	t.Parallel()

	msg := email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		To:           []string{"to@example.com"},
		Subject:      "subject",
	}

	tests := []struct {
		name         string
		sendErr      error
		wantCode     int
		wantEnhanced gosmtp.EnhancedCode
	}{
		{
			name:         "temporary delivery error",
			sendErr:      stubDeliveryError{temporary: true, statusCode: 503},
			wantCode:     451,
			wantEnhanced: gosmtp.EnhancedCode{4, 3, 0},
		},
		{
			name:         "permanent delivery error",
			sendErr:      stubDeliveryError{temporary: false, statusCode: 400},
			wantCode:     554,
			wantEnhanced: gosmtp.EnhancedCode{5, 0, 0},
		},
		{
			name:         "unknown internal error",
			sendErr:      errors.New("boom"),
			wantCode:     451,
			wantEnhanced: gosmtp.EnhancedCode{4, 3, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := newDirectSendMessageHandler(config.Config{
				DeliveryMode:         "test",
				SMTPMaxInflightSends: 1,
				SenderPolicyMode:     "rewrite",
			}, testMainLogger(), mustTestSenderPolicy(t, email.SenderPolicyOptions{
				Mode: email.SenderPolicyRewrite,
			}), func(context.Context, email.Message) error {
				return tt.sendErr
			}, time.Second)

			err := handler.HandleMessage(context.Background(), msg)
			var smtpErr *gosmtp.SMTPError
			if !errors.As(err, &smtpErr) {
				t.Fatalf("expected *gosmtp.SMTPError, got %T", err)
			}
			if smtpErr.Code != tt.wantCode {
				t.Fatalf("unexpected SMTP code: got %d want %d", smtpErr.Code, tt.wantCode)
			}
			if smtpErr.EnhancedCode != tt.wantEnhanced {
				t.Fatalf("unexpected enhanced code: got %v want %v", smtpErr.EnhancedCode, tt.wantEnhanced)
			}
		})
	}
}

func TestNewDirectSendMessageHandlerInflightSaturation(t *testing.T) {
	t.Parallel()

	blockSend := make(chan struct{})
	sendStarted := make(chan struct{})

	handler := newDirectSendMessageHandler(config.Config{
		DeliveryMode:         "test",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), mustTestSenderPolicy(t, email.SenderPolicyOptions{
		Mode: email.SenderPolicyRewrite,
	}), func(context.Context, email.Message) error {
		close(sendStarted)
		<-blockSend
		return nil
	}, time.Second)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- handler.HandleMessage(context.Background(), email.Message{
			EnvelopeFrom: "from@example.com",
			To:           []string{"to@example.com"},
		})
	}()

	select {
	case <-sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first send to start")
	}

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "from@example.com",
		To:           []string{"to@example.com"},
	})
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) {
		t.Fatalf("expected *gosmtp.SMTPError, got %T", err)
	}
	if smtpErr.Code != 451 {
		t.Fatalf("unexpected SMTP code: got %d want 451", smtpErr.Code)
	}
	if smtpErr.EnhancedCode != (gosmtp.EnhancedCode{4, 3, 2}) {
		t.Fatalf("unexpected enhanced code: got %v want %v", smtpErr.EnhancedCode, gosmtp.EnhancedCode{4, 3, 2})
	}

	close(blockSend)

	select {
	case err := <-firstErrCh:
		if err != nil {
			t.Fatalf("unexpected first handler error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first handler to finish")
	}
}

func TestNewDirectSendMessageHandlerStrictPolicyRejectsBeforeSend(t *testing.T) {
	t.Parallel()

	sendCalled := false
	handler := newDirectSendMessageHandler(config.Config{
		DeliveryMode:         "test",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "strict",
	}, testMainLogger(), mustTestSenderPolicy(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyStrict,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}), func(context.Context, email.Message) error {
		sendCalled = true
		return nil
	}, time.Second)

	err := handler.HandleMessage(context.Background(), email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "sender@blocked.example.com",
		To:           []string{"to@example.com"},
	})
	var smtpErr *gosmtp.SMTPError
	if !errors.As(err, &smtpErr) {
		t.Fatalf("expected *gosmtp.SMTPError, got %T", err)
	}
	if smtpErr.Code != 554 {
		t.Fatalf("unexpected SMTP code: got %d want 554", smtpErr.Code)
	}
	if smtpErr.EnhancedCode != (gosmtp.EnhancedCode{5, 7, 1}) {
		t.Fatalf("unexpected enhanced code: got %v want %v", smtpErr.EnhancedCode, gosmtp.EnhancedCode{5, 7, 1})
	}
	if sendCalled {
		t.Fatal("expected sendFunc not to be called")
	}
}

func TestNewDirectSendMessageHandlerRewritePassesEffectiveReplyToOnly(t *testing.T) {
	t.Parallel()

	var sent email.Message
	handler := newDirectSendMessageHandler(config.Config{
		DeliveryMode:         "test",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), mustTestSenderPolicy(t, email.SenderPolicyOptions{
		Mode:                  email.SenderPolicyRewrite,
		AllowedDomainPatterns: []string{"allowed.example.com"},
	}), func(_ context.Context, msg email.Message) error {
		sent = msg
		return nil
	}, time.Second)

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
		t.Fatalf("unexpected handler error: %v", err)
	}
	if len(sent.ReplyTo) != 1 || sent.ReplyTo[0] != "reply@allowed.example.com" {
		t.Fatalf("unexpected sent reply-to: %#v", sent.ReplyTo)
	}
	if sent.HeaderFrom != "header@example.com" {
		t.Fatalf("unexpected sent header from: %q", sent.HeaderFrom)
	}
}

func TestBuildMessageHandlerRejectsInvalidSenderPolicyRegex(t *testing.T) {
	t.Parallel()

	handlerCfg := config.Config{
		DeliveryMode:         "noop",
		SMTPMaxInflightSends: 1,
		SenderPolicyMode:     "rewrite",
		SenderAllowedDomains: []string{"re:("},
	}

	_, _, err := buildMessageHandler(handlerCfg, testMainLogger())
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

	_, _, err := buildMessageHandler(handlerCfg, testMainLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBuildRelayHandlerConstructsHandlerWithoutProviders(t *testing.T) {
	t.Parallel()

	handler, err := buildRelayHandler(config.Config{
		DeliveryMode:         "does-not-matter-here",
		SMTPMaxInflightSends: 2,
		SenderPolicyMode:     "rewrite",
	}, testMainLogger(), stubRelayStore{})
	if err != nil {
		t.Fatalf("buildRelayHandler() error: %v", err)
	}
	if handler == nil {
		t.Fatal("expected handler, got nil")
	}
}

func testMainLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustTestSenderPolicy(t *testing.T, opts email.SenderPolicyOptions) email.SenderPolicy {
	t.Helper()

	policy, err := email.NewSenderPolicy(opts)
	if err != nil {
		t.Fatalf("NewSenderPolicy() error: %v", err)
	}
	return policy
}

type stubRelayStore struct{}

func (stubRelayStore) Enqueue(context.Context, email.Message) (spool.Record, error) {
	return spool.Record{ID: "test-record"}, nil
}

func (stubRelayStore) ClaimReady(context.Context, time.Time) (spool.Record, bool, error) {
	panic("unexpected ClaimReady call")
}

func (stubRelayStore) MarkSubmitted(context.Context, spool.Record, string, string, time.Time) (spool.Record, error) {
	panic("unexpected MarkSubmitted call")
}

func (stubRelayStore) MarkRetry(context.Context, spool.Record, time.Time, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkRetry call")
}

func (stubRelayStore) MarkSucceeded(context.Context, spool.Record) (spool.Record, error) {
	panic("unexpected MarkSucceeded call")
}

func (stubRelayStore) MarkDeadLetter(context.Context, spool.Record, *spool.LastError) (spool.Record, error) {
	panic("unexpected MarkDeadLetter call")
}

func (stubRelayStore) Recover(context.Context, time.Time) (spool.RecoveryResult, error) {
	panic("unexpected Recover call")
}
