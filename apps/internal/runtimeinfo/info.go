// Package runtimeinfo freezes the display-safe identity of one service process.
package runtimeinfo

import (
	"errors"
	"runtime/debug"
	"strings"
	"time"

	"github.com/iFTY-R/game-night/platform/admin/operations"
)

var errInvalidInfo = errors.New("invalid runtime information")

// Info is immutable process identity captured once during startup.
type Info struct {
	Kind         operations.ServiceKind
	InstanceID   string
	BuildVersion string
	StartedAt    time.Time
}

// New validates display-safe process identity and resolves a bounded build version.
func New(kind operations.ServiceKind, instanceID, injectedBuild string, startedAt time.Time) (Info, error) {
	buildVersion := resolveBuildVersion(injectedBuild)
	probe := operations.ServiceInstance{
		Kind: kind, InstanceID: instanceID, BuildVersion: buildVersion, StartedAt: startedAt.UTC(), LastHeartbeatAt: startedAt.UTC(),
		Status: operations.HealthHealthy, Components: map[string]operations.HealthStatus{}, MaintenanceVersion: 1,
	}
	if startedAt.IsZero() || !operations.ValidServiceInstance(probe) {
		return Info{}, errInvalidInfo
	}
	return Info{Kind: kind, InstanceID: instanceID, BuildVersion: buildVersion, StartedAt: startedAt.UTC()}, nil
}

func resolveBuildVersion(injected string) string {
	if value := strings.TrimSpace(injected); value != "" && len(value) <= 128 {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" && len(info.Main.Version) <= 128 {
		return info.Main.Version
	}
	return "development"
}
