package gameruntime

import "errors"

var (
	// ErrInvalidSessionInput rejects malformed session state, transitions, and persistence commands.
	ErrInvalidSessionInput = errors.New("invalid game session input")
	// ErrSessionNotFound is the stable absence result for a requested authoritative session.
	ErrSessionNotFound = errors.New("game session not found")
	// ErrSessionAlreadyExists rejects reuse of a session or initial event-batch identity.
	ErrSessionAlreadyExists = errors.New("game session already exists")
	// ErrStateVersionConflict rejects a transition that did not start from the persisted authoritative version.
	ErrStateVersionConflict = errors.New("game session state version conflict")
	// ErrOwnershipLost fences a runtime whose PostgreSQL ownership epoch is no longer current.
	ErrOwnershipLost = errors.New("game session ownership lost")
	// ErrSessionSuspended prevents game actions while exact-version recovery has paused a session.
	ErrSessionSuspended = errors.New("game session is suspended")
	// ErrSessionTerminal prevents any further runtime transition after finish or cancellation.
	ErrSessionTerminal = errors.New("game session is terminal")
	// ErrActionReceiptNotFound reports that PostgreSQL has no durable result for one scoped action ID.
	ErrActionReceiptNotFound = errors.New("game action receipt not found")
	// ErrInvalidActionCommit rejects a receipt, batch, state, or outbox combination that is not one transition.
	ErrInvalidActionCommit = errors.New("invalid game action commit")
	// ErrGameSessionRepositoryUnavailable hides database and driver details from runtime callers.
	ErrGameSessionRepositoryUnavailable = errors.New("game session repository unavailable")
	// ErrGameSessionIntegrity reports persisted rows that cannot restore a valid authoritative aggregate.
	ErrGameSessionIntegrity = errors.New("game session persistence integrity failure")
)
