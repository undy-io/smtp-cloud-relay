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
		From:    "from@example.com",
		To:      []string{"to@example.com"},
		Subject: "subject",
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
			}, testMainLogger(), func(context.Context, email.Message) error {
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
	}, testMainLogger(), func(context.Context, email.Message) error {
		close(sendStarted)
		<-blockSend
		return nil
	}, time.Second)

	firstErrCh := make(chan error, 1)
	go func() {
		firstErrCh <- handler.HandleMessage(context.Background(), email.Message{
			From: "from@example.com",
			To:   []string{"to@example.com"},
		})
	}()

	select {
	case <-sendStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first send to start")
	}

	err := handler.HandleMessage(context.Background(), email.Message{
		From: "from@example.com",
		To:   []string{"to@example.com"},
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

func testMainLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
