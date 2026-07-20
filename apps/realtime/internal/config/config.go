// Package config loads realtime listener, ownership, and WebSocket resource policy.
package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
)

const (
	publicListenAddressEnvironment   = "GAME_NIGHT_REALTIME_PUBLIC_LISTEN_ADDRESS"
	internalListenAddressEnvironment = "GAME_NIGHT_REALTIME_INTERNAL_LISTEN_ADDRESS"
	advertisedURLEnvironment         = "GAME_NIGHT_REALTIME_ADVERTISED_URL"
	instanceIDEnvironment            = "GAME_NIGHT_REALTIME_INSTANCE_ID"
	internalTokenEnvironment         = "GAME_NIGHT_REALTIME_INTERNAL_TOKEN"
	leaseTTLEnvironment              = "GAME_NIGHT_REALTIME_LEASE_TTL"
	renewIntervalEnvironment         = "GAME_NIGHT_REALTIME_RENEW_INTERVAL"
	shutdownTimeoutEnvironment       = "GAME_NIGHT_REALTIME_SHUTDOWN_TIMEOUT"
	helloTimeoutEnvironment          = "GAME_NIGHT_REALTIME_HELLO_TIMEOUT"
	writeTimeoutEnvironment          = "GAME_NIGHT_REALTIME_WRITE_TIMEOUT"
	pingIntervalEnvironment          = "GAME_NIGHT_REALTIME_PING_INTERVAL"
	authorizationIntervalEnvironment = "GAME_NIGHT_REALTIME_AUTHORIZATION_INTERVAL"
	maxMessageBytesEnvironment       = "GAME_NIGHT_REALTIME_MAX_MESSAGE_BYTES"
	sendQueueCapacityEnvironment     = "GAME_NIGHT_REALTIME_SEND_QUEUE_CAPACITY"
	timerScanIntervalEnvironment     = "GAME_NIGHT_REALTIME_TIMER_SCAN_INTERVAL"
	timerOperationTimeoutEnvironment = "GAME_NIGHT_REALTIME_TIMER_OPERATION_TIMEOUT"
	timerBatchSizeEnvironment        = "GAME_NIGHT_REALTIME_TIMER_BATCH_SIZE"
	outboxLeaseDurationEnvironment   = "GAME_NIGHT_REALTIME_OUTBOX_LEASE_DURATION"
	outboxPollIntervalEnvironment    = "GAME_NIGHT_REALTIME_OUTBOX_POLL_INTERVAL"
	outboxBatchSizeEnvironment       = "GAME_NIGHT_REALTIME_OUTBOX_BATCH_SIZE"

	defaultPublicListenAddress   = ":8090"
	defaultInternalListenAddress = ":8091"
	defaultAdvertisedURL         = "http://127.0.0.1:8091"
	defaultInstanceID            = "realtime-local"
	defaultLeaseTTL              = 15 * time.Second
	defaultRenewInterval         = 5 * time.Second
	defaultShutdownTimeout       = 15 * time.Second
	defaultHelloTimeout          = 5 * time.Second
	defaultWriteTimeout          = 5 * time.Second
	defaultPingInterval          = 20 * time.Second
	defaultAuthorizationInterval = 15 * time.Second
	defaultMaxMessageBytes       = 16 << 10
	defaultSendQueueCapacity     = 32
	defaultTimerScanInterval     = 250 * time.Millisecond
	defaultTimerOperationTimeout = 5 * time.Second
	defaultTimerBatchSize        = 128
	defaultOutboxLeaseDuration   = 15 * time.Second
	defaultOutboxPollInterval    = 250 * time.Millisecond
	defaultOutboxBatchSize       = 128
)

// ListenerConfig keeps browser upgrades isolated from private owner command traffic.
type ListenerConfig struct {
	PublicAddress   string
	InternalAddress string
	ShutdownTimeout time.Duration
}

// OwnershipConfig defines the stable route stored in Redis and its fail-closed renewal cadence.
type OwnershipConfig struct {
	InstanceID    string
	AdvertisedURL string
	LeaseTTL      time.Duration
	RenewInterval time.Duration
}

