package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/spool"
)

// Handler durably enqueues policy-normalized messages into the spool.
type Handler struct {
	logger       *slog.Logger
	senderPolicy email.SenderPolicy
	store        spool.Store
	inflight     chan struct{}
}

// BusyError reports that the relay cannot accept more in-flight enqueue work.
type BusyError struct {
	Limit int
}

func (e *BusyError) Error() string {
	if e == nil {
		return "relay busy"
	}
	if e.Limit <= 0 {
		return "relay busy, try again later"
	}
	return fmt.Sprintf("relay busy, try again later (max inflight: %d)", e.Limit)
}

// AsBusyError unwraps err into a BusyError when inflight saturation caused the failure.
func AsBusyError(err error) (*BusyError, bool) {
	var target *BusyError
	if !errors.As(err, &target) {
		return nil, false
	}
	return target, true
}

// NewHandler constructs the relay-owned durable enqueue service.
func NewHandler(logger *slog.Logger, senderPolicy email.SenderPolicy, store spool.Store, maxInflight int) (*Handler, error) {
	if store == nil {
		return nil, fmt.Errorf("spool store is required")
	}
	if maxInflight <= 0 {
		return nil, fmt.Errorf("max inflight must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		logger:       logger,
		senderPolicy: senderPolicy,
		store:        store,
		inflight:     make(chan struct{}, maxInflight),
	}, nil
}

// HandleMessage applies sender policy and durably enqueues the message.
func (h *Handler) HandleMessage(ctx context.Context, msg email.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	policyResult, err := email.ApplySenderPolicy(msg, h.senderPolicy)
	if err != nil {
		if policyErr, ok := email.AsSenderPolicyError(err); ok {
			h.logger.Warn("smtp sender rejected by policy",
				"sender_policy_reason", policyErr.Reason,
				"envelope_from", msg.EnvelopeFrom,
				"header_from", msg.HeaderFrom,
				"original_sender", policyResult.OriginalSender,
				"effective_reply_to_count", len(policyResult.EffectiveReplyTo),
			)
			return err
		}

		h.logger.Error("failed to apply sender policy",
			"envelope_from", msg.EnvelopeFrom,
			"header_from", msg.HeaderFrom,
			"error", err,
		)
		return err
	}

	if policyResult.DecisionReason != "" {
		h.logger.Info("smtp sender policy dropped original sender intent",
			"sender_policy_reason", policyResult.DecisionReason,
			"envelope_from", msg.EnvelopeFrom,
			"header_from", msg.HeaderFrom,
			"original_sender", policyResult.OriginalSender,
			"effective_reply_to_count", len(policyResult.EffectiveReplyTo),
		)
	}

	msg = policyResult.Message

	select {
	case h.inflight <- struct{}{}:
		defer func() { <-h.inflight }()
	default:
		h.logger.Warn("smtp enqueue rejected due to inflight saturation", "max_inflight", cap(h.inflight))
		return &BusyError{Limit: cap(h.inflight)}
	}

	if _, err := h.store.Enqueue(ctx, msg); err != nil {
		return err
	}
	return nil
}
