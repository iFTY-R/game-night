package room

import "errors"

var (
	// ErrInvalidRoomInput rejects malformed identifiers, admission values, timestamps, or persisted snapshots.
	ErrInvalidRoomInput = errors.New("invalid room input")
	// ErrRoomVersionConflict prevents a stale client from overwriting room or membership changes.
	ErrRoomVersionConflict = errors.New("room version conflict")
	// ErrRoomStatus reports an operation that is not valid in the current continuous-room lifecycle.
	ErrRoomStatus = errors.New("room status does not permit operation")
	// ErrRoomClosed prevents a closed room from accepting new members or starting another session.
	ErrRoomClosed = errors.New("room is closed")
	// ErrAdmissionClosed reports that the requested member role is not currently admitted.
	ErrAdmissionClosed = errors.New("room admission is closed")
	// ErrRoomFull reports that no participant seat is available for the requested transition.
	ErrRoomFull = errors.New("room participant capacity is full")
	// ErrMemberNotFound hides membership lookup details from transport callers.
	ErrMemberNotFound = errors.New("room member not found")
	// ErrHostRequired is returned when a host-only command is attempted by another member.
	ErrHostRequired = errors.New("room host permission required")
	// ErrCannotRemoveHost keeps a room from losing its only owner through a member command.
	ErrCannotRemoveHost = errors.New("room host cannot be removed")
	// ErrSessionActive prevents a second authoritative game session from replacing the current one.
	ErrSessionActive = errors.New("room already has an active session")
	// ErrSessionNotFound protects finish commands from clearing a different session after a retry race.
	ErrSessionNotFound = errors.New("room session not found")
	// ErrInsufficientParticipants prevents an empty or undersized frozen participant snapshot.
	ErrInsufficientParticipants = errors.New("room has insufficient participants")
	// ErrWaitingNotFound reports an approval command for a user without a waiting membership.
	ErrWaitingNotFound = errors.New("waiting room member not found")
	// ErrRoomVersionOverflow rejects a practically impossible but unsafe uint64 wraparound.
	ErrRoomVersionOverflow = errors.New("room version overflow")
	// ErrRoomNotFound is the persistence absence result for an inaccessible room identifier or code.
	ErrRoomNotFound = errors.New("room not found")
	// ErrRoomCodeUnavailable collapses duplicate and concurrently claimed invitation codes.
	ErrRoomCodeUnavailable = errors.New("room code is unavailable")
	// ErrRoomRepositoryUnavailable hides database and generated-query diagnostics from domain callers.
	ErrRoomRepositoryUnavailable = errors.New("room repository unavailable")
	// ErrRoomIntegrity reports persisted room/member state that violates aggregate invariants.
	ErrRoomIntegrity = errors.New("room persistence integrity failure")
)
