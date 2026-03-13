package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// ServerConfig defines the observability listener surface and readiness callback.
type ServerConfig struct {
	Addr    string
	ReadyFn func() bool
	Metrics *Metrics
}

type Server struct {
	srv    *http.Server
	logger *slog.Logger
}

func NewServer(cfg ServerConfig, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NewMetrics(nil)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if cfg.ReadyFn != nil && !cfg.ReadyFn() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/metrics", cfg.Metrics.Handler())

	return &Server{
		srv: &http.Server{
			Addr:    cfg.Addr,
			Handler: mux,
		},
		logger: logger,
	}
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("starting observability server", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown observability server: %w", err)
	}
	return nil
}
