package gameruntime

import "errors"

var (
	// ErrInvalidSessionInput rejects malformed session state, transitions, and persistence commands.
	ErrInvalidSessionInput = errors.New("invalid game session input")
	// ErrSessionNotFound is the stable absence result for a requested authoritative session.
	ErrSessionNotFound = errors.New("game session not found")
	// ErrSessionAlreadyExists rejects reuse of a session or initial event-batch identity.
	ErrSessionAlreadyExists = errors.New("game session already exists")
	// ErrStartReceiptNotFound distinguishes a new start operation from a durable idempotent replay.
	ErrStartReceiptNotFound = errors.New("game session start receipt not found")
	// ErrStateVersionConflict rejects a transition that did not start from the persisted authoritative version.
	ErrStateVersionConflict = errors.New("game session state version conflict")
	// ErrOwnershipLost fences a runtime whose PostgreSQL ownership epoch is no longer current.
	ErrOwnershipLost = errors.New("game session ownership lost")
	// ErrSessionSuspended prevents game actions while exact-version recovery has paused a session.
	ErrSessionSuspended = errors.New("game session is suspended")
	// ErrSessionTerminal prevents any further runtime transition after finish or cancellation.
	ErrSessionTerminal = errors.New("game session is terminal")
	// ErrParticipantNotActive rejects an action after PartyRoom membership or the active-session pointer changed.
	ErrParticipantNotActive = errors.New("game participant is not active in the room")
	// ErrTimerNotDue rejects an early firing without consuming or replacing the persisted timer.
	ErrTimerNotDue = errors.New("game timer is not due")
	// ErrActionReceiptNotFound reports that PostgreSQL has no durable result for one scoped action ID.
	ErrActionReceiptNotFound = errors.New("game action receipt not found")
	// ErrTimerNotFound reports that a timer firing does not match the current persisted timer set.
	ErrTimerNotFound = errors.New("game timer not found")
	// ErrTimerReceiptNotFound reports that no durable result exists for one exact scheduled timer firing.
	ErrTimerReceiptNotFound = errors.New("game timer receipt not found")
	// ErrSystemReceiptNotFound reports that no durable result exists for one system operation and source.
	ErrSystemReceiptNotFound = errors.New("game system receipt not found")
	// ErrSystemOperationPending asks the runtime to reload state and recompute the same logical system operation.
	ErrSystemOperationPending = errors.New("game system operation is pending")
	// ErrInvalidActionCommit rejects a receipt, batch, state, or outbox combination that is not one transition.
	ErrInvalidActionCommit = errors.New("invalid game action commit")
	// ErrInvalidTimerCommit rejects a timer receipt, batch, state, or outbox combination that is not one transition.
	ErrInvalidTimerCommit = errors.New("invalid game timer commit")
	// ErrInvalidSystemCommit rejects a system receipt, batch, state, or outbox combination that is not one transition.
	ErrInvalidSystemCommit = errors.New("invalid game system commit")
	// ErrInvalidLifecycleCommit rejects a suspend or cancel write that changes engine state or session identity.
	ErrInvalidLifecycleCommit = errors.New("invalid game session lifecycle commit")
	// ErrModuleUnavailable means the exact retained module is missing or lacks the complete runtime contract.
	ErrModuleUnavailable = errors.New("game module is unavailable")
	// ErrProjectionUnsafe rejects module output that violates the viewer-safe projection contract.
	ErrProjectionUnsafe = errors.New("game projection is unsafe")
	// ErrReplayUnavailable rejects live, incomplete, oversized, or unsupported replay histories.
	ErrReplayUnavailable = errors.New("game replay is unavailable")
	// ErrGameSessionRepositoryUnavailable hides database and driver details from runtime callers.
	ErrGameSessionRepositoryUnavailable = errors.New("game session repository unavailable")
	// ErrGameSessionIntegrity reports persisted rows that cannot restore a valid authoritative aggregate.
	ErrGameSessionIntegrity = errors.New("game session persistence integrity failure")
)
