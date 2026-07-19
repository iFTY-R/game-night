package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
)

func TestRuntimeShutdownDrainsActiveRequestWithCanceledParent(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(testListenerConfig(listener.Addr().String()), handler)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- runtime.Serve(listener) }()

	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach server")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- runtime.Shutdown(canceled) }()
	select {
	case err = <-shutdownDone:
		t.Fatalf("shutdown returned before active request drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err = <-requestDone; err != nil {
		t.Fatalf("active request failed during drain: %v", err)
	}
	if err = <-shutdownDone; err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if err = <-serveDone; err != nil {
		t.Fatalf("serve returned error after shutdown: %v", err)
	}
}

func testListenerConfig(address string) apiConfig.ListenerConfig {
	return apiConfig.ListenerConfig{
		Address: address, ReadHeaderTimeout: time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second,
		ShutdownTimeout: 2 * time.Second, MaxHeaderBytes: 1 << 20,
	}
}
