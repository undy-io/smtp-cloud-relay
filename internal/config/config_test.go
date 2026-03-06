package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWithRequiredSecurityEnv(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.SMTPListenAddr != "0.0.0.0:2525" {
		t.Fatalf("unexpected SMTPListenAddr: %q", cfg.SMTPListenAddr)
	}
	if cfg.HTTPListenAddr != "0.0.0.0:8080" {
		t.Fatalf("unexpected HTTPListenAddr: %q", cfg.HTTPListenAddr)
	}
	if cfg.DeliveryMode != "acs" {
		t.Fatalf("unexpected DeliveryMode: %q", cfg.DeliveryMode)
	}
	if !cfg.SMTPRequireAuth {
		t.Fatal("expected SMTPRequireAuth=true")
	}
	if cfg.SMTPAuthProvider != "static" {
		t.Fatalf("unexpected SMTPAuthProvider: %q", cfg.SMTPAuthProvider)
	}
	if !cfg.SMTPStartTLSEnabled {
		t.Fatal("expected SMTPStartTLSEnabled=true")
	}
	if cfg.SMTPRequireTLS {
		t.Fatal("expected SMTPRequireTLS=false")
	}
	if cfg.SMTPMaxInflightSends != 200 {
		t.Fatalf("unexpected SMTPMaxInflightSends: %d", cfg.SMTPMaxInflightSends)
	}
	if cfg.ACSRetryAttempts != 3 {
		t.Fatalf("unexpected ACSRetryAttempts: %d", cfg.ACSRetryAttempts)
	}
	if cfg.ACSRetryBaseDelayMS != 1000 {
		t.Fatalf("unexpected ACSRetryBaseDelayMS: %d", cfg.ACSRetryBaseDelayMS)
	}
	if cfg.ACSHTTPTimeoutMS != 30000 {
		t.Fatalf("unexpected ACSHTTPTimeoutMS: %d", cfg.ACSHTTPTimeoutMS)
	}
	if cfg.ACSHTTPMaxIdleConns != 200 {
		t.Fatalf("unexpected ACSHTTPMaxIdleConns: %d", cfg.ACSHTTPMaxIdleConns)
	}
	if cfg.ACSHTTPMaxIdleConnsPerHost != 50 {
		t.Fatalf("unexpected ACSHTTPMaxIdleConnsPerHost: %d", cfg.ACSHTTPMaxIdleConnsPerHost)
	}
	if cfg.ACSHTTPIdleConnTimeoutMS != 90000 {
		t.Fatalf("unexpected ACSHTTPIdleConnTimeoutMS: %d", cfg.ACSHTTPIdleConnTimeoutMS)
	}
	if cfg.ACSTLSCAFile != "" {
		t.Fatalf("unexpected ACSTLSCAFile default: %q", cfg.ACSTLSCAFile)
	}
	if cfg.ACSTLSCAPEM != "" {
		t.Fatalf("unexpected ACSTLSCAPEM default: %q", cfg.ACSTLSCAPEM)
	}
}

func TestLoadNoopModeWithoutACSConfig(t *testing.T) {
	setSecureDefaults(t)
	t.Setenv("DELIVERY_MODE", "noop")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DeliveryMode != "noop" {
		t.Fatalf("unexpected DeliveryMode: %q", cfg.DeliveryMode)
	}
}

func TestLoadConnectionStringFromFile(t *testing.T) {
	setSecureDefaults(t)
	t.Setenv("ACS_SENDER", "no-reply@example.com")

	dir := t.TempDir()
	path := filepath.Join(dir, "conn.txt")
	content := "endpoint=https://example.communication.azure.us;accesskey=Zm9v\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	t.Setenv("ACS_CONNECTION_STRING_FILE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ACSConnectionString != strings.TrimSpace(content) {
		t.Fatalf("unexpected ACSConnectionString: %q", cfg.ACSConnectionString)
	}
}

