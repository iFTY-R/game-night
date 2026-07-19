// Package config loads worker-only scheduling settings and composes shared secure dependencies.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/apps/internal/checkpointstorage"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/outbox"
)

const (
	workerInstanceEnvironment = "GAME_NIGHT_WORKER_INSTANCE_ID"
	workerLeaseEnvironment    = "GAME_NIGHT_WORKER_LEASE_DURATION"
	workerBatchEnvironment    = "GAME_NIGHT_WORKER_BATCH_SIZE"
	workerPollEnvironment     = "GAME_NIGHT_WORKER_POLL_INTERVAL"
	workerShutdownEnvironment = "GAME_NIGHT_WORKER_SHUTDOWN_TIMEOUT"
	// Defaults keep recovery latency low without holding leases across long idle periods.
	defaultLeaseDuration = time.Minute
	defaultBatchSize     = 10
	defaultPollInterval  = time.Second
	defaultShutdown      = 15 * time.Second
	maximumPollInterval  = time.Minute
	maximumShutdown      = time.Minute
)

// RuntimeConfig controls lease ownership and the single checkpoint consumer loop.
type RuntimeConfig struct {
	InstanceID      outbox.LeaseOwner
	LeaseDuration   time.Duration
	BatchSize       uint32
	PollInterval    time.Duration
	ShutdownTimeout time.Duration
}

// Config separates worker scheduling from the shared database, keyring, and checkpoint sink policy.
type Config struct {
	Shared            sharedconfig.WorkerDependencies
	CheckpointStorage checkpointstorage.Config
	Runtime           RuntimeConfig
}

// Load validates all worker settings before opening a database pool or object-storage client.
func Load(lookup sharedconfig.LookupEnv) (Config, error) {
	shared, err := sharedconfig.LoadWorker(lookup)
	if err != nil {
		return Config{}, err
	}
	storage, err := checkpointstorage.Load(lookup, shared.Environment)
	if err != nil {
		return Config{}, err
	}
	read := func(name string) string {
		value, _ := lookup(name)
		return strings.TrimSpace(value)
	}
	instanceID, err := outbox.ParseLeaseOwner(read(workerInstanceEnvironment))
	if err != nil {
		return Config{}, fieldError(workerInstanceEnvironment)
	}
	leaseDuration, err := parseDuration(read(workerLeaseEnvironment), defaultLeaseDuration, outbox.MaximumLeaseDuration)
	if err != nil {
		return Config{}, fieldError(workerLeaseEnvironment)
	}
	batchSize, err := parseInteger(read(workerBatchEnvironment), defaultBatchSize, 1, outbox.MaximumBatchSize)
	if err != nil {
		return Config{}, fieldError(workerBatchEnvironment)
	}
	pollInterval, err := parseDuration(read(workerPollEnvironment), defaultPollInterval, maximumPollInterval)
	if err != nil {
		return Config{}, fieldError(workerPollEnvironment)
	}
	shutdownTimeout, err := parseDuration(read(workerShutdownEnvironment), defaultShutdown, maximumShutdown)
	if err != nil {
		return Config{}, fieldError(workerShutdownEnvironment)
	}
	return Config{
		Shared: shared, CheckpointStorage: storage,
		Runtime: RuntimeConfig{
			InstanceID: instanceID, LeaseDuration: leaseDuration, BatchSize: uint32(batchSize),
			PollInterval: pollInterval, ShutdownTimeout: shutdownTimeout,
		},
	}, nil
}

func parseDuration(value string, fallback, maximum time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > maximum {
		return 0, fmt.Errorf("invalid duration")
	}
	return parsed, nil
}

func parseInteger(value string, fallback, minimum, maximum int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("invalid integer")
	}
	return parsed, nil
}

func fieldError(name string) error { return fmt.Errorf("%s: invalid worker configuration", name) }
