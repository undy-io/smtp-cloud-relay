package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	outcomeRunning        = "running"
	outcomeSucceeded      = "succeeded"
	outcomeFailed         = "failed"
	outcomeCanceled       = "canceled"
	outcomeTemporaryError = "temporary_error"
	outcomePermanentError = "permanent_error"

	retryStageSubmit = "submit"
	retryStagePoll   = "poll"
)

var spoolStates = []string{"queued", "working", "submitted", "succeeded", "dead-letter"}

// StateCountsFunc snapshots current spool record counts by state.
type StateCountsFunc func(context.Context) (map[string]int, error)

// Metrics owns the Prometheus registry and low-cardinality relay metrics.
type Metrics struct {
	registry *prometheus.Registry
	handler  http.Handler

	spoolStateCounts StateCountsFunc

	sessionsDenied     prometheus.Counter
	authFailures       prometheus.Counter
	enqueued           prometheus.Counter
	enqueueFailures    prometheus.Counter
	spoolRecords       *prometheus.GaugeVec
	deliverySubmits    *prometheus.CounterVec
	deliveryPolls      *prometheus.CounterVec
	deliveryRetries    *prometheus.CounterVec
}

// NewMetrics constructs the relay metrics registry and HTTP handler.
func NewMetrics(spoolStateCounts StateCountsFunc) *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry:         registry,
		spoolStateCounts: spoolStateCounts,
		sessionsDenied: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "smtp_relay_sessions_denied_total",
			Help: "Total denied SMTP sessions.",
		}),
		authFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "smtp_relay_auth_failures_total",
			Help: "Total failed SMTP AUTH attempts.",
		}),
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "smtp_relay_enqueued_total",
			Help: "Total messages durably enqueued into the spool.",
		}),
		enqueueFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "smtp_relay_enqueue_failures_total",
			Help: "Total durable enqueue failures caused by spool/store errors.",
		}),
		spoolRecords: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "smtp_relay_spool_records",
			Help: "Current durable spool record counts by state.",
		}, []string{"state"}),
		deliverySubmits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smtp_relay_delivery_submissions_total",
			Help: "Total provider submit attempts and outcomes.",
		}, []string{"provider", "outcome"}),
		deliveryPolls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smtp_relay_delivery_polls_total",
			Help: "Total provider poll attempts and outcomes.",
		}, []string{"provider", "outcome"}),
		deliveryRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "smtp_relay_delivery_retries_total",
			Help: "Total delivery retries and submitted-record reschedules.",
		}, []string{"stage"}),
	}
	registry.MustRegister(
		m.sessionsDenied,
		m.authFailures,
		m.enqueued,
		m.enqueueFailures,
		m.spoolRecords,
		m.deliverySubmits,
		m.deliveryPolls,
		m.deliveryRetries,
	)
	m.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.refreshSpoolStateCounts(r.Context())
		promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
	})
	return m
}

// Handler returns the Prometheus scrape handler for the registry.
func (m *Metrics) Handler() http.Handler {
	if m == nil || m.handler == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		})
	}
	return m.handler
}

// IncSessionsDenied records a denied SMTP session.
func (m *Metrics) IncSessionsDenied() {
	if m == nil {
		return
	}
	m.sessionsDenied.Inc()
}

// IncAuthFailures records a failed SMTP AUTH attempt.
func (m *Metrics) IncAuthFailures() {
	if m == nil {
		return
	}
	m.authFailures.Inc()
}

// IncEnqueued records a successful durable enqueue.
func (m *Metrics) IncEnqueued() {
	if m == nil {
		return
	}
	m.enqueued.Inc()
}

// IncEnqueueFailures records a spool/store enqueue failure.
func (m *Metrics) IncEnqueueFailures() {
	if m == nil {
		return
	}
	m.enqueueFailures.Inc()
}

// ObserveSubmission records the outcome of one provider submit call.
func (m *Metrics) ObserveSubmission(provider, outcome string) {
	if m == nil {
		return
	}
	m.deliverySubmits.WithLabelValues(normalizeProvider(provider), normalizeOutcome(outcome)).Inc()
}

// ObservePoll records the outcome of one provider poll call.
func (m *Metrics) ObservePoll(provider, outcome string) {
	if m == nil {
		return
	}
	m.deliveryPolls.WithLabelValues(normalizeProvider(provider), normalizeOutcome(outcome)).Inc()
}

// IncRetry records a submit retry or submitted-record reschedule.
func (m *Metrics) IncRetry(stage string) {
	if m == nil {
		return
	}
	m.deliveryRetries.WithLabelValues(normalizeRetryStage(stage)).Inc()
}

func (m *Metrics) refreshSpoolStateCounts(ctx context.Context) {
	if m == nil || m.spoolStateCounts == nil {
		return
	}
	counts, err := m.spoolStateCounts(ctx)
	if err != nil {
		return
	}
	for _, state := range spoolStates {
		m.spoolRecords.WithLabelValues(state).Set(0)
	}
	for state, count := range counts {
		m.spoolRecords.WithLabelValues(state).Set(float64(count))
	}
}

func normalizeProvider(provider string) string {
	if provider == "" {
		return "unknown"
	}
	return provider
}

func normalizeOutcome(outcome string) string {
	switch outcome {
	case outcomeRunning, outcomeSucceeded, outcomeFailed, outcomeCanceled, outcomeTemporaryError, outcomePermanentError:
		return outcome
	default:
		return outcomePermanentError
	}
}

func normalizeRetryStage(stage string) string {
	switch stage {
	case retryStageSubmit, retryStagePoll:
		return stage
	default:
		return retryStageSubmit
	}
}
