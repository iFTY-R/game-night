package operations

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
)

type snapshotRepositoryStub struct {
	services    []ServiceInstance
	backlogs    []BacklogSummary
	maintenance MaintenanceState
	err         error
}

func (repository *snapshotRepositoryStub) ListServiceInstances(context.Context, uint32) ([]ServiceInstance, error) {
	if repository.err != nil {
		return nil, repository.err
	}
	return repository.services, nil
}

func (repository *snapshotRepositoryStub) ListBacklogs(context.Context, time.Time) ([]BacklogSummary, error) {
	return repository.backlogs, nil
}

func (repository *snapshotRepositoryStub) GetMaintenanceState(context.Context) (MaintenanceState, error) {
	return repository.maintenance, nil
}

func TestServiceGetSnapshotMarksOnlyExpiredInstancesStaleAndClonesComponents(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	components := map[string]HealthStatus{"postgresql": HealthHealthy}
	repository := &snapshotRepositoryStub{
		services: []ServiceInstance{
			{Kind: ServiceAPI, InstanceID: "api-1", LastHeartbeatAt: now.Add(-ServiceStaleAfter), Status: HealthHealthy, Components: components},
			{Kind: ServiceWorker, InstanceID: "worker-1", LastHeartbeatAt: now.Add(-ServiceStaleAfter - time.Nanosecond), Status: HealthHealthy},
		},
		maintenance: MaintenanceState{Scope: MaintenanceUserMutations, Version: 1, ChangedByAdminID: uuid.New(), ChangedAt: now.Add(-time.Hour)},
	}
	service, err := NewService(ServiceConfig{Repository: repository, Presence: healthyPresence(now), Clock: clock.NewFake(now)})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.Services[0].Status != HealthHealthy || snapshot.Services[1].Status != HealthStale {
		t.Fatalf("service statuses = %q, %q", snapshot.Services[0].Status, snapshot.Services[1].Status)
	}
	snapshot.Services[0].Components["postgresql"] = HealthUnavailable
	if components["postgresql"] != HealthHealthy {
		t.Fatal("snapshot exposed the repository-owned component map")
	}
	if !snapshot.SampledAt.Equal(now) || !snapshot.FreshUntil.Equal(now.Add(SnapshotFreshness)) {
		t.Fatalf("snapshot freshness = %v through %v", snapshot.SampledAt, snapshot.FreshUntil)
	}
}

func TestServiceOrdersDependencyProbesAndPreservesPartialFailure(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	repository := &snapshotRepositoryStub{maintenance: MaintenanceState{Scope: MaintenanceUserMutations, Version: 1}}
	service, err := NewService(ServiceConfig{
		Repository: repository,
		Presence:   healthyPresence(now),
		Clock:      clock.NewFake(now),
		Probes: []DependencyProbe{
			{Kind: DependencyRateLimiter, Check: func(context.Context) error { return nil }},
			{Kind: DependencyRedis, Check: func(context.Context) error { return errors.New("redacted") }},
			{Kind: DependencyPostgreSQL, Check: func(context.Context) error { return nil }},
		},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	wantKinds := []DependencyKind{DependencyPostgreSQL, DependencyRedis, DependencyRateLimiter}
	gotKinds := make([]DependencyKind, len(snapshot.Dependencies))
	for index, dependency := range snapshot.Dependencies {
		gotKinds[index] = dependency.Kind
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("dependency order = %v, want %v", gotKinds, wantKinds)
	}
	if snapshot.Dependencies[0].Status != HealthHealthy || snapshot.Dependencies[1].Status != HealthUnavailable || snapshot.Dependencies[2].Status != HealthHealthy {
		t.Fatalf("dependency statuses = %+v", snapshot.Dependencies)
	}
}

func TestServiceBoundsEachDependencyProbe(t *testing.T) {
	now := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	repository := &snapshotRepositoryStub{maintenance: MaintenanceState{Scope: MaintenanceUserMutations, Version: 1}}
	service, err := NewService(ServiceConfig{
		Repository: repository,
		Presence:   healthyPresence(now),
		Clock:      clock.NewFake(now),
		Probes: []DependencyProbe{{Kind: DependencyPostgreSQL, Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}},
		ProbeTimeout:    10 * time.Millisecond,
		SnapshotTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	startedAt := time.Now()
	snapshot, err := service.GetSnapshot(context.Background())
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("probe exceeded bounded timeout: %v", elapsed)
	}
	if snapshot.Dependencies[0].Status != HealthUnavailable {
		t.Fatalf("timed-out dependency status = %q", snapshot.Dependencies[0].Status)
	}
}

func TestServiceMapsRepositoryFailures(t *testing.T) {
	service, err := NewService(ServiceConfig{
		Repository: &snapshotRepositoryStub{err: errors.New("database unavailable")},
		Presence:   healthyPresence(time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)),
		Clock:      clock.NewFake(time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.GetSnapshot(context.Background())
	if !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("GetSnapshot() error = %v, want %v", err, ErrRepositoryUnavailable)
	}
}

func healthyPresence(now time.Time) presenceReaderStub {
	return presenceReaderStub{summary: PresenceSummary{Status: HealthHealthy, SampledAt: now, FreshUntil: now.Add(time.Minute)}}
}
