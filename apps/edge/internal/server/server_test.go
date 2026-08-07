package server

import (
	"bufio"
	"context"
	"encoding/json"
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
	"sync"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/edge/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	"github.com/iFTY-R/game-night/platform/admin/operations"
)

const (
	testUserHost  = "localhost:8080"
	testAdminHost = "admin.localhost:8080" // Valid authority; /admin selects the management surface.
)

func TestStaticRoutingUsesSurfaceSpecificRoots(t *testing.T) {
	handler := newTestHandler(t, nil, nil, map[string]string{
		"index.html": "USER-INDEX",
		"asset.txt":  "USER-ASSET",
		"shared.txt": "USER-SHARED",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
		"asset.txt":  "ADMIN-ASSET",
		"shared.txt": "ADMIN-SHARED",
	})

	for _, tc := range []struct {
		name       string
		host       string
		method     string
		target     string
		accept     string
		wantStatus int
		wantBody   string
	}{
		{name: "user-asset", host: testUserHost, method: http.MethodGet, target: "/asset.txt", wantStatus: http.StatusOK, wantBody: "USER-ASSET"},
		{name: "custom-domain-user-asset", host: "play.example.test", method: http.MethodGet, target: "/asset.txt", wantStatus: http.StatusOK, wantBody: "USER-ASSET"},
		{name: "admin-subdomain-root-is-user", host: testAdminHost, method: http.MethodGet, target: "/asset.txt", wantStatus: http.StatusOK, wantBody: "USER-ASSET"},
		{name: "admin-asset", host: testAdminHost, method: http.MethodGet, target: "/admin/asset.txt", wantStatus: http.StatusOK, wantBody: "ADMIN-ASSET"},
		{name: "user-fallback", host: testUserHost, method: http.MethodGet, target: "/rooms/123", accept: "text/html", wantStatus: http.StatusOK, wantBody: "USER-INDEX"},
		{name: "admin-fallback", host: testAdminHost, method: http.MethodGet, target: "/admin/audit/events", accept: "text/html", wantStatus: http.StatusOK, wantBody: "ADMIN-INDEX"},
		{name: "json-no-fallback", host: testUserHost, method: http.MethodGet, target: "/rooms/123", accept: "application/json", wantStatus: http.StatusNotFound},
		{name: "head-fallback", host: testAdminHost, method: http.MethodHead, target: "/admin/dashboard", wantStatus: http.StatusOK},
		{name: "post-no-fallback", host: testUserHost, method: http.MethodPost, target: "/dashboard", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Host = tc.host
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			if tc.method != http.MethodHead && tc.wantBody != "" && rr.Body.String() != tc.wantBody {
				t.Fatalf("body=%q, want %q", rr.Body.String(), tc.wantBody)
			}
			if tc.method == http.MethodHead && rr.Body.Len() != 0 {
				t.Fatalf("head body=%q", rr.Body.String())
			}
		})
	}
}

