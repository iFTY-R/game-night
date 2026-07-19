package room

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// DefaultPublicRoomPageSize keeps the first mobile lobby load bounded when the client omits a preference.
	DefaultPublicRoomPageSize uint32 = 20
	// MaximumPublicRoomPageSize caps one aggregated lobby query and its response allocation.
	MaximumPublicRoomPageSize uint32 = 100
	// maximumPublicRoomGameIDBytes bounds an exact-match filter before it reaches PostgreSQL.
	maximumPublicRoomGameIDBytes = 128
)

// PublicRoomPrimaryAction is the server-authoritative main action rendered on a public room card.
type PublicRoomPrimaryAction string

const (
	PublicRoomPrimaryActionEnterRoom       PublicRoomPrimaryAction = "enter_room"
	PublicRoomPrimaryActionJoin            PublicRoomPrimaryAction = "join"
	PublicRoomPrimaryActionRequestJoin     PublicRoomPrimaryAction = "request_join"
	PublicRoomPrimaryActionSpectate        PublicRoomPrimaryAction = "spectate"
	PublicRoomPrimaryActionRequestSpectate PublicRoomPrimaryAction = "request_spectate"
	PublicRoomPrimaryActionWaitForHost     PublicRoomPrimaryAction = "wait_for_host"
	PublicRoomPrimaryActionInProgress      PublicRoomPrimaryAction = "in_progress"
	PublicRoomPrimaryActionFull            PublicRoomPrimaryAction = "full"
)

// PublicRoomFilter contains only stable server-side lobby filters. Empty fields mean no filter.
type PublicRoomFilter struct {
	Statuses                []RoomStatus
	GameID                  string
	ParticipantJoinableOnly bool
}

// PublicRoomPageCursor is the exclusive `(updated_at, room_id)` position for descending keyset pagination.
type PublicRoomPageCursor struct {
	UpdatedAt time.Time
	RoomID    uuid.UUID
}

// PublicRoomListRequest is the validated read-model query. Limit includes one lookahead row for next-page detection.
type PublicRoomListRequest struct {
	ActorUserID uuid.UUID
	Filter      PublicRoomFilter
	After       PublicRoomPageCursor
	PageSize    uint32
	Limit       uint32
}

// PublicRoomCardSnapshot contains only lobby-safe fields and the current viewer's own membership state.
type PublicRoomCardSnapshot struct {
	RoomID               uuid.UUID
	HostUsername         string
	Status               RoomStatus
	ParticipantCapacity  uint32
	ParticipantCount     uint32
	SpectatorCount       uint32
	WaitingCount         uint32
	ParticipantAdmission AdmissionMode
	SpectatorAdmission   AdmissionMode
	ActiveGameID         string
	ViewerRole           MemberRole
	ViewerRequestedRole  MemberRole
	UpdatedAt            time.Time
}

// PublicRoomCard is a validated public projection; it never contains invitation codes, real names, or other members.
type PublicRoomCard struct {
	snapshot PublicRoomCardSnapshot
}

// PublicRoomPage contains one bounded page and a cursor only when the repository returned a lookahead row.
type PublicRoomPage struct {
	Rooms      []PublicRoomCard
	NextCursor PublicRoomPageCursor
}

// PublicRoomRepository reads denormalized lobby cards without restoring full room aggregates.
type PublicRoomRepository interface {
	ListPublicRooms(context.Context, PublicRoomListRequest) ([]PublicRoomCard, error)
}

// NewPublicRoomListRequest normalizes filters and adds one lookahead row for stable next-page detection.
func NewPublicRoomListRequest(
	actorUserID uuid.UUID,
	filter PublicRoomFilter,
	after PublicRoomPageCursor,
	pageSize uint32,
) (PublicRoomListRequest, error) {
	if actorUserID == uuid.Nil || (after.UpdatedAt.IsZero() != (after.RoomID == uuid.Nil)) {
		return PublicRoomListRequest{}, ErrInvalidRoomInput
	}
	if pageSize == 0 {
		pageSize = DefaultPublicRoomPageSize
	}
	if pageSize > MaximumPublicRoomPageSize {
		return PublicRoomListRequest{}, ErrInvalidRoomInput
	}
	normalizedFilter, err := normalizePublicRoomFilter(filter)
	if err != nil {
		return PublicRoomListRequest{}, err
	}
	if !after.UpdatedAt.IsZero() {
		after.UpdatedAt = canonicalRoomTime(after.UpdatedAt)
		if after.UpdatedAt.UnixNano() <= 0 {
			return PublicRoomListRequest{}, ErrInvalidRoomInput
		}
	}
	return PublicRoomListRequest{
		ActorUserID: actorUserID,
		Filter:      normalizedFilter,
		After:       after,
		PageSize:    pageSize,
		Limit:       pageSize + 1,
	}, nil
}

