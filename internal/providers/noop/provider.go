package noop

import (
	"context"
	"log/slog"
	"strings"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

// Provider implements a no-op async provider for local testing.
type Provider struct {
	logger *slog.Logger
}

var _ email.Provider = (*Provider)(nil)

// NewProvider constructs the no-op provider.
func NewProvider(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{logger: logger}
}

// Submit records a successful no-op delivery outcome immediately.
func (p *Provider) Submit(_ context.Context, msg email.Message, operationID string) (email.SubmissionResult, error) {
	p.logger.Info("noop delivery accepted",
		"envelope_from", msg.EnvelopeFrom,
		"header_from", msg.HeaderFrom,
		"to", msg.To,
		"subject", msg.Subject,
		"attachments", len(msg.Attachments),
	)
	return email.SubmissionResult{
		OperationID: strings.TrimSpace(operationID),
		State:       email.SubmissionStateSucceeded,
	}, nil
}

// Poll reports immediate success because noop has no long-running operations.
func (p *Provider) Poll(_ context.Context, operationID string) (email.SubmissionStatus, error) {
	return email.SubmissionStatus{
		OperationID: strings.TrimSpace(operationID),
		State:       email.SubmissionStateSucceeded,
	}, nil
}
