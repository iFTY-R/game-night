package nginxtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
)

// nginxImageEnvironment must contain an immutable image reference so CI never tests a moving Nginx build.
const nginxImageEnvironment = "GAME_NIGHT_TEST_NGINX_IMAGE"

func TestPinnedImageValidationRejectsMutableOrMalformedReferences(t *testing.T) {
	validDigest := strings.Repeat("a", 64)
	for value, expected := range map[string]bool{
		"nginx:1.29-alpine":                            false,
		"nginx@sha256:" + strings.Repeat("A", 64):      false,
		"nginx@sha256:" + strings.Repeat("a", 63):      false,
		"nginx:1.29-alpine@sha256:" + validDigest:      true,
		"registry.example/nginx@sha256:" + validDigest: true,
	} {
		if actual := validPinnedImage(value); actual != expected {
			t.Fatalf("validPinnedImage(%q) = %t, want %t", value, actual, expected)
		}
	}
}

func TestNginxContainerEnforcesHostPathHeaderAndCacheBoundaries(t *testing.T) {
	image := requireNginxRuntime(t)
	identity := startEchoUpstream(t, "identity")
	admin := startEchoUpstream(t, "admin")
	userHost, adminHost := "play.game-night.test", "admin.game-night.test"
	tlsDirectory, roots := writeTestTLSIdentity(t, userHost, adminHost)
	configurationDirectory := t.TempDir()
	nginxConfig := absoluteTestPath(t, "..", "nginx.conf")
	template := absoluteTestPath(t, "..", "templates", "game-night.conf.template")

	containerArguments := []string{
		"--add-host", "host.docker.internal:host-gateway",
		"--env", "GAME_NIGHT_IDENTITY_UPSTREAM=" + identity.containerAddress(),
		"--env", "GAME_NIGHT_ADMIN_UPSTREAM=" + admin.containerAddress(),
		"--env", "GAME_NIGHT_USER_HOST=" + userHost,
		"--env", "GAME_NIGHT_ADMIN_HOST=" + adminHost,
		"--mount", bindMount(nginxConfig, "/etc/nginx/nginx.conf", true),
		"--mount", bindMount(template, "/etc/nginx/templates/game-night.conf.template", true),
		"--mount", bindMount(tlsDirectory, "/etc/nginx/tls", true),
		"--mount", bindMount(configurationDirectory, "/etc/nginx/conf.d", false),
	}
	validateArguments := append([]string{"run", "--rm"}, containerArguments...)
	validateArguments = append(validateArguments, image, "nginx", "-t")
	if output, err := runDocker(t, 90*time.Second, validateArguments...); err != nil {
		t.Fatalf("nginx -t failed: %v\n%s", err, output)
	}

	containerName := "game-night-nginx-test-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	runArguments := append([]string{"run", "--detach", "--name", containerName, "--publish", "127.0.0.1::8443"}, containerArguments...)
	runArguments = append(runArguments, image)
	if output, err := runDocker(t, 90*time.Second, runArguments...); err != nil {
		t.Fatalf("start nginx container: %v\n%s", err, output)
	}
	t.Cleanup(func() { _, _ = runDocker(t, 30*time.Second, "rm", "--force", containerName) })

	portOutput, err := runDocker(t, 30*time.Second, "port", containerName, "8443/tcp")
	if err != nil {
		t.Fatalf("read nginx test port: %v\n%s", err, portOutput)
	}
	endpoint := "https://127.0.0.1:" + mappedPort(t, portOutput)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: testTLSConfig(roots, userHost)},
	}
	waitForNginx(t, client, endpoint, userHost, containerName)

	identityResponse := requestNginx(t, client, endpoint, userHost,
		"/platform.identity.v1.IdentityService/Bootstrap", true)
	assertAllowedResponse(t, identityResponse, "identity")
	identityRequest := identity.lastRequest(t)
	assertForwardingHeadersReplaced(t, identityRequest, userHost)

	adminAuthResponse := requestNginx(t, client, endpoint, adminHost,
		"/platform.admin.v1.AdminAuthService/GetStatus", false)
	assertAllowedResponse(t, adminAuthResponse, "admin")
	adminIdentityResponse := requestNginx(t, client, endpoint, adminHost,
		"/platform.admin.v1.AdminIdentityService/GetUser", false)
	assertAllowedResponse(t, adminIdentityResponse, "admin")

	for _, request := range []struct {
		host string
		path string
	}{
		{host: userHost, path: "/platform.admin.v1.AdminAuthService/GetStatus"},
		{host: adminHost, path: "/platform.identity.v1.IdentityService/Bootstrap"},
		{host: userHost, path: "/platform.identity.v1.UnknownService/Call"},
		{host: "unexpected.game-night.test", path: "/platform.identity.v1.IdentityService/Bootstrap"},
	} {
		response := requestNginx(t, client, endpoint, request.host, request.path, false)
		if response.StatusCode != http.StatusNotFound || response.Upstream != "" {
			t.Fatalf("rejected request host=%q path=%q returned status=%d upstream=%q",
				request.host, request.path, response.StatusCode, response.Upstream)
		}
	}
}

