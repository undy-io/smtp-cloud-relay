package spool

import (
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
)

// State identifies the spool subdirectory that currently owns a record.
type State string

const (
	StateQueued     State = "queued"
	StateWorking    State = "working"
	StateSubmitted  State = "submitted"
	StateSucceeded  State = "succeeded"
	StateDeadLetter State = "dead-letter"
)

var allStates = []State{
	StateQueued,
	StateWorking,
	StateSubmitted,
	StateSucceeded,
	StateDeadLetter,
}

// LastError captures the latest provider failure persisted with a record.
type LastError struct {
	Message   string    `json:"message"`
	Provider  string    `json:"provider,omitempty"`
	Temporary bool      `json:"temporary"`
	Timestamp time.Time `json:"timestamp"`
}

// Record is the durable spool entry persisted to disk.
type Record struct {
	ID                string        `json:"id"`
	Message           email.Message `json:"message"`
	State             State         `json:"state"`
	Attempt           int           `json:"attempt"`
	NextAttemptAt     time.Time     `json:"nextAttemptAt"`
	OperationID       string        `json:"operationId,omitempty"`
	OperationLocation string        `json:"operationLocation,omitempty"`
	ProviderMessageID string        `json:"providerMessageId,omitempty"`
	FirstSubmittedAt  time.Time     `json:"firstSubmittedAt,omitempty"`
	LastError         *LastError    `json:"lastError,omitempty"`
	CreatedAt         time.Time     `json:"createdAt"`
	UpdatedAt         time.Time     `json:"updatedAt"`
}

// RecoveryResult summarizes the records surfaced during startup recovery.
type RecoveryResult struct {
	Requeued         []Record `json:"requeued,omitempty"`
	Submitted        []Record `json:"submitted,omitempty"`
	DeadLettered     []Record `json:"deadLettered,omitempty"`
	OrphanedPayloads []string `json:"orphanedPayloads,omitempty"`
}
