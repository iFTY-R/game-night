package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
)

var errInvalidRuntime = errors.New("invalid API runtime configuration")

// Runtime owns one HTTP listener. The process can create separate instances
// for user and administrator surfaces without sharing handlers or interceptors.
type Runtime struct {
	server          *http.Server
	shutdownTimeout time.Duration
}

// NewRuntime applies the validated listener bounds to an immutable surface handler.
func NewRuntime(config apiConfig.ListenerConfig, handler http.Handler) (*Runtime, error) {
	if handler == nil || config.Address == "" || config.ReadHeaderTimeout <= 0 || config.ReadTimeout <= 0 ||
		config.WriteTimeout <= 0 || config.IdleTimeout <= 0 || config.ShutdownTimeout <= 0 || config.MaxHeaderBytes <= 0 {
		return nil, errInvalidRuntime
	}
	return &Runtime{
		server: &http.Server{
			Addr: config.Address, Handler: handler, ReadHeaderTimeout: config.ReadHeaderTimeout,
			ReadTimeout: config.ReadTimeout, WriteTimeout: config.WriteTimeout,
			IdleTimeout: config.IdleTimeout, MaxHeaderBytes: config.MaxHeaderBytes,
		},
		shutdownTimeout: config.ShutdownTimeout,
	}, nil
}

// ListenAndServe starts the configured listener and normalizes a graceful stop to nil.
func (runtime *Runtime) ListenAndServe() error {
	if runtime == nil || runtime.server == nil {
		return errInvalidRuntime
	}
	return normalizeServeError(runtime.server.ListenAndServe())
}

// Serve accepts a caller-owned listener, which keeps tests and dual-surface process wiring deterministic.
func (runtime *Runtime) Serve(listener net.Listener) error {
	if runtime == nil || runtime.server == nil || listener == nil {
		return errInvalidRuntime
	}
	return normalizeServeError(runtime.server.Serve(listener))
}

// Shutdown stops accepting connections and drains active requests within the configured timeout.
func (runtime *Runtime) Shutdown(parent context.Context) error {
	if runtime == nil || runtime.server == nil {
		return errInvalidRuntime
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runtime.shutdownTimeout)
	defer cancel()
	return runtime.server.Shutdown(ctx)
}

// Close force-closes active connections after a failed graceful drain.
func (runtime *Runtime) Close() error {
	if runtime == nil || runtime.server == nil {
		return errInvalidRuntime
	}
	return runtime.server.Close()
}

func normalizeServeError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