func TestLoadRetryAndHTTPConfigFromEnv(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)
	t.Setenv("ACS_RETRY_ATTEMPTS", "5")
	t.Setenv("ACS_RETRY_BASE_DELAY_MS", "250")
	t.Setenv("ACS_HTTP_TIMEOUT_MS", "15000")
	t.Setenv("ACS_HTTP_MAX_IDLE_CONNS", "300")
	t.Setenv("ACS_HTTP_MAX_IDLE_CONNS_PER_HOST", "60")
	t.Setenv("ACS_HTTP_IDLE_CONN_TIMEOUT_MS", "45000")
	t.Setenv("ACS_TLS_CA_FILE", "/mnt/secrets/proxy-ca.pem")
	t.Setenv("ACS_TLS_CA_PEM", "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ACSRetryAttempts != 5 {
		t.Fatalf("unexpected ACSRetryAttempts: %d", cfg.ACSRetryAttempts)
	}
	if cfg.ACSRetryBaseDelayMS != 250 {
		t.Fatalf("unexpected ACSRetryBaseDelayMS: %d", cfg.ACSRetryBaseDelayMS)
	}
	if cfg.ACSHTTPTimeoutMS != 15000 {
		t.Fatalf("unexpected ACSHTTPTimeoutMS: %d", cfg.ACSHTTPTimeoutMS)
	}
	if cfg.ACSHTTPMaxIdleConns != 300 {
		t.Fatalf("unexpected ACSHTTPMaxIdleConns: %d", cfg.ACSHTTPMaxIdleConns)
	}
	if cfg.ACSHTTPMaxIdleConnsPerHost != 60 {
		t.Fatalf("unexpected ACSHTTPMaxIdleConnsPerHost: %d", cfg.ACSHTTPMaxIdleConnsPerHost)
	}
	if cfg.ACSHTTPIdleConnTimeoutMS != 45000 {
		t.Fatalf("unexpected ACSHTTPIdleConnTimeoutMS: %d", cfg.ACSHTTPIdleConnTimeoutMS)
	}
	if cfg.ACSTLSCAFile != "/mnt/secrets/proxy-ca.pem" {
		t.Fatalf("unexpected ACSTLSCAFile: %q", cfg.ACSTLSCAFile)
	}
	if cfg.ACSTLSCAPEM == "" {
		t.Fatal("expected ACSTLSCAPEM to be loaded")
	}
}