type capturedRequest struct {
	Forwarded       []string
	XForwardedFor   []string
	XForwardedProto []string
	XForwardedHost  []string
	XForwardedPort  []string
	XRealIP         []string
	Host            string
}

type echoUpstream struct {
	listener net.Listener
	server   *http.Server
	name     string
	mu       sync.Mutex
	requests []capturedRequest
}

func startEchoUpstream(t testing.TB, name string) *echoUpstream {
	t.Helper()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &echoUpstream{listener: listener, name: name}
	upstream.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstream.mu.Lock()
		upstream.requests = append(upstream.requests, capturedRequest{
			Forwarded: request.Header.Values("Forwarded"), XForwardedFor: request.Header.Values("X-Forwarded-For"),
			XForwardedProto: request.Header.Values("X-Forwarded-Proto"), XForwardedHost: request.Header.Values("X-Forwarded-Host"),
			XForwardedPort: request.Header.Values("X-Forwarded-Port"), XRealIP: request.Header.Values("X-Real-IP"), Host: request.Host,
		})
		upstream.mu.Unlock()
		writer.Header().Set("Cache-Control", "public, max-age=3600")
		writer.Header().Set("Pragma", "cache")
		writer.Header().Set("X-Game-Night-Upstream", name)
		writer.Header().Set("Set-Cookie", "__Host-test=session; Path=/; Secure; HttpOnly; SameSite=Strict")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"upstream": name})
	})}
	go func() { _ = upstream.server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = upstream.server.Shutdown(ctx)
	})
	return upstream
}

func (upstream *echoUpstream) containerAddress() string {
	return net.JoinHostPort("host.docker.internal", strconv.Itoa(upstream.listener.Addr().(*net.TCPAddr).Port))
}

func (upstream *echoUpstream) lastRequest(t testing.TB) capturedRequest {
	t.Helper()
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.requests) == 0 {
		t.Fatal("upstream received no request")
	}
	return upstream.requests[len(upstream.requests)-1]
}

type nginxResponse struct {
	StatusCode   int
	Upstream     string
	CacheControl string
	Pragma       string
	HSTS         string
	TLSVersion   uint16
}

func requestNginx(t testing.TB, client *http.Client, endpoint, host, path string, forgedHeaders bool) nginxResponse {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	if forgedHeaders {
		request.Header.Add("Forwarded", "for=198.51.100.10;proto=http;host=attacker.invalid")
		request.Header.Add("Forwarded", "for=203.0.113.20")
		request.Header.Add("X-Forwarded-For", "198.51.100.10, 203.0.113.20")
		request.Header.Add("X-Forwarded-For", "192.0.2.30")
		request.Header.Set("X-Forwarded-Proto", "http")
		request.Header.Set("X-Forwarded-Host", "attacker.invalid")
		request.Header.Set("X-Forwarded-Port", "80")
		request.Header.Set("X-Real-IP", "198.51.100.10")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result := nginxResponse{
		StatusCode: response.StatusCode, Upstream: response.Header.Get("X-Game-Night-Upstream"),
		CacheControl: response.Header.Get("Cache-Control"), Pragma: response.Header.Get("Pragma"),
		HSTS: response.Header.Get("Strict-Transport-Security"),
	}
	if response.TLS != nil {
		result.TLSVersion = response.TLS.Version
	}
	return result
}

func assertAllowedResponse(t testing.TB, response nginxResponse, upstream string) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.Upstream != upstream {
		t.Fatalf("allowed route status=%d upstream=%q, want 200/%q", response.StatusCode, response.Upstream, upstream)
	}
	if response.CacheControl != "no-store" || response.Pragma != "no-cache" {
		t.Fatalf("cache headers = %q / %q", response.CacheControl, response.Pragma)
	}
	if response.HSTS != "max-age=31536000" || response.TLSVersion < 0x0303 {
		t.Fatalf("TLS boundary hsts=%q version=%#x", response.HSTS, response.TLSVersion)
	}
}

