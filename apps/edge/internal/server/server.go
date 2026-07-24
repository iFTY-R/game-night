// Package server owns the edge gateway HTTP surface and lifecycle.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/edge/internal/config"
)

const (
	healthLivePath          = "/health/live"
	healthReadyPath         = "/health/ready"
	adminReadyPath          = "/readyz"
	adminSensitiveReadyPath = "/readyz/sensitive"
	realtimeGamePath        = "/realtime/game"
	staticIndexName         = "index.html"
)

var (
	errInvalidServer = errors.New("invalid edge server configuration")
)

type surfaceClass uint8

const (
	unknownSurface surfaceClass = iota
	userSurface
	adminSurface
)

// Run loads the handler, starts the listener, and shuts down gracefully on cancellation.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if ctx == nil || logger == nil {
		return errInvalidServer
	}
	handler, err := NewHandler(cfg, logger)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()
	logger.Info(
		"edge listening",
		"address", cfg.ListenAddress,
		"user_static_directory", cfg.UserStaticDirectory,
		"admin_static_directory", cfg.AdminStaticDirectory,
	)
	select {
	case serveErr := <-serveErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if serveErr := <-serveErrors; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		logger.Info("edge stopped")
		return nil
	}
}

// NewHandler constructs the single public edge HTTP surface.
func NewHandler(cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	if logger == nil || cfg.APIUpstreamURL == nil || cfg.RealtimeUpstreamURL == nil || cfg.UserStaticDirectory == "" ||
		cfg.AdminStaticDirectory == "" || len(cfg.UserHosts) == 0 || len(cfg.AdminHosts) == 0 || len(cfg.TrustedProxyCIDRs) == 0 {
		return nil, errInvalidServer
	}
	return &handler{
		logger:        logger,
		cfg:           cfg,
		userHosts:     hostSet(cfg.UserHosts),
		adminHosts:    hostSet(cfg.AdminHosts),
		apiProxy:      newProxy(cfg.APIUpstreamURL, cfg.TrustedProxyCIDRs, logger, "api", true, cfg),
		realtimeProxy: newProxy(cfg.RealtimeUpstreamURL, cfg.TrustedProxyCIDRs, logger, "realtime", false, cfg),
		healthClient: &http.Client{
			Timeout: cfg.HealthTimeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: cfg.ProxyDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   cfg.ProxyTLSHandshakeTimeout,
				ResponseHeaderTimeout: cfg.ProxyResponseHeaderTimeout,
				ExpectContinueTimeout: 1 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   8,
			},
		},
	}, nil
}

