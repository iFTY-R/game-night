package integrationtest

import (
	"strings"
	"testing"
)

func TestRequiredDependencies(t *testing.T) {
	t.Setenv(requireIntegrationEnvironment, " redis,postgres,redis ")

	got, err := RequiredDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected duplicate dependencies to collapse, got %#v", got)
	}
	for _, dependency := range []Dependency{DependencyPostgres, DependencyRedis} {
		if _, exists := got[dependency]; !exists {
			t.Errorf("expected %s to be required", dependency)
		}
	}
}

func TestRequiredDependenciesRejectsUnknownName(t *testing.T) {
	t.Setenv(requireIntegrationEnvironment, "postgres,typo")

	_, err := RequiredDependencies()
	if err == nil || !strings.Contains(err.Error(), "unknown dependency") {
		t.Fatalf("expected an unknown dependency error, got %v", err)
	}
}

func TestRequiredDependenciesAllowsEmptyConfiguration(t *testing.T) {
	t.Setenv(requireIntegrationEnvironment, "")

	got, err := RequiredDependencies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no required dependencies, got %#v", got)
	}
}
