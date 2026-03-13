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
	if cfg.DeliveryRetryAttempts != 3 {
		t.Fatalf("unexpected DeliveryRetryAttempts: %d", cfg.DeliveryRetryAttempts)
	}
	if cfg.DeliveryRetryBaseDelayMS != 1000 {
		t.Fatalf("unexpected DeliveryRetryBaseDelayMS: %d", cfg.DeliveryRetryBaseDelayMS)
	}
	if cfg.DeliveryHTTPTimeoutMS != 30000 {
		t.Fatalf("unexpected DeliveryHTTPTimeoutMS: %d", cfg.DeliveryHTTPTimeoutMS)
	}
	if cfg.DeliveryHTTPMaxIdleConns != 200 {
		t.Fatalf("unexpected DeliveryHTTPMaxIdleConns: %d", cfg.DeliveryHTTPMaxIdleConns)
	}
	if cfg.DeliveryHTTPMaxIdleConnsPerHost != 50 {
		t.Fatalf("unexpected DeliveryHTTPMaxIdleConnsPerHost: %d", cfg.DeliveryHTTPMaxIdleConnsPerHost)
	}
	if cfg.DeliveryHTTPIdleConnTimeoutMS != 90000 {
		t.Fatalf("unexpected DeliveryHTTPIdleConnTimeoutMS: %d", cfg.DeliveryHTTPIdleConnTimeoutMS)
	}
	if cfg.SpoolDir != "/var/lib/smtp-cloud-relay/spool" {
		t.Fatalf("unexpected SpoolDir: %q", cfg.SpoolDir)
	}
	if cfg.SpoolPollIntervalMS != 1000 {
		t.Fatalf("unexpected SpoolPollIntervalMS: %d", cfg.SpoolPollIntervalMS)
	}
	if cfg.OutboundTLSCAFile != "" {
		t.Fatalf("unexpected OutboundTLSCAFile default: %q", cfg.OutboundTLSCAFile)
	}
	if cfg.OutboundTLSCAPEM != "" {
		t.Fatalf("unexpected OutboundTLSCAPEM default: %q", cfg.OutboundTLSCAPEM)
	}
	if cfg.SenderPolicyMode != "rewrite" {
		t.Fatalf("unexpected SenderPolicyMode: %q", cfg.SenderPolicyMode)
	}
	if len(cfg.SenderAllowedDomains) != 0 {
		t.Fatalf("unexpected SenderAllowedDomains default: %#v", cfg.SenderAllowedDomains)
	}
	if cfg.SESRegion != "" || cfg.SESSender != "" || cfg.SESAccessKeyID != "" {
		t.Fatalf("expected SES defaults to be empty, got region=%q sender=%q key=%q", cfg.SESRegion, cfg.SESSender, cfg.SESAccessKeyID)
	}
}

func TestLoadNoopModeWithoutProviderConfig(t *testing.T) {
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
	t.Setenv("DELIVERY_MODE", "acs")
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

func TestLoadDeliveryTuningFromEnv(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)
	t.Setenv("DELIVERY_RETRY_ATTEMPTS", "5")
	t.Setenv("DELIVERY_RETRY_BASE_DELAY_MS", "250")
	t.Setenv("DELIVERY_HTTP_TIMEOUT_MS", "15000")
	t.Setenv("DELIVERY_HTTP_MAX_IDLE_CONNS", "300")
	t.Setenv("DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST", "60")
	t.Setenv("DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS", "45000")
	t.Setenv("OUTBOUND_TLS_CA_FILE", "/mnt/secrets/proxy-ca.pem")
	t.Setenv("OUTBOUND_TLS_CA_PEM", "-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DeliveryRetryAttempts != 5 {
		t.Fatalf("unexpected DeliveryRetryAttempts: %d", cfg.DeliveryRetryAttempts)
	}
	if cfg.DeliveryRetryBaseDelayMS != 250 {
		t.Fatalf("unexpected DeliveryRetryBaseDelayMS: %d", cfg.DeliveryRetryBaseDelayMS)
	}
	if cfg.DeliveryHTTPTimeoutMS != 15000 {
		t.Fatalf("unexpected DeliveryHTTPTimeoutMS: %d", cfg.DeliveryHTTPTimeoutMS)
	}
	if cfg.DeliveryHTTPMaxIdleConns != 300 {
		t.Fatalf("unexpected DeliveryHTTPMaxIdleConns: %d", cfg.DeliveryHTTPMaxIdleConns)
	}
	if cfg.DeliveryHTTPMaxIdleConnsPerHost != 60 {
		t.Fatalf("unexpected DeliveryHTTPMaxIdleConnsPerHost: %d", cfg.DeliveryHTTPMaxIdleConnsPerHost)
	}
	if cfg.DeliveryHTTPIdleConnTimeoutMS != 45000 {
		t.Fatalf("unexpected DeliveryHTTPIdleConnTimeoutMS: %d", cfg.DeliveryHTTPIdleConnTimeoutMS)
	}
	if cfg.OutboundTLSCAFile != "/mnt/secrets/proxy-ca.pem" {
		t.Fatalf("unexpected OutboundTLSCAFile: %q", cfg.OutboundTLSCAFile)
	}
	if cfg.OutboundTLSCAPEM == "" {
		t.Fatal("expected OutboundTLSCAPEM to be loaded")
	}
}

