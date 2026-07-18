// Package integrationtest provides shared real-service test fixtures with explicit skip and require semantics.
package integrationtest

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// Dependency names one real external service or privilege level used by integration tests.
type Dependency string

const (
	// DependencyPostgres requires the ordinary PostgreSQL test URL.
	DependencyPostgres Dependency = "postgres"
	// DependencyPostgresPrivileges requires a PostgreSQL administrator URL with role and database creation rights.
	DependencyPostgresPrivileges Dependency = "postgres-privileges"
	// DependencyRedis requires the Redis integration test URL.
	DependencyRedis Dependency = "redis"
	// DependencyObjectStorage requires the S3-compatible Object Lock test configuration.
	DependencyObjectStorage Dependency = "object-storage"
	// DependencyNginx requires the container runtime used by Nginx ingress tests.
	DependencyNginx Dependency = "nginx"
)

const requireIntegrationEnvironment = "GAME_NIGHT_REQUIRE_INTEGRATION"

// knownDependencies prevents a misspelled CI requirement from silently skipping a real integration suite.
var knownDependencies = map[Dependency]struct{}{
	DependencyPostgres:           {},
	DependencyPostgresPrivileges: {},
	DependencyRedis:              {},
	DependencyObjectStorage:      {},
	DependencyNginx:              {},
}

// RequiredDependencies parses the comma-separated CI gate without accepting misspelled dependency names.
func RequiredDependencies() (map[Dependency]struct{}, error) {
	result := make(map[Dependency]struct{})
	for _, raw := range strings.Split(os.Getenv(requireIntegrationEnvironment), ",") {
		name := Dependency(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, known := knownDependencies[name]; !known {
			return nil, fmt.Errorf("unknown dependency %q in %s", name, requireIntegrationEnvironment)
		}
		result[name] = struct{}{}
	}
	return result, nil
}

// RequireEnvironment returns required environment values, skips optional local tests, and fails closed in CI.
func RequireEnvironment(t testing.TB, dependency Dependency, names ...string) []string {
	t.Helper()

	required, err := RequiredDependencies()
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, len(names))
	missing := make([]string, 0)
	for index, name := range names {
		values[index] = strings.TrimSpace(os.Getenv(name))
		if values[index] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return values
	}
	sort.Strings(missing)
	message := fmt.Sprintf("dependency %s is missing environment variables: %s", dependency, strings.Join(missing, ", "))
	if _, mustRun := required[dependency]; mustRun {
		t.Fatal(message)
	}
	t.Skipf("SKIPPED: %s", message)
	return nil
}