// WebSocketConfig bounds pre-authentication work, periodic authorization, and slow-client buffering.
type WebSocketConfig struct {
	HelloTimeout          time.Duration
	WriteTimeout          time.Duration
	PingInterval          time.Duration
	AuthorizationInterval time.Duration
	MaxMessageBytes       int64
	SendQueueCapacity     int
}

// TimerConfig bounds PostgreSQL recovery scans and each ownership-fenced timer command.
type TimerConfig struct {
	ScanInterval     time.Duration
	OperationTimeout time.Duration
	BatchSize        uint32
}

// FanoutConfig bounds the durable outbox consumer that repairs commit-to-Redis publish gaps.
type FanoutConfig struct {
	LeaseDuration time.Duration
	PollInterval  time.Duration
	BatchSize     uint32
}

// Config combines existing PostgreSQL/Redis/origin policy with realtime-only process settings.
type Config struct {
	Shared        sharedconfig.RealtimeDependencies
	Listener      ListenerConfig
	Ownership     OwnershipConfig
	WebSocket     WebSocketConfig
	Timer         TimerConfig
	Fanout        FanoutConfig
	InternalToken string
}

// Load validates shared dependencies first and never opens a listener or dependency connection.
func Load(lookup sharedconfig.LookupEnv) (Config, error) {
	shared, err := sharedconfig.LoadRealtime(lookup)
	if err != nil {
		return Config{}, err
	}
	process, err := loadProcessConfig(lookup, shared.Environment)
	if err != nil {
		return Config{}, err
	}
	process.Shared = shared
	return process, nil
}

