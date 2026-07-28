// Package serviceheartbeat reports bounded process liveness to the private API endpoint.
package serviceheartbeat

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/runtimeinfo"
	"github.com/iFTY-R/game-night/platform/admin/operations"
	"github.com/iFTY-R/game-night/platform/clock"
)

const (
	// Path is exact and must never be forwarded by a public edge or browser proxy.
	Path = "/internal/admin/operations/heartbeat"
	// MaximumBodyBytes prevents an authenticated internal caller from allocating an unbounded decoder buffer.
	MaximumBodyBytes int64 = 16 << 10
	// DefaultInterval keeps the 30-second stale threshold tolerant of one missed report.
	DefaultInterval = 10 * time.Second
	// DefaultTimeout bounds one report independently of process shutdown.
	DefaultTimeout = 2 * time.Second
	// TargetURLEnvironment points service reporters directly at the API private listener.
	TargetURLEnvironment = "GAME_NIGHT_ADMIN_HEARTBEAT_URL"
	// TokenEnvironment is shared only by the API receiver and the three internal reporters.
	TokenEnvironment = "GAME_NIGHT_ADMIN_HEARTBEAT_TOKEN"
	// BuildVersionEnvironment is an optional display-safe release identifier injected at build or deployment time.
	BuildVersionEnvironment = "GAME_NIGHT_BUILD_VERSION"
	// IntervalEnvironment and TimeoutEnvironment bound reporter cadence and per-request work.
	IntervalEnvironment = "GAME_NIGHT_HEARTBEAT_INTERVAL"
	TimeoutEnvironment  = "GAME_NIGHT_HEARTBEAT_TIMEOUT"
)

var (
	errInvalidHeartbeat = errors.New("invalid service heartbeat")
	// ErrInvalidConfig intentionally omits configured values because the target and token are deployment-sensitive.
	ErrInvalidConfig = errors.New("invalid service heartbeat configuration")
)

// LookupEnv is the minimal environment reader shared by all four service config packages.
type LookupEnv func(string) (string, bool)

// Config contains the shared reporter target, credential, build label, and bounded timing.
// The API loads the same config without requiring TargetURL because it writes its own heartbeat directly.
type Config struct {
	TargetURL    string
	Token        string
	BuildVersion string
	Interval     time.Duration
	Timeout      time.Duration
}

// LoadConfig validates shared heartbeat settings without opening a network connection.
func LoadConfig(lookup LookupEnv, requireTarget bool) (Config, error) {
	if lookup == nil {
		return Config{}, ErrInvalidConfig
	}
	read := func(name string) string {
		value, _ := lookup(name)
		return value
	}
	token := read(TokenEnvironment)
	if !ValidToken(token) {
		return Config{}, ErrInvalidConfig
	}
	target := read(TargetURLEnvironment)
	if requireTarget && !validTargetURL(target) || !requireTarget && target != "" && !validTargetURL(target) {
		return Config{}, ErrInvalidConfig
	}
	buildVersion := read(BuildVersionEnvironment)
	if strings.TrimSpace(buildVersion) != buildVersion || len(buildVersion) > 128 {
		return Config{}, ErrInvalidConfig
	}
	interval, err := parseDuration(read(IntervalEnvironment), DefaultInterval, time.Second, time.Minute)
	if err != nil {
		return Config{}, ErrInvalidConfig
	}
	timeout, err := parseDuration(read(TimeoutEnvironment), DefaultTimeout, time.Millisecond, interval/2)
	if err != nil {
		return Config{}, ErrInvalidConfig
	}
	return Config{TargetURL: target, Token: token, BuildVersion: buildVersion, Interval: interval, Timeout: timeout}, nil
}

// Snapshot is the bounded process state supplied at each reporting interval.
type Snapshot struct {
	Status             operations.HealthStatus            `json:"status"`
	Components         map[string]operations.HealthStatus `json:"components"`
	MaintenanceVersion uint64                             `json:"maintenance_version"`
}

// Payload combines immutable process identity with one bounded snapshot.
type Payload struct {
	ServiceKind        operations.ServiceKind             `json:"service_kind"`
	InstanceID         string                             `json:"instance_id"`
	BuildVersion       string                             `json:"build_version"`
	StartedAt          time.Time                          `json:"started_at"`
	Status             operations.HealthStatus            `json:"status"`
	Components         map[string]operations.HealthStatus `json:"components"`
	MaintenanceVersion uint64                             `json:"maintenance_version"`
}

// Instance converts the payload using server-authoritative heartbeat time.
func (payload Payload) Instance(receivedAt time.Time) (operations.ServiceInstance, error) {
	instance := operations.ServiceInstance{
		Kind: payload.ServiceKind, InstanceID: payload.InstanceID, BuildVersion: payload.BuildVersion, StartedAt: payload.StartedAt.UTC(),
		LastHeartbeatAt: receivedAt.UTC(), Status: payload.Status, Components: cloneComponents(payload.Components), MaintenanceVersion: payload.MaintenanceVersion,
	}
	if !operations.ValidServiceInstance(instance) {
		return operations.ServiceInstance{}, errInvalidHeartbeat
	}
	return instance, nil
}

// Sink persists one server-timestamped heartbeat.
type Sink interface {
	Report(context.Context, Payload) error
}

