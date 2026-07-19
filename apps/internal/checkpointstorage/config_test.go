package checkpointstorage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/platform/persistence/objectstorage"
)

func TestLoadUsesLocalSinkOnlyOutsideProduction(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "checkpoints")
	config, err := Load(environment(map[string]string{
		checkpointSinkEnvironment:           string(SinkLocal),
		checkpointLocalDirectoryEnvironment: directory,
	}), sharedconfig.EnvironmentDevelopment)
	if err != nil {
		t.Fatal(err)
	}
	if config.Kind != SinkLocal || config.LocalDirectory != directory {
		t.Fatalf("unexpected local config: %+v", config)
	}

	_, err = Load(environment(map[string]string{
		checkpointSinkEnvironment:           string(SinkLocal),
		checkpointLocalDirectoryEnvironment: directory,
	}), sharedconfig.EnvironmentProduction)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("production local sink error = %v, want ErrInvalidConfig", err)
	}
}

func TestLoadRequiresBoundedS3ObjectLockSettings(t *testing.T) {
	values := map[string]string{
		checkpointSinkEnvironment:        string(SinkS3),
		checkpointS3RegionEnvironment:    "us-east-1",
		checkpointS3BucketEnvironment:    "game-night-audit",
		checkpointS3RetentionEnvironment: "8760h",
	}
	config, err := Load(environment(values), sharedconfig.EnvironmentProduction)
	if err != nil {
		t.Fatal(err)
	}
	if config.Kind != SinkS3 || config.S3.Retention != 365*24*time.Hour {
		t.Fatalf("unexpected S3 config: %+v", config)
	}

	values[checkpointS3EndpointEnvironment] = "http://minio.test"
	if _, err := Load(environment(values), sharedconfig.EnvironmentProduction); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("plaintext production endpoint error = %v, want ErrInvalidConfig", err)
	}
}

func TestReadinessCachesProductionProbeAndAllowsExplicitLocalPolicy(t *testing.T) {
	productionSink := &probeSink{}
	probe, err := NewReadiness(sharedconfig.EnvironmentProduction, productionSink, time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Ready(t.Context()) || !probe.Ready(t.Context()) || productionSink.calls != 1 {
		t.Fatalf("cached readiness calls = %d", productionSink.calls)
	}

	localSink := &probeSink{err: objectstorage.ErrNonProductionSink}
	localProbe, err := NewReadiness(sharedconfig.EnvironmentDevelopment, localSink, time.Minute, time.Second)
	if err != nil || !localProbe.Ready(t.Context()) {
		t.Fatalf("development local readiness = %v, err=%v", localProbe.Ready(t.Context()), err)
	}
	productionProbe, err := NewReadiness(sharedconfig.EnvironmentProduction, localSink, time.Minute, time.Second)
	if err != nil || productionProbe.Ready(t.Context()) {
		t.Fatal("non-production sink satisfied production readiness")
	}
}

type probeSink struct {
	calls int
	err   error
}

func (*probeSink) Write(context.Context, objectstorage.Object) error { return nil }

func (sink *probeSink) CheckProductionReady(context.Context) error {
	sink.calls++
	return sink.err
}

func environment(values map[string]string) sharedconfig.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
