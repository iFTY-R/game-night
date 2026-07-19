package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

const (
	// ReadinessPath reports whether ordinary authenticated reads may be served.
	ReadinessPath = "/readyz"
	// SensitiveReadinessPath reports the stricter gate used by state-changing and secret-bearing operations.
	SensitiveReadinessPath = "/readyz/sensitive"
)

const (
	componentPostgreSQL = "postgresql"
	componentRedis      = "redis"
	componentKeyring    = "keyring"
	componentBootstrap  = "bootstrap"
	componentCheckpoint = "checkpoint"
)

var errInvalidReadiness = errors.New("invalid API readiness configuration")

// Checker returns nil only while its named dependency is usable. Returned
// errors are deliberately reduced to a bounded status before serialization.
type Checker interface {
	Check(context.Context) error
}

// CheckFunc adapts small dependency probes without exposing their concrete clients to the server package.
type CheckFunc func(context.Context) error

// Check executes the wrapped readiness function.
func (check CheckFunc) Check(ctx context.Context) error {
	if check == nil {
		return errInvalidReadiness
	}
	return check(ctx)
}

// ReadinessChecks names every component required by the API security model.
type ReadinessChecks struct {
	PostgreSQL Checker
	Redis      Checker
	Keyring    Checker
	Bootstrap  Checker
	Checkpoint Checker
}

// Readiness evaluates current component state for both readiness modes.
// PostgreSQL, keyrings, and bootstrap coordination gate ordinary reads;
// Redis and audit checkpoint health additionally gate sensitive writes.
type Readiness struct{ checks ReadinessChecks }

// NewReadiness rejects partial health wiring so a missing security component cannot be reported as healthy.
func NewReadiness(checks ReadinessChecks) (*Readiness, error) {
	if checks.PostgreSQL == nil || checks.Redis == nil || checks.Keyring == nil || checks.Bootstrap == nil || checks.Checkpoint == nil {
		return nil, errInvalidReadiness
	}
	return &Readiness{checks: checks}, nil
}

// ReadyForOrdinaryReads evaluates the dependency subset required by authenticated read paths.
func (readiness *Readiness) ReadyForOrdinaryReads(ctx context.Context) bool {
	return readiness != nil && readiness.evaluate(ctx, ordinaryReadiness).Ready
}

// ReadyForSensitiveWrites evaluates every dependency, including Redis and durable checkpoint health.
func (readiness *Readiness) ReadyForSensitiveWrites(ctx context.Context) bool {
	return readiness != nil && readiness.evaluate(ctx, sensitiveReadiness).Ready
}

type readinessMode string

const (
	ordinaryReadiness  readinessMode = "ordinary"
	sensitiveReadiness readinessMode = "sensitive_write"
)

type readinessResponse struct {
	Mode       readinessMode     `json:"mode"`
	Ready      bool              `json:"ready"`
	Components map[string]string `json:"components"`
}

func mountReadiness(mux *http.ServeMux, readiness *Readiness) {
	mux.Handle(ReadinessPath, readiness.handler(ordinaryReadiness))
	mux.Handle(SensitiveReadinessPath, readiness.handler(sensitiveReadiness))
}

func (readiness *Readiness) handler(mode readinessMode) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		result := readiness.evaluate(request.Context(), mode)
		if !result.Ready {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
		if request.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(writer).Encode(result)
	})
}

func (readiness *Readiness) evaluate(ctx context.Context, mode readinessMode) readinessResponse {
	components := map[string]string{
		componentPostgreSQL: componentStatus(readiness.checks.PostgreSQL, ctx),
		componentRedis:      componentStatus(readiness.checks.Redis, ctx),
		componentKeyring:    componentStatus(readiness.checks.Keyring, ctx),
		componentBootstrap:  componentStatus(readiness.checks.Bootstrap, ctx),
		componentCheckpoint: componentStatus(readiness.checks.Checkpoint, ctx),
	}
	ready := components[componentPostgreSQL] == "ready" &&
		components[componentKeyring] == "ready" && components[componentBootstrap] == "ready"
	if mode == sensitiveReadiness {
		ready = ready && components[componentRedis] == "ready" && components[componentCheckpoint] == "ready"
	}
	return readinessResponse{Mode: mode, Ready: ready, Components: components}
}

func componentStatus(check Checker, ctx context.Context) string {
	if check == nil || check.Check(ctx) != nil {
		return "unavailable"
	}
	return "ready"
}
