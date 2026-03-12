package email

import "time"

// SubmissionState describes the provider-independent lifecycle state of a
// submitted outbound delivery operation.
type SubmissionState string

const (
	// SubmissionStateRunning indicates the provider accepted the submit request
	// and the operation is still in progress.
	SubmissionStateRunning SubmissionState = "running"
	// SubmissionStateSucceeded indicates the provider reached a terminal
	// successful state.
	SubmissionStateSucceeded SubmissionState = "succeeded"
	// SubmissionStateFailed indicates the provider reached a terminal failure
	// state.
	SubmissionStateFailed SubmissionState = "failed"
	// SubmissionStateCanceled indicates the provider reached a terminal canceled
	// state.
	SubmissionStateCanceled SubmissionState = "canceled"
)

// SubmissionFailure carries provider-independent terminal failure metadata.
type SubmissionFailure struct {
	Message    string
	Temporary  bool
	StatusCode int
}

// SubmissionResult captures the initial provider response after submission.
type SubmissionResult struct {
	OperationID       string
	OperationLocation string
	RetryAfter        time.Duration
	State             SubmissionState
	ProviderMessageID string
	Failure           *SubmissionFailure
}

// SubmissionStatus captures the provider-independent status of a previously
// submitted outbound operation.
type SubmissionStatus struct {
	OperationID       string
	RetryAfter        time.Duration
	State             SubmissionState
	ProviderMessageID string
	Failure           *SubmissionFailure
}
