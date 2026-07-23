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
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/edge/internal/config"
)

const (
	healthLivePath   = "/health/live"
	healthReadyPath  = "/health/ready"
	realtimeGamePath = "/realtime/game"
	platformPrefix   = "/platform."
	staticIndexName  = "index.html"
)

var (
	errInvalidServer = errors.New("invalid edge server configuration")
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
	logger.Info("edge listening", "address", cfg.ListenAddress, "static_directory", cfg.StaticDirectory)
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
	if logger == nil || cfg.APIUpstreamURL == nil || cfg.RealtimeUpstreamURL == nil || cfg.StaticDirectory == "" || len(cfg.TrustedProxyCIDRs) == 0 {
		return nil, errInvalidServer
	}
	return &handler{
		logger:        logger,
		cfg:           cfg,
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
	apiProxy      *httputil.ReverseProxy
	realtimeProxy *httputil.ReverseProxy
	healthClient  *http.Client
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == healthLivePath:
		writer.WriteHeader(http.StatusNoContent)
	case request.Method == http.MethodGet && request.URL.Path == healthReadyPath:
		h.handleReady(writer, request)
	case strings.HasPrefix(request.URL.Path, platformPrefix):
		h.apiProxy.ServeHTTP(writer, request)
	case strings.HasPrefix(request.URL.Path, "/platform"):
		http.NotFound(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == realtimeGamePath:
		h.realtimeProxy.ServeHTTP(writer, request)
	case strings.HasPrefix(request.URL.Path, "/realtime"):
		http.NotFound(writer, request)
	case strings.HasPrefix(request.URL.Path, "/health"):
		http.NotFound(writer, request)
	case request.Method == http.MethodGet || request.Method == http.MethodHead:
		h.serveStatic(writer, request)
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

func (h *handler) serveStatic(writer http.ResponseWriter, request *http.Request) {
	relative, ok := cleanRequestPath(request.URL.Path)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if served := h.tryServeResolvedPath(writer, request, relative); served {
		return
	}
	if acceptsHTML(request.Header.Get("Accept")) {
		if h.tryServeIndex(writer, request) {
			return
		}
		http.NotFound(writer, request)
		return
	}
	http.NotFound(writer, request)
}

func (h *handler) tryServeIndex(writer http.ResponseWriter, request *http.Request) bool {
	return h.tryServeResolvedPath(writer, request, staticIndexName)
}

func (h *handler) tryServeResolvedPath(writer http.ResponseWriter, request *http.Request, relative string) bool {
	candidate := filepath.Join(h.cfg.StaticDirectory, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	if !withinRoot(h.cfg.StaticDirectory, resolved) {
		return false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return h.tryServeResolvedPath(writer, request, filepath.Join(relative, staticIndexName))
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
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." {
		return err == nil
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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
