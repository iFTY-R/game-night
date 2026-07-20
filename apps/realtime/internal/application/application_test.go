package application

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicMuxKeepsWebSocketRouteExact(t *testing.T) {
	websocketCalls := 0
	websocketHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		websocketCalls++
		response.WriteHeader(http.StatusNoContent)
	})
	readyHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux := newPublicMux(websocketHandler, readyHandler)

	for _, test := range []struct {
		method string
		path   string
		status int
		calls  int
	}{
		{method: http.MethodGet, path: "/realtime/game", status: http.StatusNoContent, calls: 1},
		{method: http.MethodPost, path: "/realtime/game", status: http.StatusMethodNotAllowed, calls: 1},
		{method: http.MethodGet, path: "/realtime/game/other", status: http.StatusNotFound, calls: 1},
		{method: http.MethodGet, path: "/health/live", status: http.StatusNoContent, calls: 1},
		{method: http.MethodGet, path: "/health/ready", status: http.StatusNoContent, calls: 1},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != test.status || websocketCalls != test.calls {
			t.Fatalf("%s %s status=%d calls=%d", test.method, test.path, response.Code, websocketCalls)
		}
	}
}
