package email

import "context"

// Provider manages outbound provider operations for the durable spool worker.
type Provider interface {
	// Submit starts an outbound provider operation for the message.
	Submit(ctx context.Context, msg Message, operationID string) (SubmissionResult, error)
	// Poll reports the current state of a previously submitted provider
	// operation.
	Poll(ctx context.Context, operationID string) (SubmissionStatus, error)
}
