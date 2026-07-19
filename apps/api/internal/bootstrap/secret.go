// Package bootstrap coordinates the one-time administrator password mount with durable account state.
package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"unicode/utf8"

	"github.com/iFTY-R/game-night/platform/admin"
)

const maximumSecretFileBytes = 4 << 10

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
	if path == "" {
		return "", false, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !secureSecretFileMode(info.Mode()) {
		return "", false, ErrInvalidSecretFile
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, ErrInvalidSecretFile
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !secureSecretFileMode(openedInfo.Mode()) || !os.SameFile(info, openedInfo) {
		return "", false, ErrInvalidSecretFile
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumSecretFileBytes+1))
	if err != nil || len(contents) > maximumSecretFileBytes {
		return "", false, ErrInvalidSecretFile
	}
	contents = bytes.TrimSuffix(contents, []byte("\r\n"))
	contents = bytes.TrimSuffix(contents, []byte("\n"))
	if len(contents) == 0 || !utf8.Valid(contents) || bytes.ContainsAny(contents, "\x00\r\n") {
		return "", false, ErrInvalidSecretFile
	}
	return string(contents), true, nil
}

func secureSecretFileMode(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return mode.Perm()&0o222 == 0
	}
	return mode.Perm() == 0o400
}
