// Package adminoperations adapts bounded operations domain services to private and Connect transports.
package adminoperations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
)

// HeartbeatRepository persists one server-timestamped service instance.
type HeartbeatRepository interface {
	UpsertServiceInstance(context.Context, operations.ServiceInstance) (operations.ServiceInstance, error)
}

// HeartbeatHandler owns the exact private JSON endpoint outside both Connect surfaces.
type HeartbeatHandler struct {
	repository HeartbeatRepository
	token      string
	clock      clock.Clock
}

// NewHeartbeatHandler validates the private endpoint dependencies before mounting it.
func NewHeartbeatHandler(repository HeartbeatRepository, token string, source clock.Clock) (*HeartbeatHandler, error) {
	if repository == nil || !serviceheartbeat.ValidToken(token) || source == nil {
		return nil, operations.ErrInvalidInput
	}
	return &HeartbeatHandler{repository: repository, token: token, clock: source}, nil
}

// ServeHTTP rejects every method, path, credential, and body shape outside the fixed protocol.
func (handler *HeartbeatHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || request == nil || request.URL.Path != serviceheartbeat.Path {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") || !serviceheartbeat.TokenMatches(handler.token, strings.TrimPrefix(authorization, "Bearer ")) {
		http.Error(writer, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(writer, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, serviceheartbeat.MaximumBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload serviceheartbeat.Payload
	if err = decoder.Decode(&payload); err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	instance, err := payload.Instance(handler.clock.Now())
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if _, err = handler.repository.UpsertServiceInstance(request.Context(), instance); err != nil {
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}