type handler struct {
	logger        *slog.Logger
	cfg           config.Config
	userHosts     map[string]struct{}
	adminHosts    map[string]struct{}
	apiProxy      *httputil.ReverseProxy
	realtimeProxy *httputil.ReverseProxy
	healthClient  *http.Client
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	// Host classification is the first boundary so unknown authorities never reach health checks, proxies, or SPA fallbacks.
	surface := h.surfaceForHost(request.Host)
	switch surface {
	case unknownSurface:
		writer.WriteHeader(http.StatusMisdirectedRequest)
		return
	case userSurface, adminSurface:
	}

	switch {
	case request.Method == http.MethodGet && request.URL.Path == healthLivePath:
		writer.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodGet && request.URL.Path == healthReadyPath:
		h.handleReady(writer, request)
	case surface == userSurface:
		h.serveUserSurface(writer, request)
	case surface == adminSurface:
		h.serveAdminSurface(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (h *handler) handleReady(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), h.cfg.HealthTimeout)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- h.checkReady(ctx, h.cfg.APIUpstreamURL, "/readyz") }()
	go func() { results <- h.checkReady(ctx, h.cfg.RealtimeUpstreamURL, "/health/ready") }()
	for checked := 0; checked < 2; checked++ {
		select {
		case <-ctx.Done():
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		case err := <-results:
			if err != nil {
				cancel()
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *handler) checkReady(ctx context.Context, base *url.URL, suffix string) error {
	target := joinURL(base, suffix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := h.healthClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (h *handler) serveUserSurface(writer http.ResponseWriter, request *http.Request) {
	switch {
	case isUserRPCPath(request.URL.Path):
		h.apiProxy.ServeHTTP(writer, request)
	case strings.HasPrefix(request.URL.Path, "/platform"):
		http.NotFound(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == realtimeGamePath:
		h.realtimeProxy.ServeHTTP(writer, request)
	case strings.HasPrefix(request.URL.Path, "/realtime"):
		http.NotFound(writer, request)
	case request.URL.Path == adminReadyPath || request.URL.Path == adminSensitiveReadyPath:
		http.NotFound(writer, request)
	case strings.HasPrefix(request.URL.Path, "/health"):
		http.NotFound(writer, request)
	case request.Method == http.MethodGet || request.Method == http.MethodHead:
		h.serveStatic(writer, request, h.cfg.UserStaticDirectory)
	default:
		http.NotFound(writer, request)
	}
}

func (h *handler) serveAdminSurface(writer http.ResponseWriter, request *http.Request) {
	switch {
	case isAdminRPCPath(request.URL.Path):
		h.apiProxy.ServeHTTP(writer, request)
	case request.URL.Path == adminReadyPath || request.URL.Path == adminSensitiveReadyPath:
		http.NotFound(writer, request)
	case strings.HasPrefix(request.URL.Path, "/platform") || strings.HasPrefix(request.URL.Path, "/realtime") || strings.HasPrefix(request.URL.Path, "/health"):
		http.NotFound(writer, request)
	case request.Method == http.MethodGet || request.Method == http.MethodHead:
		h.serveStatic(writer, request, h.cfg.AdminStaticDirectory)
	default:
		http.NotFound(writer, request)
	}
}

func (h *handler) serveStatic(writer http.ResponseWriter, request *http.Request, root string) {
	relative, ok := cleanRequestPath(request.URL.Path)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if served := h.tryServeResolvedPath(writer, request, root, relative); served {
		return
	}
	if request.Method == http.MethodHead || acceptsHTML(request.Header.Get("Accept")) {
		if h.tryServeIndex(writer, request, root) {
			return
		}
	}
	http.NotFound(writer, request)
}

func (h *handler) tryServeIndex(writer http.ResponseWriter, request *http.Request, root string) bool {
	return h.tryServeResolvedPath(writer, request, root, staticIndexName)
}

func (h *handler) tryServeResolvedPath(writer http.ResponseWriter, request *http.Request, root, relative string) bool {
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	// Reject lexical traversal before touching the filesystem, then re-check after resolving symlinks.
	if !withinRoot(root, candidate) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	if !withinRoot(root, resolved) {
		return false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return h.tryServeResolvedPath(writer, request, root, filepath.Join(relative, staticIndexName))
	}
	http.ServeFile(writer, request, resolved)
	return true
}

func newProxy(target *url.URL, trustedCIDRs []netip.Prefix, logger *slog.Logger, name string, noStore bool, cfg config.Config) *httputil.ReverseProxy {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ProxyDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   cfg.ProxyTLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ProxyResponseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
	}
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			inbound := proxyRequest.In
			request := proxyRequest.Out
			inboundHost := inbound.Host
			inboundProto := "http"
			if inbound.TLS != nil {
				inboundProto = "https"
			}
			peerIP, trusted := requestPeer(inbound.RemoteAddr, trustedCIDRs)
			if trusted {
				copyForwardingHeaders(request.Header, inbound.Header)
			}
			applyProxyHeaders(request.Header, trusted, peerIP, inboundHost, inboundProto)
			request.URL.Scheme = target.Scheme
			request.URL.Host = target.Host
			request.URL.Path = joinURLPath(target.Path, request.URL.Path)
			// Keep the public Host so API origin and tenant boundary checks see the browser's authority.
			request.Host = inboundHost
			if request.URL.RawPath != "" {
				request.URL.RawPath = joinRawURLPath(target.Path, request.URL.RawPath)
			}
		},
		ModifyResponse: func(response *http.Response) error {
			if noStore {
				response.Header.Set("Cache-Control", "no-store")
			}
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			if noStore {
				writer.Header().Set("Cache-Control", "no-store")
			}
			logger.Error("edge proxy failed", "upstream", name, "path", request.URL.Path, "error", err.Error())
			writer.WriteHeader(http.StatusBadGateway)
		},
	}
}

func applyProxyHeaders(headers http.Header, trusted bool, peerIP, host, proto string) {
	port := inferPort(host, proto)
	forwardedEntry := buildForwardedEntry(peerIP, host, proto)
	if !trusted {
		headers.Del("Forwarded")
		headers.Del("X-Forwarded-For")
		headers.Del("X-Forwarded-Host")
		headers.Del("X-Forwarded-Proto")
		headers.Del("X-Forwarded-Port")
		headers.Del("X-Real-IP")
		if peerIP != "" {
			headers.Set("X-Forwarded-For", peerIP)
			headers.Set("X-Real-IP", peerIP)
			headers.Set("Forwarded", forwardedEntry)
		}
		headers.Set("X-Forwarded-Host", host)
		headers.Set("X-Forwarded-Proto", proto)
		headers.Set("X-Forwarded-Port", port)
		return
	}
	if peerIP != "" {
		if current := headers.Get("X-Forwarded-For"); current != "" {
			headers.Set("X-Forwarded-For", current+", "+peerIP)
		} else {
			headers.Set("X-Forwarded-For", peerIP)
		}
		if current := headers.Get("Forwarded"); current != "" {
			headers.Set("Forwarded", current+", "+forwardedEntry)
		} else {
			headers.Set("Forwarded", forwardedEntry)
		}
	}
	if headers.Get("X-Forwarded-Host") == "" {
		headers.Set("X-Forwarded-Host", host)
	}
	if headers.Get("X-Forwarded-Proto") == "" {
		headers.Set("X-Forwarded-Proto", proto)
	}
	if headers.Get("X-Forwarded-Port") == "" {
		headers.Set("X-Forwarded-Port", port)
	}
	if headers.Get("X-Real-IP") == "" && peerIP != "" {
		headers.Set("X-Real-IP", peerIP)
	}
}

func (h *handler) surfaceForHost(host string) surfaceClass {
	authority, err := config.CanonicalizeAuthority(host)
	if err != nil {
		return unknownSurface
	}
	if _, ok := h.userHosts[authority]; ok {
		return userSurface
	}
	if _, ok := h.adminHosts[authority]; ok {
		return adminSurface
	}
	return unknownSurface
}

func hostSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func isUserRPCPath(path string) bool {
	return strings.HasPrefix(path, "/platform.identity.v1.IdentityService/") ||
		strings.HasPrefix(path, "/platform.room.v1.RoomService/") ||
		strings.HasPrefix(path, "/platform.game.v1.GameService/")
}

func isAdminRPCPath(path string) bool {
	return strings.HasPrefix(path, "/platform.admin.v1.AdminAuthService/") ||
		strings.HasPrefix(path, "/platform.admin.v1.AdminIdentityService/")
}

func copyForwardingHeaders(destination, source http.Header) {
	for _, name := range []string{
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-IP",
	} {
		if values := source.Values(name); len(values) > 0 {
			destination[name] = append([]string(nil), values...)
		}
	}
}

func requestPeer(remoteAddr string, trustedCIDRs []netip.Prefix) (string, bool) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "", false
	}
	for _, prefix := range trustedCIDRs {
		if prefix.Contains(peer) {
			return peer.String(), true
		}
	}
	return peer.String(), false
}

func buildForwardedEntry(peerIP, host, proto string) string {
	if peerIP == "" {
		return ""
	}
	if strings.Contains(peerIP, ":") && !strings.HasPrefix(peerIP, "[") {
		peerIP = "[" + peerIP + "]"
	}
	return fmt.Sprintf("for=%s;host=%s;proto=%s", peerIP, quoteForwardedValue(host), proto)
}

func quoteForwardedValue(value string) string {
	if value == "" {
		return "\"\""
	}
	if strings.ContainsAny(value, "\";,") {
		return fmt.Sprintf("%q", value)
	}
	return value
}

func inferPort(host, proto string) string {
	if _, port, err := net.SplitHostPort(host); err == nil && port != "" {
		return port
	}
	if proto == "https" {
		return "443"
	}
	return "80"
}

func cleanRequestPath(requestPath string) (string, bool) {
	cleaned := path.Clean("/" + requestPath)
	if cleaned == "." || cleaned == "/" {
		return "", true
	}
	if !strings.HasPrefix(cleaned, "/") {
		return "", false
	}
	relative := strings.TrimPrefix(cleaned, "/")
	if relative == "" {
		return "", true
	}
	return relative, true
}

func withinRoot(root, candidate string) bool {
	root = normalizePath(root)
	candidate = normalizePath(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." {
		return err == nil
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func normalizePath(value string) string {
	if abs, err := filepath.Abs(value); err == nil {
		value = abs
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		value = resolved
	}
	value = filepath.Clean(value)
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func acceptsHTML(value string) bool {
	return strings.Contains(value, "text/html") || strings.Contains(value, "application/xhtml+xml")
}

func joinURL(base *url.URL, suffix string) *url.URL {
	cloned := *base
	cloned.Path = joinURLPath(base.Path, suffix)
	cloned.RawPath = joinRawURLPath(base.Path, suffix)
	cloned.RawQuery = ""
	cloned.Fragment = ""
	return &cloned
}

func joinURLPath(prefix, suffix string) string {
	if prefix == "" {
		prefix = "/"
	}
	return path.Clean(path.Join(prefix, suffix))
}

func joinRawURLPath(prefix, suffix string) string {
	if prefix == "" {
		prefix = "/"
	}
	return path.Clean(path.Join(prefix, suffix))
}
