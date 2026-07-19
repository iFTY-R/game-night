package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iFTY-R/game-night/platform/admin"
)

func TestReadSecretRequiresReadOnlySingleLineFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(path, []byte("a-long-bootstrap-password\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSecret(path); !errors.Is(err, ErrInvalidSecretFile) {
		t.Fatalf("writable secret error = %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	secret, mounted, err := ReadSecret(path)
	if err != nil || !mounted || secret != "a-long-bootstrap-password" {
		t.Fatalf("read secret: mounted=%t secret_length=%d err=%v", mounted, len(secret), err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadSecret(path); !errors.Is(err, ErrInvalidSecretFile) {
		t.Fatalf("multiline secret error = %v", err)
	}
}

func TestCoordinatorEnforcesMountedSecretLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(path, []byte("a-long-bootstrap-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	service := &fakeService{state: admin.SetupStateSetupRequired}
	coordinator, err := NewCoordinator(t.Context(), service, path)
	if err != nil || service.bootstrapSecret != "a-long-bootstrap-password" {
		t.Fatalf("coordinate mounted secret: secret_length=%d err=%v", len(service.bootstrapSecret), err)
	}
	if err := coordinator.Check(t.Context()); err != nil {
		t.Fatalf("setup-required mounted check: %v", err)
	}
	service.state = admin.SetupStateActive
	if err := coordinator.Check(t.Context()); !errors.Is(err, admin.ErrBootstrapSecretMismatch) {
		t.Fatalf("active mounted check error = %v", err)
	}
}

func TestCoordinatorRequiresSecretUntilBootstrapCompletes(t *testing.T) {
	service := &fakeService{state: admin.SetupStateBootstrapPending}
	if _, err := NewCoordinator(t.Context(), service, ""); !errors.Is(err, admin.ErrBootstrapSecretMismatch) {
		t.Fatalf("missing bootstrap secret error = %v", err)
	}
	service.state = admin.SetupStateActive
	coordinator, err := NewCoordinator(t.Context(), service, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Check(t.Context()); err != nil {
		t.Fatalf("active unmounted check: %v", err)
	}
}

type fakeService struct {
	state           admin.SetupState
	bootstrapSecret string
}

func (service *fakeService) BootstrapPassword(_ context.Context, secret string) error {
	service.bootstrapSecret = secret
	return nil
}

func (service *fakeService) BootstrapReadyWithoutSecret(context.Context) error {
	if service.state == admin.SetupStateBootstrapPending {
		return admin.ErrBootstrapSecretMismatch
	}
	return nil
}

func (service *fakeService) GetSetupState(context.Context) (admin.SetupState, error) {
	return service.state, nil
}
