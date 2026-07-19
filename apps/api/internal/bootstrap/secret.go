// Package bootstrap coordinates the one-time administrator password mount with durable account state.
package bootstrap

import (
	"context"
	"errors"

	"github.com/iFTY-R/game-night/apps/internal/secretfile"
	"github.com/iFTY-R/game-night/platform/admin"
)

// ErrInvalidSecretFile hides secret paths, contents, and filesystem diagnostics from process output.
var ErrInvalidSecretFile = errors.New("invalid administrator bootstrap secret file")

// Service is the narrow administrator lifecycle surface needed during startup and readiness checks.
type Service interface {
	BootstrapPassword(context.Context, string) error
	BootstrapReadyWithoutSecret(context.Context) error
	GetSetupState(context.Context) (admin.SetupState, error)
}

// Coordinator remembers only whether a secret was mounted; plaintext is released after startup coordination.
type Coordinator struct {
	service Service
	mounted bool
}

// NewCoordinator securely reads an optional mount and proves it is consistent with the durable singleton state.
func NewCoordinator(ctx context.Context, service Service, path string) (*Coordinator, error) {
	if ctx == nil || service == nil {
		return nil, ErrInvalidSecretFile
	}
	secret, mounted, err := ReadSecret(path)
	if err != nil {
		return nil, err
	}
	if mounted {
		err = service.BootstrapPassword(ctx, secret)
	} else {
		err = service.BootstrapReadyWithoutSecret(ctx)
	}
	secret = ""
	if err != nil {
		return nil, err
	}
	return &Coordinator{service: service, mounted: mounted}, nil
}

// Check fails once setup becomes active while the process was started with the one-time secret still mounted.
func (coordinator *Coordinator) Check(ctx context.Context) error {
	if coordinator == nil || coordinator.service == nil || ctx == nil {
		return ErrInvalidSecretFile
	}
	state, err := coordinator.service.GetSetupState(ctx)
	if err != nil {
		return err
	}
	if coordinator.mounted {
		if state != admin.SetupStateSetupRequired {
			return admin.ErrBootstrapSecretMismatch
		}
		return nil
	}
	if state == admin.SetupStateBootstrapPending {
		return admin.ErrBootstrapSecretMismatch
	}
	return nil
}

// ReadSecret accepts one read-only regular file and removes only its conventional final line ending.
func ReadSecret(path string) (string, bool, error) {
	secret, mounted, err := secretfile.Read(path)
	if err != nil {
		return "", false, ErrInvalidSecretFile
	}
	return secret, mounted, nil
}
