package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRejectsForbiddenFixture(t *testing.T) {
	fixture, err := filepath.Abs("../../testdata/boundaries/forbidden")
	if err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, fixture)
	if err == nil {
		t.Fatalf("expected CLI failure, output: %s", output)
	}
	for _, expected := range []string{"platform modules cannot import concrete games", "games cannot import application entrypoints", "game engines cannot own IO"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in CLI output: %s", expected, output)
		}
	}
}

func TestCLIAcceptsRepository(t *testing.T) {
	repository, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, repository)
	if err != nil {
		t.Fatalf("expected repository success, got %v: %s", err, output)
	}
	if !strings.Contains(output, "dependency boundaries passed (") {
		t.Fatalf("missing success summary in CLI output: %s", output)
	}
}

func TestCLIReportsDiscoveryFailure(t *testing.T) {
	root := t.TempDir()
	output, err := runCLI(t, root)
	if err == nil {
		t.Fatalf("expected discovery failure, output: %s", output)
	}
	if !strings.Contains(output, "go list -json ./...") || !strings.Contains(output, root) {
		t.Fatalf("expected command and root context in CLI output: %s", output)
	}
}

func runCLI(t *testing.T, root string) (string, error) {
	t.Helper()

	cmd := exec.Command("go", "run", ".", "-root", root)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}
