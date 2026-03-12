package noop

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

func TestSubmitReturnsImmediateSuccess(t *testing.T) {
	t.Parallel()

	p := NewProvider(testLogger())
	result, err := p.Submit(context.Background(), email.Message{To: []string{"to@example.com"}}, "op-123")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if result.State != email.SubmissionStateSucceeded {
		t.Fatalf("unexpected state: %q", result.State)
	}
	if result.OperationID != "op-123" {
		t.Fatalf("unexpected operation id: %q", result.OperationID)
	}
	if result.ProviderMessageID != "" {
		t.Fatalf("unexpected provider message id: %q", result.ProviderMessageID)
	}
}

func TestPollReturnsSucceeded(t *testing.T) {
	t.Parallel()

	p := NewProvider(testLogger())
	status, err := p.Poll(context.Background(), "op-123")
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if status.State != email.SubmissionStateSucceeded {
		t.Fatalf("unexpected state: %q", status.State)
	}
	if status.OperationID != "op-123" {
		t.Fatalf("unexpected operation id: %q", status.OperationID)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
