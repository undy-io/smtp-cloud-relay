package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const (
	defaultSMTPListenAddr                  = "0.0.0.0:2525"
	defaultHTTPListenAddr                  = "0.0.0.0:8080"
	defaultDeliveryRetryAttempts           = 3
	maxDeliveryRetryAttempts               = 10
	defaultDeliveryRetryBaseDelayMS        = 1000
	defaultDeliveryMode                    = "acs"
	defaultSMTPRequireAuth                 = true
	defaultSMTPAuthProvider                = "static"
	defaultSMTPStartTLSEnabled             = true
	defaultSMTPRequireTLS                  = false
	defaultSMTPMaxInflightSends            = 200
	defaultDeliveryHTTPTimeoutMS           = 30000
	defaultDeliveryHTTPMaxIdleConns        = 200
	defaultDeliveryHTTPMaxIdleConnsPerHost = 50
	defaultDeliveryHTTPIdleConnTimeoutMS   = 90000
)

// Config is runtime configuration loaded from env vars and/or mounted secret files.
type Config struct {
	SMTPListenAddr       string
	HTTPListenAddr       string
	DeliveryMode         string
	SMTPAllowedCIDRs     []string
	SMTPRequireAuth      bool
	SMTPAuthProvider     string
	SMTPAuthUsername     string
	SMTPAuthPassword     string
	SMTPStartTLSEnabled  bool
	SMTPRequireTLS       bool
	SMTPSListenAddr      string
	SMTPTLSCertFile      string
	SMTPTLSKeyFile       string
	SMTPMaxInflightSends int

	DeliveryRetryAttempts           int
	DeliveryRetryBaseDelayMS        int
	DeliveryHTTPTimeoutMS           int
	DeliveryHTTPMaxIdleConns        int
	DeliveryHTTPMaxIdleConnsPerHost int
	DeliveryHTTPIdleConnTimeoutMS   int

	ACSEndpoint         string
	ACSConnectionString string
	ACSSender           string
	OutboundTLSCAFile   string
	OutboundTLSCAPEM    string

	SESRegion           string
	SESSender           string
	SESEndpoint         string
	SESConfigurationSet string
	SESAccessKeyID      string
	SESSecretAccessKey  string
	SESSessionToken     string
}

