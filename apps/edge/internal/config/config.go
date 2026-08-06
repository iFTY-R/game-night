// Package config loads the edge gateway process configuration without touching the network.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
)

const (
	listenAddressEnvironment          = "GAME_NIGHT_EDGE_LISTEN_ADDRESS"
	apiUpstreamURLEnvironment         = "GAME_NIGHT_EDGE_API_UPSTREAM_URL"
	realtimeUpstreamURLEnvironment    = "GAME_NIGHT_EDGE_REALTIME_UPSTREAM_URL"
	userStaticDirectoryEnvironment    = "GAME_NIGHT_EDGE_USER_STATIC_DIRECTORY"
	adminStaticDirectoryEnvironment   = "GAME_NIGHT_EDGE_ADMIN_STATIC_DIRECTORY"
	instanceIDEnvironment             = "GAME_NIGHT_EDGE_INSTANCE_ID"
	defaultListenAddress              = ":8080"
	defaultAPIUpstreamURL             = "http://127.0.0.1:8081"
	defaultRealtimeUpstreamURL        = "http://127.0.0.1:8090"
	defaultUserStaticDirectory        = "/app/web"
	defaultAdminStaticDirectory       = "/app/admin"
	defaultInstanceID                 = "edge-local"
	defaultShutdownTimeout            = 15 * time.Second
	defaultReadHeaderTimeout          = 5 * time.Second
	defaultProxyDialTimeout           = 5 * time.Second
	defaultProxyTLSHandshakeTimeout   = 5 * time.Second
	defaultProxyResponseHeaderTimeout = 30 * time.Second
	defaultHealthTimeout              = 2 * time.Second
	staticIndexFileName               = "index.html"
)

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LookupEnv matches os.LookupEnv so tests can inject a fixed environment.
type LookupEnv func(string) (string, bool)

// Config contains only the validated, process-local edge gateway settings.
type Config struct {
	ListenAddress              string
	APIUpstreamURL             *url.URL
	RealtimeUpstreamURL        *url.URL
	UserStaticDirectory        string
	AdminStaticDirectory       string
	TrustedProxyCIDRs          []netip.Prefix
	ShutdownTimeout            time.Duration
	ReadHeaderTimeout          time.Duration
	ProxyDialTimeout           time.Duration
	ProxyTLSHandshakeTimeout   time.Duration
	ProxyResponseHeaderTimeout time.Duration
	HealthTimeout              time.Duration
	// InstanceID identifies one concrete edge process in the operations surface.
	InstanceID string
	// Heartbeat carries the private API target, credential, and bounded reporting cadence.
	Heartbeat serviceheartbeat.Config
}

// Load validates upstreams and static assets before the server starts. Host routing and proxy trust use
// fixed local defaults so deployments do not carry another allowlist configuration surface.
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
	userStaticDirectory, err := parseStaticDirectory(
		valueOrDefault(lookup, userStaticDirectoryEnvironment, defaultUserStaticDirectory),
		userStaticDirectoryEnvironment,
	)
	if err != nil {
		return Config{}, err
	}
	adminStaticDirectory, err := parseStaticDirectory(
		valueOrDefault(lookup, adminStaticDirectoryEnvironment, defaultAdminStaticDirectory),
		adminStaticDirectoryEnvironment,
	)
	if err != nil {
		return Config{}, err
	}
	instanceID := valueOrDefault(lookup, instanceIDEnvironment, defaultInstanceID)
	if !instanceIDPattern.MatchString(instanceID) {
		return Config{}, fieldError(instanceIDEnvironment, "invalid instance identifier")
	}
	heartbeat, err := serviceheartbeat.LoadConfig(serviceheartbeat.LookupEnv(lookup), true)
	if err != nil {
		return Config{}, fieldError(serviceheartbeat.TokenEnvironment, "missing or invalid heartbeat configuration")
	}
	return Config{
		ListenAddress:        listenAddress,
		APIUpstreamURL:       apiUpstreamURL,
		RealtimeUpstreamURL:  realtimeUpstreamURL,
		UserStaticDirectory:  userStaticDirectory,
		AdminStaticDirectory: adminStaticDirectory,
		TrustedProxyCIDRs: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"),
			netip.MustParsePrefix("::1/128"),
		},
		ShutdownTimeout:            defaultShutdownTimeout,
		ReadHeaderTimeout:          defaultReadHeaderTimeout,
		ProxyDialTimeout:           defaultProxyDialTimeout,
		ProxyTLSHandshakeTimeout:   defaultProxyTLSHandshakeTimeout,
		ProxyResponseHeaderTimeout: defaultProxyResponseHeaderTimeout,
		HealthTimeout:              defaultHealthTimeout,
		InstanceID:                 instanceID,
		Heartbeat:                  heartbeat,
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

func parseStaticDirectory(raw, environment string) (string, error) {
	if raw == "" {
		return "", fieldError(environment, "invalid static directory")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fieldError(environment, "invalid static directory")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fieldError(environment, "invalid static directory")
	}
	if _, err := os.Stat(filepath.Join(abs, staticIndexFileName)); err != nil {
		return "", fieldError(environment, "missing static index.html")
	}
	// Keep the caller's absolute path stable; the server resolves both sides when checking symlink boundaries.
	return abs, nil
}

// CanonicalizeAuthority normalizes a Host authority the same way config parsing and request routing do.
func CanonicalizeAuthority(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return "", fmt.Errorf("invalid authority")
	}
	parsed, err := url.Parse("//" + raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid authority")
	}
	host := parsed.Hostname()
	if host == "" || strings.Contains(host, "*") {
		return "", fmt.Errorf("invalid authority")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid authority")
		}
	}
	canonicalHost, err := canonicalizeHost(host)
	if err != nil {
		return "", err
	}
	if parsed.Port() == "" {
		return canonicalHost, nil
	}
	return formatAuthority(canonicalHost, parsed.Port()), nil
}

func canonicalizeHost(raw string) (string, error) {
	if ip, err := netip.ParseAddr(raw); err == nil {
		return ip.String(), nil
	}
	host := strings.ToLower(strings.TrimSuffix(raw, "."))
	if host == "" {
		return "", fmt.Errorf("invalid host")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("invalid host")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("invalid host")
			}
		}
	}
	return host, nil
}

func formatAuthority(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
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
