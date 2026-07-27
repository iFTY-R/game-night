package operations

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/platform/clock"
)

var dependencyOrder = [...]DependencyKind{
	DependencyPostgreSQL,
	DependencyRedis,
	DependencyExportResultStore,
	DependencyCheckpointSink,
	DependencyCheckpointProgress,
	DependencyRealtimePresence,
	DependencyRateLimiter,
}

const (
	// ServiceStaleAfter tolerates two missed 10-second heartbeats before declaring an instance stale.
	ServiceStaleAfter = 30 * time.Second
	// SnapshotFreshness matches the default heartbeat interval so callers can identify cached evidence.
	SnapshotFreshness = 10 * time.Second
	// defaultProbeTimeout prevents one dependency from consuming the whole administrator request deadline.
	defaultProbeTimeout = 500 * time.Millisecond
	// defaultSnapshotTimeout bounds all database reads and dependency probes together.
	defaultSnapshotTimeout = 2 * time.Second
)

// SnapshotRepository is the narrow PostgreSQL read surface required by operations snapshots.
type SnapshotRepository interface {
	ListServiceInstances(context.Context, uint32) ([]ServiceInstance, error)
	ListBacklogs(context.Context, time.Time) ([]BacklogSummary, error)
	GetMaintenanceState(context.Context) (MaintenanceState, error)
}

// DependencyProbe performs one bounded real dependency check without returning diagnostic text.
type DependencyProbe struct {
	Kind  DependencyKind
	Check func(context.Context) error
}

// ServiceConfig supplies the authoritative repository, clock, and fixed dependency probe set.
type ServiceConfig struct {
	Repository      SnapshotRepository
	Presence        PresenceReader
	Clock           clock.Clock
	Probes          []DependencyProbe
	ProbeTimeout    time.Duration
	SnapshotTimeout time.Duration
}

// Service aggregates current operational evidence while allowing individual dependency probes to fail.
type Service struct {
	repository      SnapshotRepository
	presence        PresenceReader
	clock           clock.Clock
	probes          []DependencyProbe
	probeTimeout    time.Duration
	snapshotTimeout time.Duration
}

// NewService validates the complete read graph and freezes deterministic dependency ordering.
func NewService(config ServiceConfig) (*Service, error) {
	if config.Repository == nil || config.Presence == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	probeTimeout := config.ProbeTimeout
	if probeTimeout == 0 {
		probeTimeout = defaultProbeTimeout
	}
	snapshotTimeout := config.SnapshotTimeout
	if snapshotTimeout == 0 {
		snapshotTimeout = defaultSnapshotTimeout
	}
	if probeTimeout <= 0 || snapshotTimeout <= 0 || probeTimeout > snapshotTimeout || len(config.Probes) > 16 {
		return nil, ErrInvalidInput
	}
	configured := make(map[DependencyKind]DependencyProbe, len(config.Probes))
	seen := make(map[DependencyKind]struct{}, len(config.Probes))
	for _, probe := range config.Probes {
		if !probe.Kind.Valid() || probe.Check == nil {
			return nil, ErrInvalidInput
		}
		if _, exists := seen[probe.Kind]; exists {
			return nil, ErrInvalidInput
		}
		seen[probe.Kind] = struct{}{}
		configured[probe.Kind] = probe
	}
	probes := make([]DependencyProbe, 0, len(configured))
	for _, kind := range dependencyOrder {
		if probe, ok := configured[kind]; ok {
			probes = append(probes, probe)
		}
	}
	return &Service{repository: config.Repository, presence: config.Presence, clock: config.Clock, probes: probes, probeTimeout: probeTimeout, snapshotTimeout: snapshotTimeout}, nil
}

// GetSnapshot reads authoritative PostgreSQL state and concurrently evaluates each configured dependency.
func (service *Service) GetSnapshot(ctx context.Context) (OperationsSnapshot, error) {
	if service == nil || ctx == nil {
		return OperationsSnapshot{}, ErrInvalidInput
	}
	requestContext, cancel := context.WithTimeout(ctx, service.snapshotTimeout)
	defer cancel()
	sampledAt := service.clock.Now()
	services, err := service.repository.ListServiceInstances(requestContext, MaximumServiceInstances)
	if err != nil {
		return OperationsSnapshot{}, mapServiceError(err)
	}
	backlogs, err := service.repository.ListBacklogs(requestContext, sampledAt)
	if err != nil {
		return OperationsSnapshot{}, mapServiceError(err)
	}
	maintenance, err := service.repository.GetMaintenanceState(requestContext)
	if err != nil {
		return OperationsSnapshot{}, mapServiceError(err)
	}
	for index := range services {
		services[index] = cloneServiceInstance(services[index])
		if sampledAt.Sub(services[index].LastHeartbeatAt) > ServiceStaleAfter {
			services[index].Status = HealthStale
		}
	}
	var dependencies []DependencyHealth
	var presence PresenceSummary
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		dependencies = service.probeDependencies(requestContext, sampledAt)
	}()
	go func() {
		defer waitGroup.Done()
		presence = service.readPresence(requestContext, sampledAt)
	}()
	waitGroup.Wait()
	return OperationsSnapshot{
		Services: services, Dependencies: dependencies, Presence: presence, Backlogs: append([]BacklogSummary(nil), backlogs...),
		Maintenance: maintenance, SampledAt: sampledAt, FreshUntil: sampledAt.Add(SnapshotFreshness),
	}, nil
}

func (service *Service) readPresence(ctx context.Context, sampledAt time.Time) PresenceSummary {
	summary, err := service.presence.ReadPresenceSummary(ctx)
	if err != nil {
		return PresenceSummary{Status: HealthUnavailable, SampledAt: sampledAt, FreshUntil: sampledAt.Add(SnapshotFreshness)}
	}
	if !summary.Status.Valid() || summary.SampledAt.IsZero() || summary.FreshUntil.Before(summary.SampledAt) {
		return PresenceSummary{Status: HealthUnavailable, SampledAt: sampledAt, FreshUntil: sampledAt.Add(SnapshotFreshness)}
	}
	if summary.FreshUntil.Before(sampledAt) {
		summary.Status = HealthStale
	}
	return summary
}

func (service *Service) probeDependencies(ctx context.Context, sampledAt time.Time) []DependencyHealth {
	results := make([]DependencyHealth, len(service.probes))
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(service.probes))
	for index, probe := range service.probes {
		go func() {
			defer waitGroup.Done()
			probeContext, cancel := context.WithTimeout(ctx, service.probeTimeout)
			defer cancel()
			status := HealthHealthy
			if probe.Check(probeContext) != nil {
				status = HealthUnavailable
			}
			results[index] = DependencyHealth{Kind: probe.Kind, Status: status, SampledAt: sampledAt, FreshUntil: sampledAt.Add(SnapshotFreshness)}
		}()
	}
	waitGroup.Wait()
	return results
}

func cloneServiceInstance(source ServiceInstance) ServiceInstance {
	clone := source
	clone.Components = make(map[string]HealthStatus, len(source.Components))
	for code, status := range source.Components {
		clone.Components[code] = status
	}
	return clone
}

func mapServiceError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIntegrity) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return err
	}
	return ErrRepositoryUnavailable
}