// Load reads configuration from environment.
// For each variable X, this also supports X_FILE to read secret values from mounted files.
func Load() (Config, error) {
	cfg := Config{
		SMTPListenAddr:                  defaultSMTPListenAddr,
		HTTPListenAddr:                  defaultHTTPListenAddr,
		DeliveryMode:                    defaultDeliveryMode,
		SMTPRequireAuth:                 defaultSMTPRequireAuth,
		SMTPAuthProvider:                defaultSMTPAuthProvider,
		SMTPStartTLSEnabled:             defaultSMTPStartTLSEnabled,
		SMTPRequireTLS:                  defaultSMTPRequireTLS,
		SMTPMaxInflightSends:            defaultSMTPMaxInflightSends,
		DeliveryRetryAttempts:           defaultDeliveryRetryAttempts,
		DeliveryRetryBaseDelayMS:        defaultDeliveryRetryBaseDelayMS,
		DeliveryHTTPTimeoutMS:           defaultDeliveryHTTPTimeoutMS,
		DeliveryHTTPMaxIdleConns:        defaultDeliveryHTTPMaxIdleConns,
		DeliveryHTTPMaxIdleConnsPerHost: defaultDeliveryHTTPMaxIdleConnsPerHost,
		DeliveryHTTPIdleConnTimeoutMS:   defaultDeliveryHTTPIdleConnTimeoutMS,
	}

	var err error

	if v, err := envOrFile("SMTP_LISTEN_ADDR"); err != nil {
		return Config{}, err
	} else if v != "" {
		cfg.SMTPListenAddr = v
	}

	if v, err := envOrFile("HTTP_LISTEN_ADDR"); err != nil {
		return Config{}, err
	} else if v != "" {
		cfg.HTTPListenAddr = v
	}

	if v, err := envOrFile("DELIVERY_MODE"); err != nil {
		return Config{}, err
	} else if v != "" {
		cfg.DeliveryMode = strings.ToLower(strings.TrimSpace(v))
	}

	cfg.SMTPAllowedCIDRs, err = envOrFileList("SMTP_ALLOWED_CIDRS")
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPRequireAuth, err = envOrFileBool("SMTP_REQUIRE_AUTH", defaultSMTPRequireAuth)
	if err != nil {
		return Config{}, err
	}

	if v, err := envOrFile("SMTP_AUTH_PROVIDER"); err != nil {
		return Config{}, err
	} else if v != "" {
		cfg.SMTPAuthProvider = strings.ToLower(strings.TrimSpace(v))
	}

	cfg.SMTPAuthUsername, err = envOrFile("SMTP_AUTH_USERNAME")
	if err != nil {
		return Config{}, err
	}
	cfg.SMTPAuthPassword, err = envOrFile("SMTP_AUTH_PASSWORD")
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPStartTLSEnabled, err = envOrFileBool("SMTP_STARTTLS_ENABLED", defaultSMTPStartTLSEnabled)
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPRequireTLS, err = envOrFileBool("SMTP_REQUIRE_TLS", defaultSMTPRequireTLS)
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPSListenAddr, err = envOrFile("SMTPS_LISTEN_ADDR")
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPTLSCertFile, err = envOrFile("SMTP_TLS_CERT_FILE")
	if err != nil {
		return Config{}, err
	}
	cfg.SMTPTLSKeyFile, err = envOrFile("SMTP_TLS_KEY_FILE")
	if err != nil {
		return Config{}, err
	}

	cfg.SMTPMaxInflightSends, err = envOrFileInt("SMTP_MAX_INFLIGHT_SENDS", defaultSMTPMaxInflightSends)
	if err != nil {
		return Config{}, err
	}

	cfg.DeliveryRetryAttempts, err = envOrFileInt("DELIVERY_RETRY_ATTEMPTS", defaultDeliveryRetryAttempts)
	if err != nil {
		return Config{}, err
	}
	cfg.DeliveryRetryBaseDelayMS, err = envOrFileInt("DELIVERY_RETRY_BASE_DELAY_MS", defaultDeliveryRetryBaseDelayMS)
	if err != nil {
		return Config{}, err
	}
	cfg.DeliveryHTTPTimeoutMS, err = envOrFileInt("DELIVERY_HTTP_TIMEOUT_MS", defaultDeliveryHTTPTimeoutMS)
	if err != nil {
		return Config{}, err
	}
	cfg.DeliveryHTTPMaxIdleConns, err = envOrFileInt("DELIVERY_HTTP_MAX_IDLE_CONNS", defaultDeliveryHTTPMaxIdleConns)
	if err != nil {
		return Config{}, err
	}
	cfg.DeliveryHTTPMaxIdleConnsPerHost, err = envOrFileInt("DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST", defaultDeliveryHTTPMaxIdleConnsPerHost)
	if err != nil {
		return Config{}, err
	}
	cfg.DeliveryHTTPIdleConnTimeoutMS, err = envOrFileInt("DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS", defaultDeliveryHTTPIdleConnTimeoutMS)
	if err != nil {
		return Config{}, err
	}

	cfg.ACSEndpoint, err = envOrFile("ACS_ENDPOINT")
	if err != nil {
		return Config{}, err
	}
	cfg.ACSConnectionString, err = envOrFile("ACS_CONNECTION_STRING")
	if err != nil {
		return Config{}, err
	}
	cfg.ACSSender, err = envOrFile("ACS_SENDER")
	if err != nil {
		return Config{}, err
	}
	cfg.OutboundTLSCAFile, err = envOrFileCompat("OUTBOUND_TLS_CA_FILE", "ACS_TLS_CA_FILE")
	if err != nil {
		return Config{}, err
	}
	cfg.OutboundTLSCAPEM, err = envOrFileCompat("OUTBOUND_TLS_CA_PEM", "ACS_TLS_CA_PEM")
	if err != nil {
		return Config{}, err
	}

	cfg.SESRegion, err = envOrFile("SES_REGION")
	if err != nil {
		return Config{}, err
	}
	cfg.SESSender, err = envOrFile("SES_SENDER")
	if err != nil {
		return Config{}, err
	}
	cfg.SESEndpoint, err = envOrFile("SES_ENDPOINT")
	if err != nil {
		return Config{}, err
	}
	cfg.SESConfigurationSet, err = envOrFile("SES_CONFIGURATION_SET")
	if err != nil {
		return Config{}, err
	}
	cfg.SESAccessKeyID, err = envOrFile("SES_ACCESS_KEY_ID")
	if err != nil {
		return Config{}, err
	}
	cfg.SESSecretAccessKey, err = envOrFile("SES_SECRET_ACCESS_KEY")
	if err != nil {
		return Config{}, err
	}
	cfg.SESSessionToken, err = envOrFile("SES_SESSION_TOKEN")
	if err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	switch c.DeliveryMode {
	case "acs", "noop", "ses":
	default:
		return fmt.Errorf("DELIVERY_MODE must be one of: acs, noop, ses")
	}

	switch c.DeliveryMode {
	case "acs":
		if strings.TrimSpace(c.ACSConnectionString) == "" {
			return fmt.Errorf("ACS_CONNECTION_STRING is required when DELIVERY_MODE=acs")
		}
		if strings.TrimSpace(c.ACSSender) == "" {
			return fmt.Errorf("ACS_SENDER is required when DELIVERY_MODE=acs")
		}
	case "ses":
		if strings.TrimSpace(c.SESRegion) == "" {
			return fmt.Errorf("SES_REGION is required when DELIVERY_MODE=ses")
		}
		if strings.TrimSpace(c.SESSender) == "" {
			return fmt.Errorf("SES_SENDER is required when DELIVERY_MODE=ses")
		}
		access := strings.TrimSpace(c.SESAccessKeyID)
		secret := strings.TrimSpace(c.SESSecretAccessKey)
		hasAccess := access != ""
		hasSecret := secret != ""
		if hasAccess != hasSecret {
			return fmt.Errorf("SES_ACCESS_KEY_ID and SES_SECRET_ACCESS_KEY must both be set or both be empty")
		}
		if strings.TrimSpace(c.SESSessionToken) != "" && (!hasAccess || !hasSecret) {
			return fmt.Errorf("SES_SESSION_TOKEN requires SES_ACCESS_KEY_ID and SES_SECRET_ACCESS_KEY")
		}
	}

	if len(c.SMTPAllowedCIDRs) == 0 {
		return fmt.Errorf("SMTP_ALLOWED_CIDRS must be configured to avoid open relay")
	}
	if _, err := ParseCIDRs(c.SMTPAllowedCIDRs); err != nil {
		return err
	}

	if !c.SMTPRequireAuth {
		return fmt.Errorf("SMTP_REQUIRE_AUTH must be true to avoid open relay")
	}
	if strings.TrimSpace(c.SMTPAuthProvider) != "static" {
		return fmt.Errorf("SMTP_AUTH_PROVIDER %q is not supported; expected static", c.SMTPAuthProvider)
	}
	if strings.TrimSpace(c.SMTPAuthUsername) == "" {
		return fmt.Errorf("SMTP_AUTH_USERNAME is required when SMTP_REQUIRE_AUTH=true")
	}
	if strings.TrimSpace(c.SMTPAuthPassword) == "" {
		return fmt.Errorf("SMTP_AUTH_PASSWORD is required when SMTP_REQUIRE_AUTH=true")
	}

	tlsConfigured := strings.TrimSpace(c.SMTPTLSCertFile) != "" && strings.TrimSpace(c.SMTPTLSKeyFile) != ""
	if c.SMTPStartTLSEnabled || strings.TrimSpace(c.SMTPSListenAddr) != "" {
		if !tlsConfigured {
			return fmt.Errorf("SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE are required when STARTTLS or SMTPS is enabled")
		}
	}
	if c.SMTPRequireTLS && !c.SMTPStartTLSEnabled && strings.TrimSpace(c.SMTPSListenAddr) == "" {
		return fmt.Errorf("SMTP_REQUIRE_TLS=true requires SMTP_STARTTLS_ENABLED=true or SMTPS_LISTEN_ADDR to be set")
	}

	if c.SMTPMaxInflightSends < 1 {
		return fmt.Errorf("SMTP_MAX_INFLIGHT_SENDS must be >= 1")
	}
	if c.DeliveryRetryAttempts < 1 {
		return fmt.Errorf("DELIVERY_RETRY_ATTEMPTS must be >= 1")
	}
	if c.DeliveryRetryAttempts > maxDeliveryRetryAttempts {
		return fmt.Errorf("DELIVERY_RETRY_ATTEMPTS must be <= %d", maxDeliveryRetryAttempts)
	}
	if c.DeliveryRetryBaseDelayMS < 1 {
		return fmt.Errorf("DELIVERY_RETRY_BASE_DELAY_MS must be >= 1")
	}
	if c.DeliveryHTTPTimeoutMS < 1 {
		return fmt.Errorf("DELIVERY_HTTP_TIMEOUT_MS must be >= 1")
	}
	if c.DeliveryHTTPMaxIdleConns < 1 {
		return fmt.Errorf("DELIVERY_HTTP_MAX_IDLE_CONNS must be >= 1")
	}
	if c.DeliveryHTTPMaxIdleConnsPerHost < 1 {
		return fmt.Errorf("DELIVERY_HTTP_MAX_IDLE_CONNS_PER_HOST must be >= 1")
	}
	if c.DeliveryHTTPIdleConnTimeoutMS < 1 {
		return fmt.Errorf("DELIVERY_HTTP_IDLE_CONN_TIMEOUT_MS must be >= 1")
	}

	return nil
}

