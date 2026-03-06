package httpclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config defines outbound HTTP transport settings shared by providers.
type Config struct {
	Timeout             time.Duration
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
	TLSCAFile           string
	TLSCAPEM            string
}

// Build creates an HTTP client with proxy-aware transport and optional extra trusted roots.
func Build(cfg Config) (*http.Client, error) {
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("http timeout must be > 0")
	}
	if cfg.MaxIdleConns <= 0 {
		return nil, fmt.Errorf("http max idle conns must be > 0")
	}
	if cfg.MaxIdleConnsPerHost <= 0 {
		return nil, fmt.Errorf("http max idle conns per host must be > 0")
	}
	if cfg.IdleConnTimeout <= 0 {
		return nil, fmt.Errorf("http idle conn timeout must be > 0")
	}

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not *http.Transport")
	}

	transport := baseTransport.Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.MaxIdleConns = cfg.MaxIdleConns
	transport.MaxIdleConnsPerHost = cfg.MaxIdleConnsPerHost
	transport.IdleConnTimeout = cfg.IdleConnTimeout

	extraPEMs, err := collectExtraPEMs(cfg.TLSCAFile, cfg.TLSCAPEM)
	if err != nil {
		return nil, err
	}
	if len(extraPEMs) > 0 {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}

		for _, pemBytes := range extraPEMs {
			if !roots.AppendCertsFromPEM(pemBytes) {
				return nil, fmt.Errorf("failed to append tls ca certificates")
			}
		}

		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.RootCAs = roots
	}

	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}, nil
}

func collectExtraPEMs(filePath, inlinePEM string) ([][]byte, error) {
	out := make([][]byte, 0, 2)

	filePath = strings.TrimSpace(filePath)
	if filePath != "" {
		b, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read tls ca file %q: %w", filePath, err)
		}
		pemBytes, err := validatePEM(b)
		if err != nil {
			return nil, err
		}
		out = append(out, pemBytes)
	}

	inlinePEM = strings.TrimSpace(inlinePEM)
	if inlinePEM != "" {
		pemBytes, err := validatePEM([]byte(inlinePEM))
		if err != nil {
			return nil, err
		}
		out = append(out, pemBytes)
	}

	return out, nil
}

func validatePEM(raw []byte) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("tls ca pem cannot be empty")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("tls ca pem does not contain valid certificates")
	}

	cp := make([]byte, len(raw))
	copy(cp, raw)
	return cp, nil
}
