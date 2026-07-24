package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestSameRoomSnapshotIncludesRuleSelectionFences(t *testing.T) {
	base := roomGameSessionComparisonSnapshot()

	selectedGameChanged := base
	selectedGameChanged.SelectedGameID = "dice-789"
	if sameRoomSnapshot(base, selectedGameChanged) {
		t.Fatal("sameRoomSnapshot ignored selected game changes")
	}

	ownershipEpochChanged := base
	ownershipEpochChanged.OwnershipEpoch++
	if sameRoomSnapshot(base, ownershipEpochChanged) {
		t.Fatal("sameRoomSnapshot ignored ownership epoch changes")
	}
}

func roomGameSessionComparisonSnapshot() roomDomain.RoomSnapshot {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	hostID := uuid.New()
	return roomDomain.RoomSnapshot{
		ID:                   uuid.New(),
		RoomCode:             "ABC123",
		Visibility:           roomDomain.VisibilityPrivate,
		Status:               roomDomain.RoomStatusLobby,
		HostUserID:           hostID,
		ParticipantCapacity:  4,
		ParticipantAdmission: roomDomain.AdmissionOpen,
		SpectatorAdmission:   roomDomain.AdmissionOpen,
		Members: []roomDomain.MemberSnapshot{{
			UserID:     hostID,
			Role:       roomDomain.MemberRoleParticipant,
			SeatIndex:  1,
			JoinedAt:   now,
			LastSeenAt: now,
		}},
		SelectedGameID:    roomDomain.DefaultSelectedGameID,
		RoomVersion:       1,
		MembershipVersion: 1,
		OwnershipEpoch:    1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}
