package noop

import (
	"context"
	"log/slog"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

type Provider struct {
	logger *slog.Logger
}

func NewProvider(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{logger: logger}
}

func (p *Provider) Send(_ context.Context, msg email.Message) error {
	p.logger.Info("noop delivery accepted",
		"from", msg.From,
		"to", msg.To,
		"subject", msg.Subject,
		"attachments", len(msg.Attachments),
	)
	return nil
}
