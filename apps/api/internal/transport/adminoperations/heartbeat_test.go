package adminoperations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
)

const heartbeatTestToken = "heartbeat-handler-test-token-123456789"

type heartbeatRepositoryStub struct {
	instance operations.ServiceInstance
	err      error
	calls    int
}

func (repository *heartbeatRepositoryStub) UpsertServiceInstance(_ context.Context, instance operations.ServiceInstance) (operations.ServiceInstance, error) {
	repository.calls++
	repository.instance = instance
	return instance, repository.err
}

func TestHeartbeatHandlerPersistsServerTimestampedInstance(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 27, 10, 0, 5, 0, time.UTC)
	repository := &heartbeatRepositoryStub{}
	handler, err := NewHeartbeatHandler(repository, heartbeatTestToken, clock.NewFake(receivedAt))
	if err != nil {
		t.Fatal(err)
	}

	request := heartbeatRequest(serviceheartbeat.Path, http.MethodPost, heartbeatTestToken, `{
		"service_kind":"api",
		"instance_id":"api-local-1",
		"build_version":"test-build",
		"started_at":"2026-07-27T10:00:00Z",
		"status":"healthy",
		"components":{"postgresql":"healthy"},
		"maintenance_version":3
	}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response code=%d body=%q cache=%q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
	}
	if repository.calls != 1 || !repository.instance.LastHeartbeatAt.Equal(receivedAt) || repository.instance.Kind != operations.ServiceAPI {
		t.Fatalf("persisted instance=%+v calls=%d", repository.instance, repository.calls)
	}
}

func TestHeartbeatHandlerRejectsRequestsOutsidePrivateProtocol(t *testing.T) {
	repository := &heartbeatRepositoryStub{}
	handler, err := NewHeartbeatHandler(repository, heartbeatTestToken, clock.NewFake(time.Date(2026, time.July, 27, 10, 0, 5, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	validBody := `{"service_kind":"worker","instance_id":"worker-1","build_version":"test","started_at":"2026-07-27T10:00:00Z","status":"healthy","components":{},"maintenance_version":1}`

	tests := []struct {
		name        string
		path        string
		method      string
		token       string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "wrong path", path: serviceheartbeat.Path + "/", method: http.MethodPost, token: heartbeatTestToken, contentType: "application/json", body: validBody, wantStatus: http.StatusNotFound},
		{name: "wrong method", path: serviceheartbeat.Path, method: http.MethodGet, token: heartbeatTestToken, contentType: "application/json", body: validBody, wantStatus: http.StatusMethodNotAllowed},
		{name: "missing token", path: serviceheartbeat.Path, method: http.MethodPost, contentType: "application/json", body: validBody, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", path: serviceheartbeat.Path, method: http.MethodPost, token: heartbeatTestToken + "-wrong", contentType: "application/json", body: validBody, wantStatus: http.StatusUnauthorized},
		{name: "wrong media type", path: serviceheartbeat.Path, method: http.MethodPost, token: heartbeatTestToken, contentType: "text/plain", body: validBody, wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown field", path: serviceheartbeat.Path, method: http.MethodPost, token: heartbeatTestToken, contentType: "application/json", body: strings.TrimSuffix(validBody, "}") + `,"raw_error":"secret"}`, wantStatus: http.StatusBadRequest},
		{name: "trailing document", path: serviceheartbeat.Path, method: http.MethodPost, token: heartbeatTestToken, contentType: "application/json", body: validBody + `{}`, wantStatus: http.StatusBadRequest},
		{name: "oversized body", path: serviceheartbeat.Path, method: http.MethodPost, token: heartbeatTestToken, contentType: "application/json", body: `{"padding":"` + strings.Repeat("x", int(serviceheartbeat.MaximumBodyBytes)) + `"}`, wantStatus: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := heartbeatRequest(test.path, test.method, test.token, test.body)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
	if repository.calls != 0 {
		t.Fatalf("invalid requests reached repository %d times", repository.calls)
	}
}

func TestHeartbeatHandlerMapsRepositoryFailureWithoutLeakingDetails(t *testing.T) {
	repository := &heartbeatRepositoryStub{err: errors.New("database password leaked")}
	handler, err := NewHeartbeatHandler(repository, heartbeatTestToken, clock.NewFake(time.Date(2026, time.July, 27, 10, 0, 5, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	request := heartbeatRequest(serviceheartbeat.Path, http.MethodPost, heartbeatTestToken, `{"service_kind":"edge","instance_id":"edge-1","build_version":"test","started_at":"2026-07-27T10:00:00Z","status":"healthy","components":{},"maintenance_version":1}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("response code=%d body=%q", response.Code, response.Body.String())
	}
}

func heartbeatRequest(path, method, token, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}