// InstanceRepository persists a validated service instance using the caller's transaction boundary.
type InstanceRepository interface {
	UpsertServiceInstance(context.Context, operations.ServiceInstance) (operations.ServiceInstance, error)
}

// RepositorySink lets the API report locally while preserving the same payload validation as HTTP reporters.
type RepositorySink struct {
	repository InstanceRepository
	clock      clock.Clock
}

// NewRepositorySink binds the API heartbeat reporter to server-authoritative time.
func NewRepositorySink(repository InstanceRepository, source clock.Clock) (*RepositorySink, error) {
	if repository == nil || source == nil {
		return nil, errInvalidHeartbeat
	}
	return &RepositorySink{repository: repository, clock: source}, nil
}

// Report converts and persists one API heartbeat without an unnecessary loopback HTTP request.
func (sink *RepositorySink) Report(ctx context.Context, payload Payload) error {
	if sink == nil || sink.repository == nil || sink.clock == nil || ctx == nil {
		return errInvalidHeartbeat
	}
	instance, err := payload.Instance(sink.clock.Now())
	if err != nil {
		return err
	}
	_, err = sink.repository.UpsertServiceInstance(ctx, instance)
	return err
}

// SnapshotFunc returns current bounded status without leaking the underlying error.
type SnapshotFunc func(context.Context) Snapshot

// Reporter emits an initial report and then repeats until its lifecycle context ends.
type Reporter struct {
	sink     Sink
	info     runtimeinfo.Info
	snapshot SnapshotFunc
	interval time.Duration
	timeout  time.Duration
}

// NewReporter validates timing and dependencies before a process starts serving traffic.
func NewReporter(sink Sink, info runtimeinfo.Info, snapshot SnapshotFunc, interval, timeout time.Duration) (*Reporter, error) {
	if sink == nil || snapshot == nil || interval < time.Second || interval > time.Minute || timeout <= 0 || timeout > interval/2 {
		return nil, errInvalidHeartbeat
	}
	return &Reporter{sink: sink, info: info, snapshot: snapshot, interval: interval, timeout: timeout}, nil
}

// Run reports until cancellation; transient report failures do not stop the owning service.
func (reporter *Reporter) Run(ctx context.Context) {
	if reporter == nil || ctx == nil {
		return
	}
	reporter.report(ctx)
	ticker := time.NewTicker(reporter.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// A bounded final report records the last dependency snapshot before the owner closes its clients.
			reporter.report(context.Background())
			return
		case <-ticker.C:
			reporter.report(ctx)
		}
	}
}

func (reporter *Reporter) report(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, reporter.timeout)
	defer cancel()
	snapshot := reporter.snapshot(ctx)
	payload := Payload{
		ServiceKind: reporter.info.Kind, InstanceID: reporter.info.InstanceID, BuildVersion: reporter.info.BuildVersion, StartedAt: reporter.info.StartedAt,
		Status: snapshot.Status, Components: cloneComponents(snapshot.Components), MaintenanceVersion: snapshot.MaintenanceVersion,
	}
	_ = reporter.sink.Report(ctx, payload)
}

// HTTPClient sends heartbeats only to one validated exact URL with one fixed credential.
type HTTPClient struct {
	client *http.Client
	url    string
	token  string
}

// NewHTTPClient validates the private target and credential without opening the network.
func NewHTTPClient(client *http.Client, targetURL, token string) (*HTTPClient, error) {
	parsed, err := url.Parse(targetURL)
	if client == nil || err != nil || !validTargetURL(targetURL) || !ValidToken(token) {
		return nil, errInvalidHeartbeat
	}
	return &HTTPClient{client: client, url: parsed.String(), token: token}, nil
}

// Report posts one bounded JSON document and accepts only an empty 204 response.
func (client *HTTPClient) Report(ctx context.Context, payload Payload) error {
	if client == nil || client.client == nil || ctx == nil {
		return errInvalidHeartbeat
	}
	encoded, err := json.Marshal(payload)
	if err != nil || int64(len(encoded)) > MaximumBodyBytes {
		return errInvalidHeartbeat
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, strings.NewReader(string(encoded)))
	if err != nil {
		return errInvalidHeartbeat
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode != http.StatusNoContent {
		return errInvalidHeartbeat
	}
	return nil
}

// ValidToken applies one printable fixed-length internal credential boundary.
func ValidToken(token string) bool {
	if len(token) < 32 || len(token) > 256 || strings.TrimSpace(token) != token {
		return false
	}
	for index := range len(token) {
		if token[index] < 0x21 || token[index] > 0x7e {
			return false
		}
	}
	return true
}

// TokenMatches compares internal credentials without content-dependent timing.
func TokenMatches(expected, actual string) bool {
	if !ValidToken(expected) || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func validTargetURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != Path ||
		parsed.Hostname() == "" || parsed.Port() == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return true
}

func parseDuration(value string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, ErrInvalidConfig
	}
	return parsed, nil
}

func cloneComponents(source map[string]operations.HealthStatus) map[string]operations.HealthStatus {
	result := make(map[string]operations.HealthStatus, len(source))
	for code, status := range source {
		result[code] = status
	}
	return result
}