func loadProcessConfig(lookup sharedconfig.LookupEnv, environment sharedconfig.Environment) (Config, error) {
	reader := environmentReader{lookup: lookup}
	publicAddress := reader.valueOrDefault(publicListenAddressEnvironment, defaultPublicListenAddress)
	if !validListenAddress(publicAddress) {
		return Config{}, fieldError(publicListenAddressEnvironment, "invalid listen address")
	}
	internalAddress := reader.valueOrDefault(internalListenAddressEnvironment, defaultInternalListenAddress)
	if !validListenAddress(internalAddress) || internalAddress == publicAddress {
		return Config{}, fieldError(internalListenAddressEnvironment, "invalid or shared listen address")
	}
	advertisedURL := reader.valueOrDefault(advertisedURLEnvironment, defaultAdvertisedURL)
	if !validAdvertisedURL(advertisedURL, environment == sharedconfig.EnvironmentProduction) {
		return Config{}, fieldError(advertisedURLEnvironment, "invalid internal service URL")
	}
	instanceID := reader.valueOrDefault(instanceIDEnvironment, defaultInstanceID)
	if !validInstanceID(instanceID) {
		return Config{}, fieldError(instanceIDEnvironment, "invalid instance identifier")
	}
	internalToken, err := reader.required(internalTokenEnvironment)
	if err != nil || !validInternalToken(internalToken) {
		return Config{}, fieldError(internalTokenEnvironment, "missing or invalid internal credential")
	}
	leaseTTL, err := parseDuration(reader, leaseTTLEnvironment, defaultLeaseTTL, redisstore.MinimumSessionLeaseTTL, redisstore.MaximumSessionLeaseTTL)
	if err != nil {
		return Config{}, err
	}
	renewInterval, err := parseDuration(reader, renewIntervalEnvironment, defaultRenewInterval, time.Millisecond, leaseTTL/2)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parseDuration(reader, shutdownTimeoutEnvironment, defaultShutdownTimeout, time.Second, time.Minute)
	if err != nil {
		return Config{}, err
	}
	helloTimeout, err := parseDuration(reader, helloTimeoutEnvironment, defaultHelloTimeout, time.Second, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := parseDuration(reader, writeTimeoutEnvironment, defaultWriteTimeout, time.Second, 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	pingInterval, err := parseDuration(reader, pingIntervalEnvironment, defaultPingInterval, 5*time.Second, time.Minute)
	if err != nil {
		return Config{}, err
	}
	authorizationInterval, err := parseDuration(reader, authorizationIntervalEnvironment, defaultAuthorizationInterval, 5*time.Second, 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	maxMessageBytes, err := parseInteger(reader, maxMessageBytesEnvironment, defaultMaxMessageBytes, 1<<10, 64<<10)
	if err != nil {
		return Config{}, err
	}
	sendQueueCapacity, err := parseInteger(reader, sendQueueCapacityEnvironment, defaultSendQueueCapacity, 4, 256)
	if err != nil {
		return Config{}, err
	}
	timerScanInterval, err := parseDuration(reader, timerScanIntervalEnvironment, defaultTimerScanInterval, 10*time.Millisecond, time.Minute)
	if err != nil {
		return Config{}, err
	}
	timerOperationTimeout, err := parseDuration(reader, timerOperationTimeoutEnvironment, defaultTimerOperationTimeout, 100*time.Millisecond, time.Minute)
	if err != nil {
		return Config{}, err
	}
	timerBatchSize, err := parseInteger(reader, timerBatchSizeEnvironment, defaultTimerBatchSize, 1, 1024)
	if err != nil {
		return Config{}, err
	}
	outboxLeaseDuration, err := parseDuration(reader, outboxLeaseDurationEnvironment, defaultOutboxLeaseDuration, time.Second, 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	outboxPollInterval, err := parseDuration(reader, outboxPollIntervalEnvironment, defaultOutboxPollInterval, 10*time.Millisecond, time.Minute)
	if err != nil {
		return Config{}, err
	}
	outboxBatchSize, err := parseInteger(reader, outboxBatchSizeEnvironment, defaultOutboxBatchSize, 1, 1000)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Listener: ListenerConfig{
			PublicAddress: publicAddress, InternalAddress: internalAddress, ShutdownTimeout: shutdownTimeout,
		},
		Ownership: OwnershipConfig{
			InstanceID: instanceID, AdvertisedURL: advertisedURL, LeaseTTL: leaseTTL, RenewInterval: renewInterval,
		},
		WebSocket: WebSocketConfig{
			HelloTimeout: helloTimeout, WriteTimeout: writeTimeout, PingInterval: pingInterval,
			AuthorizationInterval: authorizationInterval, MaxMessageBytes: int64(maxMessageBytes),
			SendQueueCapacity: sendQueueCapacity,
		},
		Timer: TimerConfig{
			ScanInterval: timerScanInterval, OperationTimeout: timerOperationTimeout, BatchSize: uint32(timerBatchSize),
		},
		Fanout: FanoutConfig{
			LeaseDuration: outboxLeaseDuration, PollInterval: outboxPollInterval, BatchSize: uint32(outboxBatchSize),
		},
		InternalToken: internalToken,
	}, nil
}

type environmentReader struct {
	lookup sharedconfig.LookupEnv
}

func (reader environmentReader) optional(name string) string {
	value, _ := reader.lookup(name)
	return strings.TrimSpace(value)
}

func (reader environmentReader) required(name string) (string, error) {
	value := reader.optional(name)
	if value == "" {
		return "", fieldError(name, "required configuration is missing")
	}
	return value, nil
}

func (reader environmentReader) valueOrDefault(name, fallback string) string {
	if value := reader.optional(name); value != "" {
		return value
	}
	return fallback
}

func validListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.ContainsAny(host, " /\\") {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort >= 1 && parsedPort <= 65535
}

func validAdvertisedURL(value string, requireTLS bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if requireTLS && parsed.Scheme != "https" {
		return false
	}
	return parsed.Hostname() != "" && parsed.Port() != ""
}

func validInstanceID(value string) bool {
	if len(value) < 3 || len(value) > redisstore.MaximumLeaseOwnerBytes {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && character != '.' &&
			(character < '0' || character > '9') && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func validInternalToken(value string) bool {
	if len(value) < 32 || len(value) > 256 {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func parseDuration(reader environmentReader, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fieldError(name, "duration outside allowed range")
	}
	return parsed, nil
}

func parseInteger(reader environmentReader, name string, fallback, minimum, maximum int) (int, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fieldError(name, "integer outside allowed range")
	}
	return parsed, nil
}

func fieldError(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}
