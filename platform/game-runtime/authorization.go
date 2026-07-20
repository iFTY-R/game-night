package gameruntime

import (
	"github.com/google/uuid"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// LiveViewerAuthorization binds a current room role to the frozen session seat used for viewer-safe projection.
type LiveViewerAuthorization struct {
	Viewer game.Viewer
	Host   bool
}

// AuthorizeLiveViewer rechecks the authoritative room/session relationship before any live projection is generated.
func AuthorizeLiveViewer(
	room roomdomain.Room,
	session Session,
	userID uuid.UUID,
	requested game.ViewerKind,
) (LiveViewerAuthorization, error) {
	if userID == uuid.Nil || (requested != game.ViewerPlayer && requested != game.ViewerSpectator) {
		return LiveViewerAuthorization{}, ErrInvalidSessionInput
	}
	roomSnapshot, sessionSnapshot := room.Snapshot(), session.Snapshot()
	if roomSnapshot.ID == uuid.Nil || sessionSnapshot.ID == uuid.Nil ||
		roomSnapshot.Status != roomdomain.RoomStatusPlaying || roomSnapshot.ActiveSessionID != sessionSnapshot.ID ||
		sessionSnapshot.RoomID != roomSnapshot.ID || sessionSnapshot.Status.Terminal() {
		return LiveViewerAuthorization{}, ErrParticipantNotActive
	}
	member, present := room.Member(userID)
	if !present {
		return LiveViewerAuthorization{}, ErrParticipantNotActive
	}
	viewer := game.Viewer{Kind: requested, UserID: game.Identifier(userID.String())}
	switch requested {
	case game.ViewerPlayer:
		if member.Role != roomdomain.MemberRoleParticipant {
			return LiveViewerAuthorization{}, ErrParticipantNotActive
		}
		found := false
		for _, participant := range sessionSnapshot.Participants {
			if participant.UserID == userID {
				viewer.SeatIndex = participant.SeatIndex
				found = true
				break
			}
		}
		if !found {
			return LiveViewerAuthorization{}, ErrParticipantNotActive
		}
	case game.ViewerSpectator:
		if member.Role != roomdomain.MemberRoleSpectator {
			return LiveViewerAuthorization{}, ErrParticipantNotActive
		}
	}
	if !viewer.Valid() {
		return LiveViewerAuthorization{}, ErrGameSessionIntegrity
	}
	return LiveViewerAuthorization{Viewer: viewer, Host: roomSnapshot.HostUserID == userID}, nil
}
