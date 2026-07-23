package server

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/edge/internal/config"
)

func TestStaticRoutingAndFallback(t *testing.T) {
	handler := newTestHandler(t, nil, nil, map[string]string{
		"index.html": "INDEX",
		"asset.txt":  "ASSET",
	})
	t.Run("asset", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/asset.txt", nil)
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "ASSET" {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("fallback", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/rooms/123", nil)
		req.Header.Set("Accept", "text/html")
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "INDEX" {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("json-no-fallback", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/rooms/123", nil)
		req.Header.Set("Accept", "application/json")
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "INDEX") {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
	t.Run("path-traversal-blocked", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/../outside.txt", nil)
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
}

func TestRouteIsolation(t *testing.T) {
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(apiUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, apiUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	for _, path := range []string{"/health", "/health/livez", "/realtime", "/realtime/other", "/platform", "/platformx"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", path, rr.Code)
		}
	}
}

func TestAPIProxyRebuildsForwardingHeadersForUntrustedPeer(t *testing.T) {
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Fatalf("method=%s", got)
		}
		if got, want := r.URL.Path, "/platform.game.v1.GameService/Play"; got != want {
			t.Fatalf("path=%s", got)
		}
		if got, want := r.URL.RawQuery, "x=1"; got != want {
			t.Fatalf("query=%s", got)
		}
		if got, want := r.Header.Get("X-Forwarded-For"), "203.0.113.10"; got != want {
			t.Fatalf("xff=%q", got)
		}
		if got, want := r.Header.Get("X-Real-IP"), "203.0.113.10"; got != want {
			t.Fatalf("xreal=%q", got)
		}
		if got := r.Header.Get("Forwarded"); !strings.Contains(got, "203.0.113.10") {
			t.Fatalf("forwarded=%q", got)
		}
		if got, want := r.Host, "game.example.test"; got != want {
			t.Fatalf("host=%q, want %q", got, want)
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api"))
	}))
	t.Cleanup(apiUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, apiUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/platform.game.v1.GameService/Play?x=1", strings.NewReader("body"))
	req.RemoteAddr = "203.0.113.10:12345"
	req.Host = "game.example.test"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	if rr.Body.String() != "api" {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func TestAPIProxyPreservesTrustedForwardingChain(t *testing.T) {
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Forwarded-For"); !strings.Contains(got, "1.2.3.4") || !strings.Contains(got, "127.0.0.1") {
			t.Fatalf("xff=%q", got)
		}
		if got := r.Header.Get("Forwarded"); !strings.Contains(got, "1.2.3.4") || !strings.Contains(got, "127.0.0.1") {
			t.Fatalf("forwarded=%q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, apiUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/platform.room.v1.RoomService/Join", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestHealthReadyChecksBothUpstreamsConcurrently(t *testing.T) {
	apiStarted := make(chan struct{}, 1)
	realtimeStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Fatalf("api path=%s", r.URL.Path)
		}
		apiStarted <- struct{}{}
		<-release
		// API readiness returns a JSON 200 response, while realtime readiness returns 204.
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiUpstream.Close)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			t.Fatalf("realtime path=%s", r.URL.Path)
		}
		realtimeStarted <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(realtimeUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, realtimeUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rr, req)
		close(done)
	}()
	waitForTwo(t, apiStarted, realtimeStarted)
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ready handler timed out")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestHealthReadyHidesFailures(t *testing.T) {
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(apiUpstream.Close)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(realtimeUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, realtimeUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("leaked body=%q", rr.Body.String())
	}
}

func TestRealtimeGameWebSocketUpgrade(t *testing.T) {
	upgradeSeen := make(chan http.Header, 1)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgradeSeen <- r.Header.Clone()
		if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("missing websocket headers: %#v", r.Header)
		}
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "websocket")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	t.Cleanup(realtimeUpstream.Close)
	handler := newTestHandler(t, mustURL(t, realtimeUpstream.URL), mustURL(t, realtimeUpstream.URL), map[string]string{
		"index.html": "INDEX",
	})
	edge := httptest.NewServer(handler)
	t.Cleanup(edge.Close)
	conn, err := net.Dial("tcp", strings.TrimPrefix(edge.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /realtime/game HTTP/1.1\r\nHost: edge\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	select {
	case <-upgradeSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive websocket upgrade")
	}
}

func newTestHandler(t *testing.T, apiURL, realtimeURL *url.URL, files map[string]string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := files["index.html"]; !ok {
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("INDEX"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		ListenAddress:       ":0",
		APIUpstreamURL:      apiURL,
		RealtimeUpstreamURL: realtimeURL,
		StaticDirectory:     dir,
		TrustedProxyCIDRs: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"),
		},
		ShutdownTimeout:            2 * time.Second,
		ReadHeaderTimeout:          time.Second,
		ProxyDialTimeout:           time.Second,
		ProxyTLSHandshakeTimeout:   time.Second,
		ProxyResponseHeaderTimeout: time.Second,
		HealthTimeout:              500 * time.Millisecond,
	}
	if apiURL == nil {
		cfg.APIUpstreamURL = mustURL(t, "http://127.0.0.1:1")
	}
	if realtimeURL == nil {
		cfg.RealtimeUpstreamURL = mustURL(t, "http://127.0.0.1:1")
	}
	handler, err := NewHandler(cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func waitForTwo(t *testing.T, left, right <-chan struct{}) {
	t.Helper()
	seen := 0
	deadline := time.After(2 * time.Second)
	for seen < 2 {
		select {
		case <-left:
			seen++
			left = nil
		case <-right:
			seen++
			right = nil
		case <-deadline:
			t.Fatal("upstreams did not start concurrently")
		}
	}
}
