package room

import "errors"

var (
	ErrInvalidInput          = errors.New("admin room: invalid input")
	ErrNotFound              = errors.New("admin room: not found")
	ErrConflict              = errors.New("admin room: version conflict")
	ErrIntegrity             = errors.New("admin room: integrity violation")
	ErrRepositoryUnavailable = errors.New("admin room: repository unavailable")
	ErrPermissionDenied      = errors.New("admin room: permission denied")
)