func TestLoadTLSCAPEMFromFile(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	content := "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	t.Setenv("ACS_TLS_CA_PEM_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if strings.TrimSpace(cfg.ACSTLSCAPEM) != strings.TrimSpace(content) {
		t.Fatalf("unexpected ACSTLSCAPEM: %q", cfg.ACSTLSCAPEM)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		substr string
	}{
		{
			name: "invalid delivery mode",
			env: map[string]string{
				"DELIVERY_MODE": "bad",
			},
			substr: "DELIVERY_MODE must be one of",
		},
		{
			name: "acs mode requires connection string",
			env: map[string]string{
				"ACS_CONNECTION_STRING": "",
			},
			substr: "ACS_CONNECTION_STRING is required",
		},
		{
			name: "acs mode requires sender",
			env: map[string]string{
				"ACS_SENDER": "",
			},
			substr: "ACS_SENDER is required",
		},
		{
			name: "cidr required",
			env: map[string]string{
				"SMTP_ALLOWED_CIDRS": "",
			},
			substr: "SMTP_ALLOWED_CIDRS must be configured",
		},
		{
			name: "invalid cidr",
			env: map[string]string{
				"SMTP_ALLOWED_CIDRS": "bad-cidr",
			},
			substr: "invalid CIDR",
		},
		{
			name: "auth must be required",
			env: map[string]string{
				"SMTP_REQUIRE_AUTH": "false",
			},
			substr: "SMTP_REQUIRE_AUTH must be true",
		},
		{
			name: "auth provider static",
			env: map[string]string{
				"SMTP_AUTH_PROVIDER": "ldap",
			},
			substr: "is not supported",
		},
		{
			name: "auth username required",
			env: map[string]string{
				"SMTP_AUTH_USERNAME": "",
			},
			substr: "SMTP_AUTH_USERNAME is required",
		},
		{
			name: "auth password required",
			env: map[string]string{
				"SMTP_AUTH_PASSWORD": "",
			},
			substr: "SMTP_AUTH_PASSWORD is required",
		},
		{
			name: "tls files required when starttls enabled",
			env: map[string]string{
				"SMTP_TLS_CERT_FILE": "",
			},
			substr: "SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE are required",
		},
		{
			name: "require tls requires tls mode",
			env: map[string]string{
				"SMTP_REQUIRE_TLS":      "true",
				"SMTP_STARTTLS_ENABLED": "false",
				"SMTPS_LISTEN_ADDR":     "",
			},
			substr: "SMTP_REQUIRE_TLS=true requires",
		},
		{
			name: "inflight positive",
			env: map[string]string{
				"SMTP_MAX_INFLIGHT_SENDS": "0",
			},
			substr: "SMTP_MAX_INFLIGHT_SENDS must be >= 1",
		},
		{
			name: "retry attempts positive",
			env: map[string]string{
				"ACS_RETRY_ATTEMPTS": "0",
			},
			substr: "ACS_RETRY_ATTEMPTS must be >= 1",
		},
		{
			name: "retry delay positive",
			env: map[string]string{
				"ACS_RETRY_BASE_DELAY_MS": "0",
			},
			substr: "ACS_RETRY_BASE_DELAY_MS must be >= 1",
		},
		{
			name: "retry attempts upper bounded",
			env: map[string]string{
				"ACS_RETRY_ATTEMPTS": "11",
			},
			substr: "ACS_RETRY_ATTEMPTS must be <=",
		},
		{
			name: "timeout positive",
			env: map[string]string{
				"ACS_HTTP_TIMEOUT_MS": "0",
			},
			substr: "ACS_HTTP_TIMEOUT_MS must be >= 1",
		},
		{
			name: "max idle positive",
			env: map[string]string{
				"ACS_HTTP_MAX_IDLE_CONNS": "0",
			},
			substr: "ACS_HTTP_MAX_IDLE_CONNS must be >= 1",
		},
		{
			name: "max idle per host positive",
			env: map[string]string{
				"ACS_HTTP_MAX_IDLE_CONNS_PER_HOST": "0",
			},
			substr: "ACS_HTTP_MAX_IDLE_CONNS_PER_HOST must be >= 1",
		},
		{
			name: "idle timeout positive",
			env: map[string]string{
				"ACS_HTTP_IDLE_CONN_TIMEOUT_MS": "0",
			},
			substr: "ACS_HTTP_IDLE_CONN_TIMEOUT_MS must be >= 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setSecureDefaults(t)
			setACSDefaults(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.substr) {
				t.Fatalf("expected error to contain %q, got %q", tc.substr, err.Error())
			}
		})
	}
}

func TestParseCIDRs(t *testing.T) {
	prefixes, err := ParseCIDRs([]string{"127.0.0.1/32", " 10.0.0.0/8 "})
	if err != nil {
		t.Fatalf("ParseCIDRs() error: %v", err)
	}
	if len(prefixes) != 2 {
		t.Fatalf("unexpected prefix count: %d", len(prefixes))
	}
}

func setSecureDefaults(t *testing.T) {
	t.Helper()
	t.Setenv("SMTP_ALLOWED_CIDRS", "127.0.0.1/32")
	t.Setenv("SMTP_REQUIRE_AUTH", "true")
	t.Setenv("SMTP_AUTH_PROVIDER", "static")
	t.Setenv("SMTP_AUTH_USERNAME", "jira")
	t.Setenv("SMTP_AUTH_PASSWORD", "secret")
	t.Setenv("SMTP_STARTTLS_ENABLED", "true")
	t.Setenv("SMTP_TLS_CERT_FILE", "/tls/tls.crt")
	t.Setenv("SMTP_TLS_KEY_FILE", "/tls/tls.key")
}

func setACSDefaults(t *testing.T) {
	t.Helper()
	t.Setenv("DELIVERY_MODE", "acs")
	t.Setenv("ACS_CONNECTION_STRING", "endpoint=https://example.communication.azure.us;accesskey=Zm9v")
	t.Setenv("ACS_SENDER", "no-reply@example.com")
}
