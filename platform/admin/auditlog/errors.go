// Package auditlog provides the permission-gated, redacted administration view of the signed audit chain.
package auditlog

import "errors"

var (
	// ErrInvalidInput rejects malformed filter, page, and cursor input before an audit repository is queried.
	ErrInvalidInput = errors.New("invalid admin audit input")
	// ErrPermissionDenied prevents callers from inferring audit-chain contents without the audit.read capability.
	ErrPermissionDenied = errors.New("admin audit permission denied")
	// ErrRepositoryUnavailable hides database and verification implementation details from the management transport.
	ErrRepositoryUnavailable = errors.New("admin audit repository unavailable")
)
