// Package config loads API-only listener options and composes them with shared process configuration.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

const (
	// API environment variables remain private to the API binary instead of expanding the shared configuration surface.
	listenAddressEnvironment     = "GAME_NIGHT_API_LISTEN_ADDRESS"
	readHeaderTimeoutEnvironment = "GAME_NIGHT_API_READ_HEADER_TIMEOUT"
	readTimeoutEnvironment       = "GAME_NIGHT_API_READ_TIMEOUT"
	writeTimeoutEnvironment      = "GAME_NIGHT_API_WRITE_TIMEOUT"
	idleTimeoutEnvironment       = "GAME_NIGHT_API_IDLE_TIMEOUT"
	shutdownTimeoutEnvironment   = "GAME_NIGHT_API_SHUTDOWN_TIMEOUT"
	maxHeaderBytesEnvironment    = "GAME_NIGHT_API_MAX_HEADER_BYTES"
	argon2WorkersEnvironment     = "GAME_NIGHT_API_ARGON2_WORKERS"
	argon2QueueEnvironment       = "GAME_NIGHT_API_ARGON2_QUEUE_CAPACITY"
	// Listener defaults support mobile requests without allowing stalled clients to hold resources indefinitely.
	defaultListenAddress     = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	maximumReadHeaderTimeout = 30 * time.Second
	defaultReadTimeout       = 15 * time.Second
	maximumReadTimeout       = 2 * time.Minute
	defaultWriteTimeout      = 30 * time.Second
	maximumWriteTimeout      = 2 * time.Minute
	defaultIdleTimeout       = time.Minute
	maximumIdleTimeout       = 5 * time.Minute
	defaultShutdownTimeout   = 15 * time.Second
	maximumShutdownTimeout   = time.Minute
	// Header limits accommodate Connect metadata while bounding pre-handler memory allocation.
	defaultMaxHeaderBytes = 1 << 20
	minimumMaxHeaderBytes = 4 << 10
	maximumMaxHeaderBytes = 4 << 20
	// Two workers keep the default aggregate Argon2 memory at 128 MiB; the hard cap matches security package limits.
	defaultArgon2Workers = 2
	maximumArgon2Workers = 8
	defaultArgon2Queue   = 64
	maximumArgon2Queue   = 4096
)

// ListenerConfig bounds HTTP resource use and graceful shutdown time for the API process.
type ListenerConfig struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
}

// Argon2Config bounds expensive password and recovery-code work independently of HTTP concurrency.
type Argon2Config struct {
	Workers       int
	QueueCapacity int
}

// Config combines the shared dependency/security settings with API-only listener behavior.
type Config struct {
	Shared            sharedconfig.Config
	CheckpointStorage checkpointstorage.Config
	Listener          ListenerConfig
	Argon2            Argon2Config
}

// Load validates shared configuration first, then parses bounded API listener settings without opening sockets.
func Load(lookupEnv sharedconfig.LookupEnv) (Config, error) {
	shared, err := sharedconfig.Load(lookupEnv)
	if err != nil {
		return Config{}, err
	}
	checkpointStorage, err := checkpointstorage.Load(lookupEnv, shared.Environment)
	if err != nil {
		return Config{}, err
	}
	reader := environmentReader{lookup: lookupEnv}
	listener, err := loadListener(reader)
	if err != nil {
		return Config{}, err
	}
	argon2Config, err := loadArgon2(reader)
	if err != nil {
		return Config{}, err
	}
	return Config{Shared: shared, CheckpointStorage: checkpointStorage, Listener: listener, Argon2: argon2Config}, nil
}

type environmentReader struct {
	lookup sharedconfig.LookupEnv
}

func (r environmentReader) optional(name string) string {
	value, _ := r.lookup(name)
	return strings.TrimSpace(value)
}

func (r environmentReader) valueOrDefault(name, fallback string) string {
	if value := r.optional(name); value != "" {
		return value
	}
	return fallback
}

func loadListener(reader environmentReader) (ListenerConfig, error) {
	address := reader.valueOrDefault(listenAddressEnvironment, defaultListenAddress)
	if !validListenAddress(address) {
		return ListenerConfig{}, fieldError(listenAddressEnvironment, "invalid listen address")
	}
	readHeaderTimeout, err := parseDuration(reader, readHeaderTimeoutEnvironment, defaultReadHeaderTimeout, maximumReadHeaderTimeout)
	if err != nil {
		return ListenerConfig{}, err
	}
	readTimeout, err := parseDuration(reader, readTimeoutEnvironment, defaultReadTimeout, maximumReadTimeout)
	if err != nil {
		return ListenerConfig{}, err
	}
	writeTimeout, err := parseDuration(reader, writeTimeoutEnvironment, defaultWriteTimeout, maximumWriteTimeout)
	if err != nil {
		return ListenerConfig{}, err
	}
	idleTimeout, err := parseDuration(reader, idleTimeoutEnvironment, defaultIdleTimeout, maximumIdleTimeout)
	if err != nil {
		return ListenerConfig{}, err
	}
	shutdownTimeout, err := parseDuration(reader, shutdownTimeoutEnvironment, defaultShutdownTimeout, maximumShutdownTimeout)
	if err != nil {
		return ListenerConfig{}, err
	}
	maxHeaderBytes, err := parseInteger(reader, maxHeaderBytesEnvironment, defaultMaxHeaderBytes, minimumMaxHeaderBytes, maximumMaxHeaderBytes)
	if err != nil {
		return ListenerConfig{}, err
	}
	return ListenerConfig{
		Address:           address,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ShutdownTimeout:   shutdownTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}, nil
}

func loadArgon2(reader environmentReader) (Argon2Config, error) {
	workers, err := parseInteger(reader, argon2WorkersEnvironment, defaultArgon2Workers, 1, maximumArgon2Workers)
	if err != nil {
		return Argon2Config{}, err
	}
	queueCapacity, err := parseInteger(reader, argon2QueueEnvironment, defaultArgon2Queue, 0, maximumArgon2Queue)
	if err != nil {
		return Argon2Config{}, err
	}
	return Argon2Config{Workers: workers, QueueCapacity: queueCapacity}, nil
}

func validListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.ContainsAny(host, " /\\") {
		return false
	}
	parsedPort, err := strconv.Atoi(port)
	return err == nil && parsedPort >= 1 && parsedPort <= 65535
}

func parseDuration(reader environmentReader, name string, fallback, maximum time.Duration) (time.Duration, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > maximum {
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
