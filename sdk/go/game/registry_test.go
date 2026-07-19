package game

import (
	"context"
	"errors"
	"testing"
)

func TestRegistryRetainsExactVersionsAndUsesExplicitDefaultOnlyForNewSessions(t *testing.T) {
	oldModule := &testModule{manifest: validManifest("dice", "1.0.0")}
	newModule := &testModule{manifest: validManifest("dice", "2.0.0")}
	registry, err := NewRegistry(
		Registration{Module: oldModule},
		Registration{Module: newModule, Default: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Resolve(oldModule.manifest.Key())
	if err != nil || resolved != oldModule {
		t.Fatalf("resolve old module = %T, err = %v", resolved, err)
	}
	defaultManifest, err := registry.DefaultManifest(t.Context(), "dice")
	if err != nil || defaultManifest.Versions.Engine != "2.0.0" {
		t.Fatalf("default manifest = %+v, err = %v", defaultManifest, err)
	}
	defaultManifest.Themes.Variants[0] = "mutated"
	stored, err := registry.Manifest(newModule.manifest.Key())
	if err != nil || stored.Themes.Variants[0] != "classic" {
		t.Fatalf("registry manifest mutated: %+v, err = %v", stored, err)
	}
	if len(registry.Manifests()) != 2 {
		t.Fatalf("manifests = %+v", registry.Manifests())
	}
}

func TestRegistryRejectsDuplicateMissingAndAmbiguousDefaults(t *testing.T) {
	module := &testModule{manifest: validManifest("dice", "1.0.0")}
	if _, err := NewRegistry(Registration{Module: module}); !errors.Is(err, ErrDefaultRegistration) {
		t.Fatalf("missing default error = %v", err)
	}
	if _, err := NewRegistry(
		Registration{Module: module, Default: true}, Registration{Module: module},
	); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	other := &testModule{manifest: validManifest("dice", "2.0.0")}
	if _, err := NewRegistry(
		Registration{Module: module, Default: true}, Registration{Module: other, Default: true},
	); !errors.Is(err, ErrDefaultRegistration) {
		t.Fatalf("ambiguous default error = %v", err)
	}
}

func TestRegistryNeverFallsBackForMissingExactVersion(t *testing.T) {
	module := &testModule{manifest: validManifest("texas-holdem", "1.0.0")}
	registry, err := NewRegistry(Registration{Module: module, Default: true})
	if err != nil {
		t.Fatal(err)
	}
	missing := module.manifest.Key()
	missing.Protocol = "2.0.0"
	if _, err := registry.Resolve(missing); !errors.Is(err, ErrVersionNotRegistered) {
		t.Fatalf("missing exact version error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := registry.DefaultManifest(canceled, "texas-holdem"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error = %v", err)
	}
}

type testModule struct{ manifest Manifest }

func (module *testModule) Manifest() Manifest                { return module.manifest.Clone() }
func (*testModule) Create(CreateRequest) (Transition, error) { return Transition{}, nil }
func (*testModule) HandleCommand(Snapshot, CommandRequest) (Transition, error) {
	return Transition{}, nil
}
func (*testModule) HandleTimer(Snapshot, TimerRequest) (Transition, error) {
	return Transition{}, nil
}
func (*testModule) Project(Snapshot, Viewer) (Projection, error) { return Projection{}, nil }
func (*testModule) ProjectReplay([]Event, Viewer, ReplayAccessPolicy) (Projection, error) {
	return Projection{}, nil
}
func (*testModule) Migrate(Snapshot, uint32, uint32) (Snapshot, error) { return Snapshot{}, nil }

var _ ServerGameModule = (*testModule)(nil)
