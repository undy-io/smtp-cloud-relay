package noop

import (
	"context"
	"log/slog"
	"strings"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

type Provider struct {
	logger *slog.Logger
}

var _ email.Provider = (*Provider)(nil)

func NewProvider(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{logger: logger}
}

func (p *Provider) Send(ctx context.Context, msg email.Message) error {
	_, err := p.Submit(ctx, msg, "")
	return err
}

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

func (p *Provider) Poll(_ context.Context, operationID string) (email.SubmissionStatus, error) {
	return email.SubmissionStatus{
		OperationID: strings.TrimSpace(operationID),
		State:       email.SubmissionStateSucceeded,
	}, nil
}
