package operations

import "errors"

var (
	// ErrInvalidInput rejects values outside the closed operations contract before persistence.
	ErrInvalidInput = errors.New("admin operations input is invalid")
	// ErrNotFound reports an absent reviewed resource without exposing storage details.
	ErrNotFound = errors.New("admin operations resource not found")
	// ErrConflict reports a lost version or idempotency race.
	ErrConflict = errors.New("admin operations version conflict")
	// ErrIntegrity reports persisted data that violates the domain model.
	ErrIntegrity = errors.New("admin operations data integrity failure")
	// ErrRepositoryUnavailable hides database implementation errors from callers.
	ErrRepositoryUnavailable = errors.New("admin operations repository unavailable")
	// ErrPermissionDenied rejects command callers without operations.maintain.
	ErrPermissionDenied = errors.New("admin operations permission denied")
	// ErrElevationRequired rejects commands without a live operations.maintenance grant.
	ErrElevationRequired = errors.New("admin operations elevation required")
	// ErrPreviewExpired rejects a consumed or expired reviewed command snapshot.
	ErrPreviewExpired = errors.New("admin operations preview expired")
	// ErrIdempotencyConflict rejects reuse of an operation ID for different command content.
	ErrIdempotencyConflict = errors.New("admin operations idempotency conflict")
	// ErrAuditUnavailable fails sensitive writes closed when signed audit cannot commit.
	ErrAuditUnavailable = errors.New("admin operations audit unavailable")
	// ErrRetryLimit rejects further manual attempts after the reviewed per-task cap.
	ErrRetryLimit = errors.New("admin operations manual retry limit reached")
)