func ParseCIDRs(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		pfx, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR in SMTP_ALLOWED_CIDRS: %q: %w", raw, err)
		}
		out = append(out, pfx)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("SMTP_ALLOWED_CIDRS contains no valid entries")
	}
	return out, nil
}

func envOrFile(key string) (string, error) {
	if val, ok := os.LookupEnv(key); ok {
		trimmed := strings.TrimSpace(val)
		if trimmed != "" {
			return trimmed, nil
		}
	}

	if path, ok := os.LookupEnv(key + "_FILE"); ok {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s from file %q: %w", key, path, err)
		}
		return strings.TrimSpace(string(b)), nil
	}

	return "", nil
}

func envOrFileCompat(primary string, aliases ...string) (string, error) {
	keys := append([]string{primary}, aliases...)
	for _, key := range keys {
		val, err := envOrFile(key)
		if err != nil {
			return "", err
		}
		if val != "" {
			return val, nil
		}
	}
	return "", nil
}

func envOrFileList(key string) ([]string, error) {
	raw, err := envOrFile(key)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}

	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		trimmed := strings.TrimSpace(f)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out, nil
}

func envOrFileInt(key string, defaultValue int) (int, error) {
	raw, err := envOrFile(key)
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return defaultValue, nil
	}

	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	if v < 1 {
		return 0, fmt.Errorf("%s must be >= 1", key)
	}
	return v, nil
}

func envOrFileBool(key string, defaultValue bool) (bool, error) {
	raw, err := envOrFile(key)
	if err != nil {
		return false, err
	}
	if raw == "" {
		return defaultValue, nil
	}

	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return v, nil
}