func TestInvalidHostReturnsMisdirectedRequestBeforeProxyOrStatic(t *testing.T) {
	apiHits := 0
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(apiUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, apiUpstream.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	for _, target := range []string{"/health/live", "/platform.identity.v1.IdentityService/Bootstrap", "/readyz", "/"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = "invalid host"
		req.Header.Set("Accept", "text/html")
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusMisdirectedRequest {
			t.Fatalf("target=%s status=%d", target, rr.Code)
		}
	}
	if apiHits != 0 {
		t.Fatalf("invalid host reached upstream %d times", apiHits)
	}
}

func TestRouteIsolationByHostMethodAndPath(t *testing.T) {
	api := startCapturedUpstream(t, "api")
	realtime := startCapturedUpstream(t, "realtime")
	handler := newTestHandler(t, mustURL(t, api.server.URL), mustURL(t, realtime.server.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	for _, tc := range []struct {
		name         string
		host         string
		method       string
		target       string
		accept       string
		wantStatus   int
		wantBody     string
		wantUpstream string
	}{
		{name: "user-identity-rpc", host: testUserHost, method: http.MethodPost, target: "/platform.identity.v1.IdentityService/Bootstrap", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "user-room-rpc", host: testUserHost, method: http.MethodPost, target: "/platform.room.v1.RoomService/Join", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "user-game-rpc", host: testUserHost, method: http.MethodPost, target: "/platform.game.v1.GameService/GetProjection", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "user-admin-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminAuthService/GetCurrentAdminSession", wantStatus: http.StatusNotFound},
		{name: "user-admin-user-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminUserService/ListUsers", wantStatus: http.StatusNotFound},
		{name: "user-admin-room-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminRoomService/ListRooms", wantStatus: http.StatusNotFound},
		{name: "user-admin-audit-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminAuditService/ListAuditEvents", wantStatus: http.StatusNotFound},
		{name: "user-admin-operations-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminOperationsService/GetOperationsSnapshot", wantStatus: http.StatusNotFound},
		{name: "user-admin-overview-rpc-404", host: testUserHost, method: http.MethodPost, target: "/platform.admin.v1.AdminOverviewService/GetOverview", wantStatus: http.StatusNotFound},
		{name: "user-readiness-404", host: testUserHost, method: http.MethodGet, target: "/readyz", wantStatus: http.StatusNotFound},
		{name: "user-realtime", host: testUserHost, method: http.MethodGet, target: "/realtime/game", wantStatus: http.StatusOK, wantBody: "realtime", wantUpstream: "realtime"},
		{name: "user-realtime-other-404", host: testUserHost, method: http.MethodGet, target: "/realtime/other", wantStatus: http.StatusNotFound},
		{name: "admin-auth-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminAuthService/GetCurrentAdminSession", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-runtime-readiness-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminAuthService/GetRuntimeReadiness", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-preview-revoke-other-sessions-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-user-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminUserService/ListUsers", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-room-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminRoomService/ListRooms", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-audit-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminAuditService/ListAuditEvents", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-operations-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminOperationsService/GetOperationsSnapshot", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-overview-rpc", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminOverviewService/GetOverview", wantStatus: http.StatusOK, wantBody: "api", wantUpstream: "api"},
		{name: "admin-identity-rpc-404", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.admin.v1.AdminIdentityService/ListAuditEvents", wantStatus: http.StatusNotFound},
		{name: "admin-user-rpc-404", host: testAdminHost, method: http.MethodPost, target: "/admin/platform.identity.v1.IdentityService/Bootstrap", wantStatus: http.StatusNotFound},
		{name: "admin-realtime-404", host: testAdminHost, method: http.MethodGet, target: "/admin/realtime/game", wantStatus: http.StatusNotFound},
		{name: "admin-readyz-404", host: testAdminHost, method: http.MethodGet, target: "/admin/readyz", wantStatus: http.StatusNotFound},
		{name: "admin-sensitive-readyz-404", host: testAdminHost, method: http.MethodHead, target: "/admin/readyz/sensitive", wantStatus: http.StatusNotFound},
		{name: "known-platform-miss", host: testAdminHost, method: http.MethodGet, target: "/admin/platform.example", wantStatus: http.StatusNotFound},
		{name: "known-health-miss", host: testUserHost, method: http.MethodGet, target: "/health/not-real", wantStatus: http.StatusNotFound},
		{name: "user-private-heartbeat-404", host: testUserHost, method: http.MethodPost, target: "/internal/admin/operations/heartbeat", wantStatus: http.StatusNotFound},
		{name: "admin-private-heartbeat-404", host: testAdminHost, method: http.MethodPost, target: "/admin/internal/admin/operations/heartbeat", wantStatus: http.StatusNotFound},
		{name: "known-non-get-route", host: testAdminHost, method: http.MethodPatch, target: "/admin/dashboard", wantStatus: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.Host = tc.host
			req.Header.Set("Accept", "text/html")
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
			}
			if tc.method != http.MethodHead && tc.wantBody != "" && rr.Body.String() != tc.wantBody {
				t.Fatalf("body=%q, want %q", rr.Body.String(), tc.wantBody)
			}
			if tc.wantUpstream == "" {
				return
			}
			request := lastRecordedRequest(t, upstreamByName(tc.wantUpstream, api, realtime))
			if request.Host != tc.host {
				t.Fatalf("upstream host=%q, want %q", request.Host, tc.host)
			}
			wantPath := tc.target
			if strings.HasPrefix(wantPath, "/admin/") {
				wantPath = strings.TrimPrefix(wantPath, "/admin")
			}
			if request.Path != wantPath {
				t.Fatalf("upstream path=%q, want %q", request.Path, wantPath)
			}
		})
	}
}

func TestSurfaceSelectionIgnoresForwardedHost(t *testing.T) {
	api := startCapturedUpstream(t, "api")
	handler := newTestHandler(t, mustURL(t, api.server.URL), mustURL(t, api.server.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Host = testUserHost
	req.Header.Set("X-Forwarded-Host", testAdminHost)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/platform.admin.v1.AdminAuthService/GetCurrentAdminSession", strings.NewReader("{}"))
	req.Host = testAdminHost
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("X-Forwarded-Host", testUserHost)
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	req.Header.Set("Forwarded", "for=198.51.100.1;host=attacker.invalid;proto=http")
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	request := lastRecordedRequest(t, api)
	if got, want := request.Host, testAdminHost; got != want {
		t.Fatalf("host=%q, want %q", got, want)
	}
	if got, want := request.XForwardedHost, testAdminHost; got != want {
		t.Fatalf("xfh=%q, want %q", got, want)
	}
}

func TestAPIProxyRebuildsForwardingHeadersForUntrustedPeer(t *testing.T) {
	api := startCapturedUpstream(t, "api")
	handler := newTestHandler(t, mustURL(t, api.server.URL), mustURL(t, api.server.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/platform.game.v1.GameService/Play?x=1", strings.NewReader("body"))
	req.RemoteAddr = "203.0.113.10:12345"
	req.Host = testUserHost
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control=%q", got)
	}
	request := lastRecordedRequest(t, api)
	if got, want := request.Method, http.MethodPost; got != want {
		t.Fatalf("method=%s", got)
	}
	if got, want := request.Path, "/platform.game.v1.GameService/Play"; got != want {
		t.Fatalf("path=%s", got)
	}
	if got, want := request.RawQuery, "x=1"; got != want {
		t.Fatalf("query=%s", got)
	}
	if got, want := request.XForwardedFor, "203.0.113.10"; got != want {
		t.Fatalf("xff=%q", got)
	}
	if got, want := request.XRealIP, "203.0.113.10"; got != want {
		t.Fatalf("xreal=%q", got)
	}
	if got, want := request.XForwardedHost, testUserHost; got != want {
		t.Fatalf("xfh=%q", got)
	}
	if got := request.Forwarded; !strings.Contains(got, "203.0.113.10") || !strings.Contains(got, "host="+testUserHost) {
		t.Fatalf("forwarded=%q", got)
	}
}

func TestAPIProxyPreservesTrustedForwardingChain(t *testing.T) {
	api := startCapturedUpstream(t, "api")
	handler := newTestHandler(t, mustURL(t, api.server.URL), mustURL(t, api.server.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/platform.room.v1.RoomService/Join", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = testUserHost
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	request := lastRecordedRequest(t, api)
	if !strings.Contains(request.XForwardedFor, "1.2.3.4") || !strings.Contains(request.XForwardedFor, "127.0.0.1") {
		t.Fatalf("xff=%q", request.XForwardedFor)
	}
	if !strings.Contains(request.Forwarded, "1.2.3.4") || !strings.Contains(request.Forwarded, "127.0.0.1") {
		t.Fatalf("forwarded=%q", request.Forwarded)
	}
}

func TestHealthReadyChecksBothUpstreamsConcurrently(t *testing.T) {
	apiStarted := make(chan struct{}, 1)
	realtimeStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != adminReadyPath {
			t.Fatalf("api path=%s", r.URL.Path)
		}
		apiStarted <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(apiUpstream.Close)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthReadyPath {
			t.Fatalf("realtime path=%s", r.URL.Path)
		}
		realtimeStarted <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(realtimeUpstream.Close)
	handler := newTestHandler(t, mustURL(t, apiUpstream.URL), mustURL(t, realtimeUpstream.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, healthReadyPath, nil)
	req.Host = testUserHost
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
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, healthReadyPath, nil)
	req.Host = testAdminHost
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("leaked body=%q", rr.Body.String())
	}
}

func TestRunStartsAndStopsHeartbeatReporter(t *testing.T) {
	apiUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != adminReadyPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(apiUpstream.Close)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthReadyPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(realtimeUpstream.Close)
	heartbeatToken := strings.Repeat("h", 32)
	received := make(chan recordedHeartbeat, 4)
	heartbeatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := recordedHeartbeat{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
		}
		record.decodeErr = json.NewDecoder(r.Body).Decode(&record.payload)
		_ = r.Body.Close()
		received <- record
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(heartbeatServer.Close)
	cfg := config.Config{
		ListenAddress:       "127.0.0.1:0",
		APIUpstreamURL:      mustURL(t, apiUpstream.URL),
		RealtimeUpstreamURL: mustURL(t, realtimeUpstream.URL),
		UserStaticDirectory: writeStaticRoot(t, map[string]string{"index.html": "USER-INDEX"}),
		AdminStaticDirectory: writeStaticRoot(t, map[string]string{
			"index.html": "ADMIN-INDEX",
		}),
		TrustedProxyCIDRs:          []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		ShutdownTimeout:            2 * time.Second,
		ReadHeaderTimeout:          time.Second,
		ProxyDialTimeout:           time.Second,
		ProxyTLSHandshakeTimeout:   time.Second,
		ProxyResponseHeaderTimeout: time.Second,
		HealthTimeout:              500 * time.Millisecond,
		InstanceID:                 "edge-run-test",
		Heartbeat: serviceheartbeat.Config{
			TargetURL:    heartbeatServer.URL + serviceheartbeat.Path,
			Token:        heartbeatToken,
			BuildVersion: "edge-build",
			Interval:     time.Second,
			Timeout:      300 * time.Millisecond,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, cfg, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()

	first := waitForHeartbeat(t, received)
	if first.decodeErr != nil {
		t.Fatalf("decode err=%v", first.decodeErr)
	}
	if first.method != http.MethodPost || first.path != serviceheartbeat.Path {
		t.Fatalf("heartbeat request=%s %s", first.method, first.path)
	}
	if first.authorization != "Bearer "+heartbeatToken || first.contentType != "application/json" {
		t.Fatalf("heartbeat headers auth=%q content-type=%q", first.authorization, first.contentType)
	}
	if first.payload.ServiceKind != operations.ServiceEdge || first.payload.InstanceID != cfg.InstanceID || first.payload.BuildVersion != cfg.Heartbeat.BuildVersion {
		t.Fatalf("heartbeat payload identity=%+v", first.payload)
	}
	if first.payload.Status != operations.HealthDegraded {
		t.Fatalf("heartbeat status=%s", first.payload.Status)
	}
	if first.payload.Components[heartbeatComponentAPIUpstream] != operations.HealthUnavailable ||
		first.payload.Components[heartbeatComponentRealtimeUpstream] != operations.HealthHealthy {
		t.Fatalf("heartbeat components=%v", first.payload.Components)
	}
	if first.payload.MaintenanceVersion != initialMaintenanceVersion {
		t.Fatalf("maintenance version=%d", first.payload.MaintenanceVersion)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}

	final := waitForHeartbeat(t, received)
	if final.decodeErr != nil {
		t.Fatalf("final decode err=%v", final.decodeErr)
	}
	if !final.payload.StartedAt.Equal(first.payload.StartedAt) {
		t.Fatalf("started_at drifted: first=%s final=%s", first.payload.StartedAt, final.payload.StartedAt)
	}
}

func TestRealtimeGameWebSocketUpgrade(t *testing.T) {
	upgradeSeen := make(chan recordedRequest, 1)
	realtimeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgradeSeen <- captureRequest(r)
		if !strings.EqualFold(r.Header.Get("Connection"), "Upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("missing websocket headers: %#v", r.Header)
		}
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "websocket")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	t.Cleanup(realtimeUpstream.Close)
	handler := newTestHandler(t, mustURL(t, realtimeUpstream.URL), mustURL(t, realtimeUpstream.URL), map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})
	edge := httptest.NewServer(handler)
	t.Cleanup(edge.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(edge.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn,
		"GET /realtime/game HTTP/1.1\r\nHost: "+testUserHost+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	select {
	case request := <-upgradeSeen:
		if request.Host != testUserHost {
			t.Fatalf("upstream host=%q", request.Host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive websocket upgrade")
	}
}

func TestStaticRoutingBlocksTraversalAndSymlinkEscape(t *testing.T) {
	handler, roots := newTestHandlerWithRoots(t, nil, nil, map[string]string{
		"index.html": "USER-INDEX",
	}, map[string]string{
		"index.html": "ADMIN-INDEX",
	})
	assertNotFound := func(t *testing.T, target string) {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = testUserHost
		req.Header.Set("Accept", "text/html")
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
		}
	}

	t.Run("path-traversal", func(t *testing.T) {
		assertNotFound(t, "/../outside.txt")
	})
	t.Run("symlink-escape", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(roots.user, "escape.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertNotFound(t, "/escape.txt")
	})
}

type testRoots struct {
	user  string
	admin string
}

func newTestHandler(t *testing.T, apiURL, realtimeURL *url.URL, userFiles, adminFiles map[string]string) http.Handler {
	t.Helper()
	handler, _ := newTestHandlerWithRoots(t, apiURL, realtimeURL, userFiles, adminFiles)
	return handler
}

func newTestHandlerWithRoots(t *testing.T, apiURL, realtimeURL *url.URL, userFiles, adminFiles map[string]string) (http.Handler, testRoots) {
	t.Helper()
	userDir := writeStaticRoot(t, userFiles)
	adminDir := writeStaticRoot(t, adminFiles)
	cfg := config.Config{
		ListenAddress:        ":0",
		APIUpstreamURL:       apiURL,
		RealtimeUpstreamURL:  realtimeURL,
		UserStaticDirectory:  userDir,
		AdminStaticDirectory: adminDir,
		TrustedProxyCIDRs: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"),
		},
		ShutdownTimeout:            2 * time.Second,
		ReadHeaderTimeout:          time.Second,
		ProxyDialTimeout:           time.Second,
		ProxyTLSHandshakeTimeout:   time.Second,
		ProxyResponseHeaderTimeout: time.Second,
		HealthTimeout:              500 * time.Millisecond,
		InstanceID:                 "edge-test",
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
	return handler, testRoots{user: userDir, admin: adminDir}
}

func writeStaticRoot(t testing.TB, files map[string]string) string {
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
	return dir
}

type recordedRequest struct {
	Method         string
	Path           string
	RawQuery       string
	Host           string
	Forwarded      string
	XForwardedFor  string
	XForwardedHost string
	XRealIP        string
}

type recordedHeartbeat struct {
	method        string
	path          string
	authorization string
	contentType   string
	payload       serviceheartbeat.Payload
	decodeErr     error
}

type capturedUpstream struct {
	name     string
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func startCapturedUpstream(t testing.TB, name string) *capturedUpstream {
	t.Helper()
	upstream := &capturedUpstream{name: name}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.mu.Lock()
		upstream.requests = append(upstream.requests, captureRequest(r))
		upstream.mu.Unlock()
		w.Header().Set("X-Upstream", name)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(name))
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func captureRequest(r *http.Request) recordedRequest {
	return recordedRequest{
		Method:         r.Method,
		Path:           r.URL.Path,
		RawQuery:       r.URL.RawQuery,
		Host:           r.Host,
		Forwarded:      r.Header.Get("Forwarded"),
		XForwardedFor:  r.Header.Get("X-Forwarded-For"),
		XForwardedHost: r.Header.Get("X-Forwarded-Host"),
		XRealIP:        r.Header.Get("X-Real-IP"),
	}
}

func lastRecordedRequest(t testing.TB, upstream *capturedUpstream) recordedRequest {
	t.Helper()
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) == 0 {
		t.Fatalf("upstream %s received no request", upstream.name)
	}
	return upstream.requests[len(upstream.requests)-1]
}

func upstreamByName(name string, upstreams ...*capturedUpstream) *capturedUpstream {
	for _, upstream := range upstreams {
		if upstream != nil && upstream.name == name {
			return upstream
		}
	}
	return nil
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

func waitForHeartbeat(t *testing.T, heartbeats <-chan recordedHeartbeat) recordedHeartbeat {
	t.Helper()
	select {
	case heartbeat := <-heartbeats:
		return heartbeat
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat was not received")
		return recordedHeartbeat{}
	}
}
