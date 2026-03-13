package observability

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerEmitsPrometheusText(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(nil)
	body := scrapeMetrics(t, metrics)
	if !strings.Contains(body, "# HELP smtp_relay_enqueued_total") {
		t.Fatalf("expected Prometheus help text, got:\n%s", body)
	}
	if !strings.Contains(body, "smtp_relay_enqueued_total 0") {
		t.Fatalf("expected initialized counter output, got:\n%s", body)
	}
}

func TestMetricsSpoolStateCountsRefreshAndZeroMissingStates(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(func(context.Context) (map[string]int, error) {
		return map[string]int{
			"queued":    2,
			"submitted": 1,
		}, nil
	})

	body := scrapeMetrics(t, metrics)
	if !strings.Contains(body, `smtp_relay_spool_records{state="queued"} 2`) {
		t.Fatalf("expected queued state count, got:\n%s", body)
	}
	if !strings.Contains(body, `smtp_relay_spool_records{state="submitted"} 1`) {
		t.Fatalf("expected submitted state count, got:\n%s", body)
	}
	if !strings.Contains(body, `smtp_relay_spool_records{state="working"} 0`) {
		t.Fatalf("expected missing states to be zeroed, got:\n%s", body)
	}
}

func TestMetricsSubmissionPollAndRetryLabels(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(nil)
	metrics.ObserveSubmission("acs", "running")
	metrics.ObserveSubmission("ses", "temporary_error")
	metrics.ObservePoll("acs", "succeeded")
	metrics.ObservePoll("ses", "permanent_error")
	metrics.IncRetry("submit")
	metrics.IncRetry("poll")

	body := scrapeMetrics(t, metrics)
	for _, want := range []string{
		`smtp_relay_delivery_submissions_total{outcome="running",provider="acs"} 1`,
		`smtp_relay_delivery_submissions_total{outcome="temporary_error",provider="ses"} 1`,
		`smtp_relay_delivery_polls_total{outcome="succeeded",provider="acs"} 1`,
		`smtp_relay_delivery_polls_total{outcome="permanent_error",provider="ses"} 1`,
		`smtp_relay_delivery_retries_total{stage="submit"} 1`,
		`smtp_relay_delivery_retries_total{stage="poll"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metric line %q, got:\n%s", want, body)
		}
	}
}

func TestMetricsSpoolStateCountErrorsDoNotBreakScrape(t *testing.T) {
	t.Parallel()

	metrics := NewMetrics(func(context.Context) (map[string]int, error) {
		return nil, errors.New("db unavailable")
	})
	body := scrapeMetrics(t, metrics)
	if !strings.Contains(body, "smtp_relay_enqueued_total 0") {
		t.Fatalf("expected scrape to continue on state count error, got:\n%s", body)
	}
}

func scrapeMetrics(t *testing.T, metrics *Metrics) string {
	t.Helper()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}
