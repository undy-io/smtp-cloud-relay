package email

import "context"

// Provider sends email messages to an outbound service.
type Provider interface {
	Send(ctx context.Context, msg Message) error
}