func TestLoadSenderPolicyFromEnv(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)
	t.Setenv("SENDER_POLICY_MODE", "strict")
	t.Setenv("SENDER_ALLOWED_DOMAINS", "example.com\nglob:*.example.org\nre:(?:.+\\.)?example\\.gov")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.SenderPolicyMode != "strict" {
		t.Fatalf("unexpected SenderPolicyMode: %q", cfg.SenderPolicyMode)
	}
	if len(cfg.SenderAllowedDomains) != 3 {
		t.Fatalf("unexpected SenderAllowedDomains length: %d", len(cfg.SenderAllowedDomains))
	}
	if cfg.SenderAllowedDomains[0] != "example.com" {
		t.Fatalf("unexpected first SenderAllowedDomains entry: %q", cfg.SenderAllowedDomains[0])
	}
	if cfg.SenderAllowedDomains[1] != "glob:*.example.org" {
		t.Fatalf("unexpected second SenderAllowedDomains entry: %q", cfg.SenderAllowedDomains[1])
	}
	if cfg.SenderAllowedDomains[2] != "re:(?:.+\\.)?example\\.gov" {
		t.Fatalf("unexpected third SenderAllowedDomains entry: %q", cfg.SenderAllowedDomains[2])
	}
}

func TestLoadSESModeAndStaticCredentials(t *testing.T) {
	setSecureDefaults(t)
	t.Setenv("DELIVERY_MODE", "ses")
	t.Setenv("SES_REGION", "us-gov-west-1")
	t.Setenv("SES_SENDER", "no-reply@example.com")
	t.Setenv("SES_CONFIGURATION_SET", "relay-config")
	t.Setenv("SES_ACCESS_KEY_ID", "AKIA_TEST")
	t.Setenv("SES_SECRET_ACCESS_KEY", "secret")
	t.Setenv("SES_SESSION_TOKEN", "token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DeliveryMode != "ses" {
		t.Fatalf("unexpected DeliveryMode: %q", cfg.DeliveryMode)
	}
	if cfg.SESRegion != "us-gov-west-1" {
		t.Fatalf("unexpected SESRegion: %q", cfg.SESRegion)
	}
	if cfg.SESSender != "no-reply@example.com" {
		t.Fatalf("unexpected SESSender: %q", cfg.SESSender)
	}
	if cfg.SESConfigurationSet != "relay-config" {
		t.Fatalf("unexpected SESConfigurationSet: %q", cfg.SESConfigurationSet)
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

	t.Setenv("OUTBOUND_TLS_CA_PEM_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if strings.TrimSpace(cfg.OutboundTLSCAPEM) != strings.TrimSpace(content) {
		t.Fatalf("unexpected OutboundTLSCAPEM: %q", cfg.OutboundTLSCAPEM)
	}
}

func TestLoadOutboundTLSCompatAliases(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)
	t.Setenv("ACS_TLS_CA_FILE", "/mnt/secrets/legacy-ca.pem")
	t.Setenv("ACS_TLS_CA_PEM", "-----BEGIN CERTIFICATE-----\nLEGACY\n-----END CERTIFICATE-----")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.OutboundTLSCAFile != "/mnt/secrets/legacy-ca.pem" {
		t.Fatalf("unexpected OutboundTLSCAFile from alias: %q", cfg.OutboundTLSCAFile)
	}
	if cfg.OutboundTLSCAPEM == "" {
		t.Fatal("expected OutboundTLSCAPEM alias to be loaded")
	}
}

