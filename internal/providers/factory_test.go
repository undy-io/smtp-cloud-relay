package providers

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/smtp-cloud-relay/internal/config"
	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"github.com/undy-io/smtp-cloud-relay/internal/providers/acs"
)

func TestBuildNoop(t *testing.T) {
	rt, err := Build(config.Config{
		DeliveryMode: "noop",
	}, testLogger())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if rt.Provider == nil {
		t.Fatal("expected provider")
	}
	if rt.SendTimeout != 0 {
		t.Fatalf("unexpected send timeout: %s", rt.SendTimeout)
	}
	if rt.HandlerTimeout != 0 {
		t.Fatalf("unexpected handler timeout: %s", rt.HandlerTimeout)
	}
	if err := rt.Provider.Send(context.Background(), email.Message{To: []string{"to@example.com"}}); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
}

func TestBuildACS(t *testing.T) {
	accessKey := base64.StdEncoding.EncodeToString([]byte("test-access-key"))
	rt, err := Build(config.Config{
		DeliveryMode:                    "acs",
		ACSConnectionString:             "endpoint=https://example.communication.azure.us;accesskey=" + accessKey,
		ACSSender:                       "no-reply@example.com",
		DeliveryRetryAttempts:           3,
		DeliveryRetryBaseDelayMS:        1000,
		DeliveryHTTPTimeoutMS:           30000,
		DeliveryHTTPMaxIdleConns:        200,
		DeliveryHTTPMaxIdleConnsPerHost: 50,
		DeliveryHTTPIdleConnTimeoutMS:   90000,
	}, testLogger())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if rt.Provider == nil {
		t.Fatal("expected provider")
	}
	if _, ok := rt.Provider.(*acs.Provider); !ok {
		t.Fatalf("unexpected provider type: %T", rt.Provider)
	}
	if rt.SendTimeout <= 0 {
		t.Fatalf("expected send timeout > 0, got %s", rt.SendTimeout)
	}
	if rt.HandlerTimeout <= rt.SendTimeout {
		t.Fatalf("expected handler timeout > send timeout, got send=%s handler=%s", rt.SendTimeout, rt.HandlerTimeout)
	}
}

func TestBuildSESNotImplemented(t *testing.T) {
	_, err := Build(config.Config{
		DeliveryMode: "ses",
	}, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildUnknownMode(t *testing.T) {
	_, err := Build(config.Config{
		DeliveryMode: "unknown",
	}, testLogger())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported delivery mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRetryBackoffTotal(t *testing.T) {
	got := retryBackoffTotal(time.Second, 3)
	if got != 3*time.Second {
		t.Fatalf("unexpected total backoff: %s", got)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
