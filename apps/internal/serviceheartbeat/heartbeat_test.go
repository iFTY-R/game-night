package serviceheartbeat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
)

type repositoryStub struct {
	instance operations.ServiceInstance
	err      error
}

func (repository *repositoryStub) UpsertServiceInstance(_ context.Context, instance operations.ServiceInstance) (operations.ServiceInstance, error) {
	repository.instance = instance
	return instance, repository.err
}

func TestHTTPClientUsesExactPathAndCredential(t *testing.T) {
	token := "heartbeat-integration-token-123456789"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != Path || request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client, err := NewHTTPClient(server.Client(), server.URL+Path, token, false)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Report(context.Background(), Payload{
		ServiceKind: operations.ServiceWorker, InstanceID: "worker-1", BuildVersion: "test", StartedAt: time.Now().UTC(),
		Status: operations.HealthHealthy, Components: map[string]operations.HealthStatus{"postgresql": operations.HealthHealthy}, MaintenanceVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPayloadUsesServerTimeAndRejectsReportedStale(t *testing.T) {
	started := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	received := started.Add(time.Second)
	payload := Payload{
		ServiceKind: operations.ServiceEdge, InstanceID: "edge-1", BuildVersion: "test", StartedAt: started,
		Status: operations.HealthHealthy, Components: map[string]operations.HealthStatus{}, MaintenanceVersion: 1,
	}
	instance, err := payload.Instance(received)
	if err != nil || !instance.LastHeartbeatAt.Equal(received) {
		t.Fatalf("instance=%+v err=%v", instance, err)
	}
	payload.Status = operations.HealthStale
	if _, err = payload.Instance(received); err == nil {
		t.Fatal("reporter-supplied stale status was accepted")
	}
}

func TestLoadConfigValidatesPrivateTargetCredentialAndTiming(t *testing.T) {
	values := map[string]string{
		TargetURLEnvironment:    "http://127.0.0.1:8081" + Path,
		TokenEnvironment:        "heartbeat-config-token-1234567890",
		BuildVersionEnvironment: "v2026.07.27",
		IntervalEnvironment:     "12s",
		TimeoutEnvironment:      "3s",
	}
	config, err := LoadConfig(func(name string) (string, bool) { value, ok := values[name]; return value, ok }, true, false)
	if err != nil || config.Interval != 12*time.Second || config.Timeout != 3*time.Second {
		t.Fatalf("config=%+v err=%v", config, err)
	}

	for name, value := range map[string]string{
		"public path":       "http://127.0.0.1:8081/readyz",
		"credential spaces": " heartbeat-config-token-1234567890",
		"excessive timeout": "7s",
	} {
		t.Run(name, func(t *testing.T) {
			invalid := mapsClone(values)
			switch name {
			case "public path":
				invalid[TargetURLEnvironment] = value
			case "credential spaces":
				invalid[TokenEnvironment] = value
			case "excessive timeout":
				invalid[TimeoutEnvironment] = value
			}
			if _, loadErr := LoadConfig(func(key string) (string, bool) { candidate, ok := invalid[key]; return candidate, ok }, true, false); !errors.Is(loadErr, ErrInvalidConfig) {
				t.Fatalf("error=%v", loadErr)
			}
		})
	}
}

func TestRepositorySinkUsesAuthoritativeClock(t *testing.T) {
	receivedAt := time.Date(2026, time.July, 27, 10, 0, 2, 0, time.UTC)
	repository := &repositoryStub{}
	sink, err := NewRepositorySink(repository, clock.NewFake(receivedAt))
	if err != nil {
		t.Fatal(err)
	}
	err = sink.Report(context.Background(), Payload{
		ServiceKind: operations.ServiceAPI, InstanceID: "api-1", BuildVersion: "test", StartedAt: receivedAt.Add(-time.Second),
		Status: operations.HealthHealthy, Components: map[string]operations.HealthStatus{}, MaintenanceVersion: 1,
	})
	if err != nil || !repository.instance.LastHeartbeatAt.Equal(receivedAt) {
		t.Fatalf("instance=%+v err=%v", repository.instance, err)
	}
}

func mapsClone(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
