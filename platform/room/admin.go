package room

import (
	"time"

	"github.com/google/uuid"
)

// AdminActor identifies the operator for audit/event attribution only; authorization stays in admin services.
type AdminActor struct {
	ID uuid.UUID
}

// SetAdmissionByAdmin changes lobby admission without pretending to be the room host.
func (room Room) SetAdmissionByAdmin(actor AdminActor, participant, spectator AdmissionMode, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if actor.ID == uuid.Nil || !participant.Valid() || !spectator.Valid() || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if !room.snapshot.Status.admissionMutable() {
		return Room{}, ErrRoomStatus
	}
	next := room.snapshot
	next.ParticipantAdmission, next.SpectatorAdmission = participant, spectator
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}

// RemoveMemberByAdmin removes a non-host member while preserving the active-session revocation signal.
func (room Room) RemoveMemberByAdmin(actor AdminActor, userID uuid.UUID, expected Version, at time.Time) (Room, RemovalResult, error) {
	at = canonicalRoomTime(at)
	if actor.ID == uuid.Nil || userID == uuid.Nil || at.IsZero() {
		return Room{}, RemovalResult{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, RemovalResult{}, err
	}
	if userID == room.snapshot.HostUserID {
		return Room{}, RemovalResult{}, ErrCannotRemoveHost
	}
	next := room.snapshot
	removedIndex := -1
	var removed MemberSnapshot
	for index, member := range next.Members {
		if member.UserID == userID {
			removedIndex, removed = index, member
			break
		}
	}
	if removedIndex < 0 {
		return Room{}, RemovalResult{}, ErrMemberNotFound
	}
	next.Members = append(next.Members[:removedIndex], next.Members[removedIndex+1:]...)
	if next.PendingPauseRequest.RequestedByUserID == userID {
		next.PendingPauseRequest = PendingPauseRequest{}
	}
	if err := bumpVersions(&next, true, at); err != nil {
		return Room{}, RemovalResult{}, err
	}
	updated, err := Restore(next)
	if err != nil {
		return Room{}, RemovalResult{}, err
	}
	return updated, RemovalResult{
		Removed: removed, ParticipantRevoked: room.snapshot.Status == RoomStatusPlaying && removed.Role == MemberRoleParticipant,
		SessionID: room.snapshot.ActiveSessionID, Version: updated.Version(),
	}, nil
}

// CloseWaitingByAdmin permanently closes a non-playing room; active sessions must be terminated through the runtime path.
func (room Room) CloseWaitingByAdmin(actor AdminActor, expected Version, at time.Time) (Room, error) {
	at = canonicalRoomTime(at)
	if actor.ID == uuid.Nil || at.IsZero() {
		return Room{}, ErrInvalidRoomInput
	}
	if err := room.checkVersion(expected); err != nil {
		return Room{}, err
	}
	if room.snapshot.Status == RoomStatusPlaying {
		return Room{}, ErrSessionActive
	}
	if room.snapshot.Status == RoomStatusClosed {
		return room, nil
	}
	next := room.snapshot
	next.Status, next.ParticipantAdmission, next.SpectatorAdmission = RoomStatusClosed, AdmissionClosed, AdmissionClosed
	next.PendingPauseRequest, next.ActivePause = PendingPauseRequest{}, ActivePause{}
	if err := bumpVersions(&next, false, at); err != nil {
		return Room{}, err
	}
	return Restore(next)
}
