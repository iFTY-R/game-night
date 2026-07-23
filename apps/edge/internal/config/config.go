// Package config loads the edge gateway process configuration without touching the network.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	listenAddressEnvironment       = "GAME_NIGHT_EDGE_LISTEN_ADDRESS"
	apiUpstreamURLEnvironment      = "GAME_NIGHT_EDGE_API_UPSTREAM_URL"
	realtimeUpstreamURLEnvironment = "GAME_NIGHT_EDGE_REALTIME_UPSTREAM_URL"
	staticDirectoryEnvironment     = "GAME_NIGHT_EDGE_STATIC_DIRECTORY"
	trustedProxyCIDRsEnvironment   = "GAME_NIGHT_EDGE_TRUSTED_PROXY_CIDRS"
	defaultListenAddress           = ":8080"
	defaultAPIUpstreamURL          = "http://127.0.0.1:8081"
	defaultRealtimeUpstreamURL     = "http://127.0.0.1:8090"
	defaultStaticDirectory         = "/app/web"
	// Only local loopback is trusted by default; deployments must explicitly name their proxy networks.
	defaultTrustedProxyCIDRs          = "127.0.0.1/32,::1/128"
	defaultShutdownTimeout            = 15 * time.Second
	defaultReadHeaderTimeout          = 5 * time.Second
	defaultProxyDialTimeout           = 5 * time.Second
	defaultProxyTLSHandshakeTimeout   = 5 * time.Second
	defaultProxyResponseHeaderTimeout = 30 * time.Second
	defaultHealthTimeout              = 2 * time.Second
	staticIndexFileName               = "index.html"
)

// LookupEnv matches os.LookupEnv so tests can inject a fixed environment.
type LookupEnv func(string) (string, bool)

// Config contains only the validated, process-local edge gateway settings.
type Config struct {
	ListenAddress              string
	APIUpstreamURL             *url.URL
	RealtimeUpstreamURL        *url.URL
	StaticDirectory            string
	TrustedProxyCIDRs          []netip.Prefix
	ShutdownTimeout            time.Duration
	ReadHeaderTimeout          time.Duration
	ProxyDialTimeout           time.Duration
	ProxyTLSHandshakeTimeout   time.Duration
	ProxyResponseHeaderTimeout time.Duration
	HealthTimeout              time.Duration
}

// Load validates URLs, CIDRs, and the static asset directory before the server starts.
func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("lookup env: required")
	}
	listenAddress := valueOrDefault(lookup, listenAddressEnvironment, defaultListenAddress)
	if !validListenAddress(listenAddress) {
		return Config{}, fieldError(listenAddressEnvironment, "invalid listen address")
	}
	apiUpstreamURL, err := parseUpstreamURL(lookup, apiUpstreamURLEnvironment, defaultAPIUpstreamURL)
	if err != nil {
		return Config{}, err
	}
	realtimeUpstreamURL, err := parseUpstreamURL(lookup, realtimeUpstreamURLEnvironment, defaultRealtimeUpstreamURL)
	if err != nil {
		return Config{}, err
	}
	staticDirectory, err := parseStaticDirectory(valueOrDefault(lookup, staticDirectoryEnvironment, defaultStaticDirectory))
	if err != nil {
		return Config{}, err
	}
	trustedProxyCIDRs, err := parseTrustedProxyCIDRs(valueOrDefault(lookup, trustedProxyCIDRsEnvironment, defaultTrustedProxyCIDRs))
	if err != nil {
		return Config{}, err
	}
	return Config{
		ListenAddress:              listenAddress,
		APIUpstreamURL:             apiUpstreamURL,
		RealtimeUpstreamURL:        realtimeUpstreamURL,
		StaticDirectory:            staticDirectory,
		TrustedProxyCIDRs:          trustedProxyCIDRs,
		ShutdownTimeout:            defaultShutdownTimeout,
		ReadHeaderTimeout:          defaultReadHeaderTimeout,
		ProxyDialTimeout:           defaultProxyDialTimeout,
		ProxyTLSHandshakeTimeout:   defaultProxyTLSHandshakeTimeout,
		ProxyResponseHeaderTimeout: defaultProxyResponseHeaderTimeout,
		HealthTimeout:              defaultHealthTimeout,
	}, nil
}

func valueOrDefault(lookup LookupEnv, name, fallback string) string {
	if value, ok := lookup(name); ok {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return fallback
}

func parseUpstreamURL(lookup LookupEnv, name, fallback string) (*url.URL, error) {
	raw := valueOrDefault(lookup, name, fallback)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fieldError(name, "invalid upstream URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fieldError(name, "invalid upstream URL")
	}
	return parsed, nil
}

func parseStaticDirectory(raw string) (string, error) {
	if raw == "" {
		return "", fieldError(staticDirectoryEnvironment, "invalid static directory")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fieldError(staticDirectoryEnvironment, "invalid static directory")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fieldError(staticDirectoryEnvironment, "invalid static directory")
	}
	if _, err := os.Stat(filepath.Join(abs, staticIndexFileName)); err != nil {
		return "", fieldError(staticDirectoryEnvironment, "missing static index.html")
	}
	// Keep the caller's absolute path stable; the server resolves both sides when checking symlink boundaries.
	return abs, nil
}

func parseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "invalid proxy CIDR")
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "invalid proxy CIDR")
		}
		prefix = prefix.Masked()
		if !prefix.IsValid() || prefix.Bits() == 0 {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "invalid proxy CIDR")
		}
		key := prefix.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, fieldError(trustedProxyCIDRsEnvironment, "invalid proxy CIDR")
	}
	return prefixes, nil
}

func validListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.ContainsAny(host, " /\\") {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort >= 1 && parsedPort <= 65535
}

func fieldError(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}