func assertForwardingHeadersReplaced(t testing.TB, request capturedRequest, expectedHost string) {
	t.Helper()
	for name, values := range map[string][]string{
		"Forwarded": request.Forwarded, "X-Forwarded-For": request.XForwardedFor,
		"X-Forwarded-Proto": request.XForwardedProto, "X-Forwarded-Host": request.XForwardedHost,
		"X-Forwarded-Port": request.XForwardedPort, "X-Real-IP": request.XRealIP,
	} {
		if len(values) != 1 {
			t.Fatalf("%s values = %q, want one canonical value", name, values)
		}
		if strings.Contains(values[0], "198.51.100.10") || strings.Contains(values[0], "203.0.113.20") ||
			strings.Contains(values[0], "192.0.2.30") || strings.Contains(values[0], "attacker.invalid") {
			t.Fatalf("%s retained forged input: %q", name, values)
		}
	}
	if net.ParseIP(request.XForwardedFor[0]) == nil || request.XForwardedProto[0] != "https" ||
		request.XForwardedHost[0] != expectedHost || request.XForwardedPort[0] != "443" || request.Host != expectedHost ||
		!strings.Contains(request.Forwarded[0], "proto=https") || !strings.Contains(request.Forwarded[0], "host=\""+expectedHost+"\"") {
		t.Fatalf("canonical forwarding boundary = %+v", request)
	}
}

func requireNginxRuntime(t testing.TB) string {
	t.Helper()
	required, err := integrationtest.RequiredDependencies()
	if err != nil {
		t.Fatal(err)
	}
	_, mustRun := required[integrationtest.DependencyNginx]
	image := strings.TrimSpace(os.Getenv(nginxImageEnvironment))
	if image == "" {
		dependencyUnavailable(t, mustRun, nginxImageEnvironment+" is required and must contain a digest-pinned image")
	}
	if !validPinnedImage(image) {
		t.Fatalf("%s must use image@sha256:<64 lowercase hex>", nginxImageEnvironment)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		dependencyUnavailable(t, mustRun, "docker executable is unavailable")
	}
	if output, err := runDocker(t, 30*time.Second, "version", "--format", "{{.Server.Version}}"); err != nil {
		dependencyUnavailable(t, mustRun, "docker runtime is unavailable: "+strings.TrimSpace(output))
	}
	if _, err := runDocker(t, 30*time.Second, "image", "inspect", image); err != nil {
		if output, pullErr := runDocker(t, 3*time.Minute, "pull", image); pullErr != nil {
			dependencyUnavailable(t, mustRun, "digest-pinned nginx image is unavailable: "+strings.TrimSpace(output))
		}
	}
	return image
}

func dependencyUnavailable(t testing.TB, required bool, reason string) {
	t.Helper()
	if required {
		t.Fatal(reason)
	}
	t.Skip("SKIPPED: nginx dependency unavailable: " + reason)
}

func validPinnedImage(image string) bool {
	parts := strings.Split(image, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 || strings.ToLower(parts[1]) != parts[1] {
		return false
	}
	decoded, err := hex.DecodeString(parts[1])
	return err == nil && len(decoded) == 32
}

func runDocker(t testing.TB, timeout time.Duration, arguments ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", arguments...).CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return string(output), err
}

func bindMount(source, target string, readOnly bool) string {
	option := "type=bind,source=" + filepath.ToSlash(source) + ",target=" + target
	if readOnly {
		option += ",readonly"
	}
	return option
}

func absoluteTestPath(t testing.TB, elements ...string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(elements...))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func mappedPort(t testing.TB, output string) string {
	t.Helper()
	line := strings.TrimSpace(strings.Split(strings.TrimSpace(output), "\n")[0])
	_, port, err := net.SplitHostPort(line)
	if err != nil || port == "" {
		t.Fatalf("invalid docker port output %q", output)
	}
	return port
}

func waitForNginx(t testing.TB, client *http.Client, endpoint, host, containerName string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			endpoint+"/platform.identity.v1.IdentityService/Bootstrap", strings.NewReader("{}"))
		request.Host = host
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := runDocker(t, 10*time.Second, "logs", containerName)
	t.Fatalf("nginx container did not become ready\n%s", logs)
}

func writeTestTLSIdentity(t testing.TB, hosts ...string) (string, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: hosts[0]}, DNSNames: hosts,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if err := os.WriteFile(filepath.Join(directory, "tls.crt"), certificatePEM, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tls.key"), privatePEM, 0o400); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("append generated nginx test certificate")
	}
	return directory, roots
}

func testTLSConfig(roots *x509.CertPool, serverName string) *tls.Config {
	return &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}
}