// Valid lets persistence adapters reject fabricated requests that bypass the service constructor.
func (request PublicRoomListRequest) Valid() bool {
	validated, err := NewPublicRoomListRequest(request.ActorUserID, request.Filter, request.After, request.PageSize)
	if err != nil || validated.ActorUserID != request.ActorUserID || validated.PageSize != request.PageSize ||
		validated.Limit != request.Limit || validated.Filter.GameID != request.Filter.GameID ||
		validated.Filter.ParticipantJoinableOnly != request.Filter.ParticipantJoinableOnly ||
		!validated.After.UpdatedAt.Equal(request.After.UpdatedAt) || validated.After.RoomID != request.After.RoomID ||
		len(validated.Filter.Statuses) != len(request.Filter.Statuses) {
		return false
	}
	for index := range validated.Filter.Statuses {
		if validated.Filter.Statuses[index] != request.Filter.Statuses[index] {
			return false
		}
	}
	return true
}

// RestorePublicRoomCard validates one aggregated persistence row before it crosses the domain boundary.
func RestorePublicRoomCard(snapshot PublicRoomCardSnapshot) (PublicRoomCard, error) {
	snapshot.UpdatedAt = canonicalRoomTime(snapshot.UpdatedAt)
	if snapshot.RoomID == uuid.Nil || snapshot.HostUsername == "" || strings.TrimSpace(snapshot.HostUsername) != snapshot.HostUsername ||
		(snapshot.Status != RoomStatusLobby && snapshot.Status != RoomStatusPlaying) || snapshot.ParticipantCapacity == 0 ||
		snapshot.ParticipantCount == 0 || snapshot.ParticipantCount > snapshot.ParticipantCapacity ||
		!snapshot.ParticipantAdmission.Valid() || !snapshot.SpectatorAdmission.Valid() || snapshot.UpdatedAt.IsZero() {
		return PublicRoomCard{}, ErrRoomIntegrity
	}
	if snapshot.Status == RoomStatusPlaying {
		if snapshot.ActiveGameID == "" || snapshot.ParticipantAdmission != AdmissionClosed {
			return PublicRoomCard{}, ErrRoomIntegrity
		}
	} else if snapshot.ActiveGameID != "" {
		return PublicRoomCard{}, ErrRoomIntegrity
	}
	switch snapshot.ViewerRole {
	case "":
		if snapshot.ViewerRequestedRole != "" {
			return PublicRoomCard{}, ErrRoomIntegrity
		}
	case MemberRoleParticipant, MemberRoleSpectator:
		if snapshot.ViewerRequestedRole != "" {
			return PublicRoomCard{}, ErrRoomIntegrity
		}
	case MemberRoleWaiting:
		if snapshot.ViewerRequestedRole != MemberRoleParticipant && snapshot.ViewerRequestedRole != MemberRoleSpectator {
			return PublicRoomCard{}, ErrRoomIntegrity
		}
	default:
		return PublicRoomCard{}, ErrRoomIntegrity
	}
	return PublicRoomCard{snapshot: snapshot}, nil
}

// Snapshot returns the immutable lobby-safe fields used by transport mapping.
func (card PublicRoomCard) Snapshot() PublicRoomCardSnapshot {
	return card.snapshot
}

// PrimaryAction projects one unambiguous card action from room state, capacity, admission, and viewer membership.
func (card PublicRoomCard) PrimaryAction() PublicRoomPrimaryAction {
	snapshot := card.snapshot
	if snapshot.ViewerRole != "" {
		return PublicRoomPrimaryActionEnterRoom
	}
	if snapshot.Status == RoomStatusPlaying {
		switch snapshot.SpectatorAdmission {
		case AdmissionOpen:
			return PublicRoomPrimaryActionSpectate
		case AdmissionApproval:
			return PublicRoomPrimaryActionRequestSpectate
		default:
			return PublicRoomPrimaryActionInProgress
		}
	}
	if snapshot.ParticipantCount < snapshot.ParticipantCapacity {
		switch snapshot.ParticipantAdmission {
		case AdmissionOpen:
			return PublicRoomPrimaryActionJoin
		case AdmissionApproval:
			return PublicRoomPrimaryActionRequestJoin
		}
	}
	switch snapshot.SpectatorAdmission {
	case AdmissionOpen:
		return PublicRoomPrimaryActionSpectate
	case AdmissionApproval:
		return PublicRoomPrimaryActionRequestSpectate
	}
	if snapshot.ParticipantCount >= snapshot.ParticipantCapacity && snapshot.ParticipantAdmission != AdmissionClosed {
		return PublicRoomPrimaryActionFull
	}
	return PublicRoomPrimaryActionWaitForHost
}

func normalizePublicRoomFilter(filter PublicRoomFilter) (PublicRoomFilter, error) {
	filter.GameID = strings.TrimSpace(filter.GameID)
	if len(filter.GameID) > maximumPublicRoomGameIDBytes || len(filter.Statuses) > 2 {
		return PublicRoomFilter{}, ErrInvalidRoomInput
	}
	statuses := make([]RoomStatus, 0, len(filter.Statuses))
	seen := make(map[RoomStatus]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if status != RoomStatusLobby && status != RoomStatusPlaying {
			return PublicRoomFilter{}, ErrInvalidRoomInput
		}
		if _, duplicate := seen[status]; duplicate {
			return PublicRoomFilter{}, ErrInvalidRoomInput
		}
		seen[status] = struct{}{}
		statuses = append(statuses, status)
	}
	filter.Statuses = statuses
	return filter, nil
}
