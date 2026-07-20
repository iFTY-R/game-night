package gamewebsocket

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestParseClientFrameRequiresCanonicalKnownProtobuf(t *testing.T) {
	hello := &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{
		Ticket: []byte("ticket"), Grant: []byte("grant"),
	}}}
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseClientFrame(raw)
	if err != nil || !proto.Equal(parsed, hello) {
		t.Fatalf("parseClientFrame() parsed=%v error=%v", parsed, err)
	}

	for name, candidate := range map[string][]byte{
		"empty":         nil,
		"unknown field": append(append([]byte(nil), raw...), 0x78, 0x01),
		"missing body":  {0x0a, 0x00},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseClientFrame(candidate); !errors.Is(err, ErrInvalidClientFrame) {
				t.Fatalf("parseClientFrame() error = %v", err)
			}
		})
	}
}

func TestServerFrameForSnapshotDecoratesCurrentHostPermission(t *testing.T) {
	authorization := wireAuthorization(game.ViewerPlayer)
	update := wireUpdate(authorization, 8)
	update.Host = true
	update.Projection = game.Projection{
		View:           game.Message{MessageType: "viewer.state", SchemaVersion: 1, Payload: []byte("safe")},
		AllowedActions: []game.Identifier{"round.roll", finishAction},
	}
	original := append([]game.Identifier(nil), update.Projection.AllowedActions...)

	frame, err := serverFrameForUpdate(update, authorization, 7)
	if err != nil {
		t.Fatal(err)
	}
	projection := frame.GetProjection()
	if projection == nil || projection.GetSessionId() != authorization.SessionID.String() || projection.GetStateVersion() != 8 ||
		projection.GetViewerKind() != gamev1.ViewerKind_VIEWER_KIND_PLAYER ||
		!reflect.DeepEqual(projection.GetAllowedActions(), []string{"round.roll", string(finishAction)}) ||
		string(projection.GetView().GetPayload()) != "safe" {
		t.Fatalf("projection = %+v", projection)
	}
	if !reflect.DeepEqual(update.Projection.AllowedActions, original) {
		t.Fatalf("input actions mutated: %v", update.Projection.AllowedActions)
	}
	if _, err := marshalServerFrame(frame); err != nil {
		t.Fatal(err)
	}
}

func TestServerFrameForDeltaPreservesCursorAndViewerScope(t *testing.T) {
	authorization := wireAuthorization(game.ViewerSpectator)
	update := wireUpdate(authorization, 12)
	update.Delta = game.EventProjection{Messages: []game.Message{
		{MessageType: "viewer.delta", SchemaVersion: 1, Payload: []byte("spectator-safe")},
	}}

	frame, err := serverFrameForUpdate(update, authorization, 9)
	if err != nil {
		t.Fatal(err)
	}
	delta := frame.GetDelta()
	if delta == nil || delta.GetFromStateVersion() != 9 || delta.GetToStateVersion() != 12 ||
		delta.GetViewerKind() != gamev1.ViewerKind_VIEWER_KIND_SPECTATOR || len(delta.GetMessages()) != 1 ||
		string(delta.GetMessages()[0].GetPayload()) != "spectator-safe" || delta.GetSnapshotFallback() {
		t.Fatalf("delta = %+v", delta)
	}
}

func TestServerFrameForUpdateRejectsCrossSessionAndNonAdvancingDelta(t *testing.T) {
	authorization := wireAuthorization(game.ViewerPlayer)
	update := wireUpdate(authorization, 2)
	update.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}

	other := update
	other.SessionID = uuid.New()
	for name, test := range map[string]struct {
		update   subscription.Update
		previous uint64
	}{
		"cross session": {update: other, previous: 1},
		"same cursor":   {update: update, previous: 2},
		"future cursor": {update: update, previous: 3},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serverFrameForUpdate(test.update, authorization, test.previous); !errors.Is(err, ErrInvalidUpdate) {
				t.Fatalf("serverFrameForUpdate() error = %v", err)
			}
		})
	}
}

func wireAuthorization(kind game.ViewerKind) subscription.Authorization {
	userID := uuid.New()
	seat := uint32(0)
	if kind == game.ViewerPlayer {
		seat = 3
	}
	return subscription.Authorization{
		UserID: userID, RoomID: uuid.New(), SessionID: uuid.New(),
		Viewer: game.Viewer{Kind: kind, UserID: game.Identifier(userID.String()), SeatIndex: seat},
		Cursor: 1, CurrentVersion: 1, RoomVersion: 1, MembershipVersion: 1,
	}
}

func wireUpdate(authorization subscription.Authorization, stateVersion uint64) subscription.Update {
	return subscription.Update{
		SessionID: authorization.SessionID, StateVersion: stateVersion,
		VersionKey: game.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"},
	}
}