func TestLoadOutboundTLSPrefersProviderNeutralEnv(t *testing.T) {
	setSecureDefaults(t)
	setACSDefaults(t)
	t.Setenv("OUTBOUND_TLS_CA_FILE", "/mnt/secrets/outbound-ca.pem")
	t.Setenv("ACS_TLS_CA_FILE", "/mnt/secrets/legacy-ca.pem")
	t.Setenv("OUTBOUND_TLS_CA_PEM", "-----BEGIN CERTIFICATE-----\nPRIMARY\n-----END CERTIFICATE-----")
	t.Setenv("ACS_TLS_CA_PEM", "-----BEGIN CERTIFICATE-----\nLEGACY\n-----END CERTIFICATE-----")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.OutboundTLSCAFile != "/mnt/secrets/outbound-ca.pem" {
		t.Fatalf("expected provider-neutral CA file to win, got %q", cfg.OutboundTLSCAFile)
	}
	if !strings.Contains(cfg.OutboundTLSCAPEM, "PRIMARY") {
		t.Fatalf("expected provider-neutral PEM to win, got %q", cfg.OutboundTLSCAPEM)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		substr string
	}{
		{
			name: "spool poll interval positive",
			env: map[string]string{
				"SPOOL_POLL_INTERVAL_MS": "0",
			},
			substr: "SPOOL_POLL_INTERVAL_MS must be >= 1",
		},
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
				"DELIVERY_MODE":         "acs",
				"ACS_CONNECTION_STRING": "",
			},
			substr: "ACS_CONNECTION_STRING is required",
		},
		{
			name: "acs mode requires sender",
			env: map[string]string{
				"DELIVERY_MODE": "acs",
				"ACS_SENDER":    "",
			},
			substr: "ACS_SENDER is required",
		},
		{
			name: "ses mode requires region",
			env: map[string]string{
				"DELIVERY_MODE": "ses",
				"SES_REGION":    "",
				"SES_SENDER":    "no-reply@example.com",
			},
			substr: "SES_REGION is required",
		},
		{
			name: "ses mode requires sender",
			env: map[string]string{
				"DELIVERY_MODE": "ses",
				"SES_REGION":    "us-gov-west-1",
				"SES_SENDER":    "",
			},
			substr: "SES_SENDER is required",
		},
		{
			name: "ses static credentials must be paired",
			env: map[string]string{
				"DELIVERY_MODE":         "ses",
				"SES_REGION":            "us-gov-west-1",
				"SES_SENDER":            "no-reply@example.com",
				"SES_ACCESS_KEY_ID":     "AKIA_TEST",
				"SES_SECRET_ACCESS_KEY": "",
			},
			substr: "must both be set or both be empty",
		},
		{
			name: "ses session token requires static credentials",
			env: map[string]string{
				"DELIVERY_MODE":     "ses",
				"SES_REGION":        "us-gov-west-1",
				"SES_SENDER":        "no-reply@example.com",
				"SES_SESSION_TOKEN": "token",
			},
			substr: "SES_SESSION_TOKEN requires",
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
			name: "invalid sender policy mode",
			env: map[string]string{
				"SENDER_POLICY_MODE": "bad",
			},
			substr: "SENDER_POLICY_MODE must be one of",
		},
		{
			name: "retry attempts positive",
			env: map[string]string{
				"DELIVERY_RETRY_ATTEMPTS": "0",
			},
			substr: "DELIVERY_RETRY_ATTEMPTS must be >= 1",
		},
		{
			name: "retry delay positive",
			env: map[string]string{
				"DELIVERY_RETRY_BASE_DELAY_MS": "0",
			},
			substr: "DELIVERY_RETRY_BASE_DELAY_MS must be >= 1",
		},
		{
			name: "retry attempts upper bounded",
			env: map[string]string{
				"DELIVERY_RETRY_ATTEMPTS": "11",
			},
			substr: "DELIVERY_RETRY_ATTEMPTS must be <=",
		},
		{
			name: "timeout positive",
			env: map[string]string{
				"DELIVERY_HTTP_TIMEOUT_MS": "0",
			},
			substr: "DELIVERY_HTTP_TIMEOUT_MS must be >= 1",
		},
		{
			name: "max idle positive",
			env: map[string]string{
				"DELIVERY_HTTP_MAX_IDLE_CONNS": "0",
			},
			substr: "DELIVERY_HTTP_MAX_IDLE_CONNS must be >= 1",
		},
		{
			name: "max idle per host positive",
			env: map[string]string{
				"DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST": "0",
			},
			substr: "DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST must be >= 1",
		},
		{
			name: "idle timeout positive",
			env: map[string]string{
				"DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS": "0",
			},
			substr: "DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS must be >= 1",
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

func TestConfigValidateRejectsEmptySpoolDir(t *testing.T) {
	cfg := Config{
		DeliveryMode:                    "noop",
		SMTPAllowedCIDRs:                []string{"127.0.0.1/32"},
		SMTPRequireAuth:                 true,
		SMTPAuthProvider:                "static",
		SMTPAuthUsername:                "jira",
		SMTPAuthPassword:                "secret",
		SMTPStartTLSEnabled:             true,
		SMTPTLSCertFile:                 "/tls/tls.crt",
		SMTPTLSKeyFile:                  "/tls/tls.key",
		SMTPMaxInflightSends:            1,
		SenderPolicyMode:                "rewrite",
		DeliveryRetryAttempts:           3,
		DeliveryRetryBaseDelayMS:        1000,
		DeliveryHTTPTimeoutMS:           30000,
		DeliveryHTTPMaxIdleConns:        200,
		DeliveryHTTPMaxIdleConnsPerHost: 50,
		DeliveryHTTPIdleConnTimeoutMS:   90000,
		SpoolDir:                        "   ",
		SpoolPollIntervalMS:             1000,
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "SPOOL_DIR must be non-empty") {
		t.Fatalf("expected spool dir validation error, got %q", err.Error())
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
