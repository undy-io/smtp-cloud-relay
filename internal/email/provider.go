package email

import "context"

// Provider sends email messages to an outbound service.
type Provider interface {
	// Send is a temporary synchronous compatibility entrypoint used by the
	// still-live direct-send SMTP path. It remains until SPOOL-003A removes the
	// compatibility layer.
	Send(ctx context.Context, msg Message) error
	// Submit starts an outbound provider operation for the message.
	Submit(ctx context.Context, msg Message, operationID string) (SubmissionResult, error)
	// Poll reports the current state of a previously submitted provider
	// operation.
	Poll(ctx context.Context, operationID string) (SubmissionStatus, error)
}
