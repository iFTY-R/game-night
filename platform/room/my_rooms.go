package room

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MyRoomPageCursor preserves host-first ordering across descending keyset pages.
type MyRoomPageCursor struct {
	IsHost    bool
	UpdatedAt time.Time
	RoomID    uuid.UUID
}

// MyRoomListRequest is the validated member-room query. Limit includes one lookahead row.
type MyRoomListRequest struct {
	ActorUserID uuid.UUID
	After       MyRoomPageCursor
	PageSize    uint32
	Limit       uint32
}

// MyRoomCardSnapshot contains private member-safe fields needed to resume a room from any device.
type MyRoomCardSnapshot struct {
	RoomID               uuid.UUID
	RoomCode             string
	Visibility           Visibility
	HostUsername         string
	Status               RoomStatus
	IsHost               bool
	ParticipantCapacity  uint32
	ParticipantCount     uint32
	SpectatorCount       uint32
	WaitingCount         uint32
	ParticipantAdmission AdmissionMode
	SpectatorAdmission   AdmissionMode
	ActiveGameID         string
	LastFinishedGameID   string
	ViewerRole           MemberRole
	ViewerRequestedRole  MemberRole
	UpdatedAt            time.Time
}

// MyRoomCard is a validated projection visible only to the current room member.
type MyRoomCard struct {
	snapshot MyRoomCardSnapshot
}

// MyRoomPage returns host-owned rooms first and carries the last visible card as its next cursor.
type MyRoomPage struct {
	Rooms      []MyRoomCard
	NextCursor MyRoomPageCursor
}

// MyRoomRepository reads all active public or private rooms containing the authenticated actor.
type MyRoomRepository interface {
	ListMyRooms(context.Context, MyRoomListRequest) ([]MyRoomCard, error)
}

// NewMyRoomListRequest validates one bounded host-first page request.
func NewMyRoomListRequest(actorUserID uuid.UUID, after MyRoomPageCursor, pageSize uint32) (MyRoomListRequest, error) {
	if actorUserID == uuid.Nil || (after.UpdatedAt.IsZero() != (after.RoomID == uuid.Nil)) {
		return MyRoomListRequest{}, ErrInvalidRoomInput
	}
	if pageSize == 0 {
		pageSize = DefaultPublicRoomPageSize
	}
	if pageSize > MaximumPublicRoomPageSize {
		return MyRoomListRequest{}, ErrInvalidRoomInput
	}
	if !after.UpdatedAt.IsZero() {
		after.UpdatedAt = canonicalRoomTime(after.UpdatedAt)
		if after.UpdatedAt.UnixNano() <= 0 {
			return MyRoomListRequest{}, ErrInvalidRoomInput
		}
	}
	return MyRoomListRequest{
		ActorUserID: actorUserID,
		After:       after,
		PageSize:    pageSize,
		Limit:       pageSize + 1,
	}, nil
}

// Valid lets persistence reject requests that bypassed the domain constructor.
func (request MyRoomListRequest) Valid() bool {
	validated, err := NewMyRoomListRequest(request.ActorUserID, request.After, request.PageSize)
	return err == nil && validated == request
}

// RestoreMyRoomCard validates one membership projection before transport can expose invitation data.
func RestoreMyRoomCard(snapshot MyRoomCardSnapshot) (MyRoomCard, error) {
	snapshot.UpdatedAt = canonicalRoomTime(snapshot.UpdatedAt)
	if snapshot.RoomID == uuid.Nil || validateRoomCode(snapshot.RoomCode) != nil || !snapshot.Visibility.Valid() ||
		snapshot.HostUsername == "" || strings.TrimSpace(snapshot.HostUsername) != snapshot.HostUsername ||
		(snapshot.Status != RoomStatusLobby && snapshot.Status != RoomStatusPlaying && snapshot.Status != RoomStatusPostGame) ||
		snapshot.ParticipantCapacity == 0 || snapshot.ParticipantCount == 0 || snapshot.ParticipantCount > snapshot.ParticipantCapacity ||
		!snapshot.ParticipantAdmission.Valid() || !snapshot.SpectatorAdmission.Valid() || !snapshot.ViewerRole.Valid() || snapshot.UpdatedAt.IsZero() {
		return MyRoomCard{}, ErrRoomIntegrity
	}
	if snapshot.Status == RoomStatusPlaying {
		if snapshot.ActiveGameID == "" || snapshot.ParticipantAdmission != AdmissionClosed || snapshot.LastFinishedGameID != "" {
			return MyRoomCard{}, ErrRoomIntegrity
		}
	} else if snapshot.ActiveGameID != "" || (snapshot.Status == RoomStatusPostGame && snapshot.LastFinishedGameID == "") {
		return MyRoomCard{}, ErrRoomIntegrity
	}
	if snapshot.IsHost && snapshot.ViewerRole != MemberRoleParticipant {
		return MyRoomCard{}, ErrRoomIntegrity
	}
	if snapshot.ViewerRole == MemberRoleWaiting {
		if snapshot.ViewerRequestedRole != MemberRoleParticipant && snapshot.ViewerRequestedRole != MemberRoleSpectator {
			return MyRoomCard{}, ErrRoomIntegrity
		}
	} else if snapshot.ViewerRequestedRole != "" {
		return MyRoomCard{}, ErrRoomIntegrity
	}
	return MyRoomCard{snapshot: snapshot}, nil
}

// Snapshot returns immutable fields for persistence cursors and transport mapping.
func (card MyRoomCard) Snapshot() MyRoomCardSnapshot {
	return card.snapshot
}
