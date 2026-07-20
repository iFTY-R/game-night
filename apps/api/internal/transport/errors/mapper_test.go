package errors

import (
	stderrors "errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/room"
)

func TestMapReturnsStableBusinessDetails(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantConnect  connect.Code
		wantBusiness commonv1.BusinessErrorCode
		wantKey      string
	}{
		{name: "username invalid", err: identifier.ErrUsernameCharacters, wantConnect: connect.CodeInvalidArgument, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_INVALID, wantKey: "identity.username.invalid"},
		{name: "username taken", err: identity.ErrUsernameUnavailable, wantConnect: connect.CodeAlreadyExists, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_TAKEN, wantKey: "identity.username.taken"},
		{name: "device invalid", err: identity.ErrDeviceAuthentication, wantConnect: connect.CodeUnauthenticated, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_DEVICE_CREDENTIAL_INVALID, wantKey: "identity.device.invalid"},
		{name: "admin auth", err: admin.ErrAuthentication, wantConnect: connect.CodeUnauthenticated, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUTH_INVALID, wantKey: "admin.auth.invalid"},
		{name: "room version", err: room.ErrRoomVersionConflict, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT, wantKey: "room.version.conflict"},
		{name: "room admission", err: room.ErrAdmissionClosed, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_ADMISSION_CLOSED, wantKey: "room.admission.closed"},
		{name: "game state version", err: gameruntime.ErrStateVersionConflict, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT, wantKey: "game.state.version_conflict"},
		{name: "game participant", err: gameruntime.ErrParticipantNotActive, wantConnect: connect.CodePermissionDenied, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, wantKey: "game.participant.not_active"},
		{name: "game replay", err: gameruntime.ErrReplayUnavailable, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, wantKey: "game.replay.unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := Map(test.err)
			var connectError *connect.Error
			if !stderrors.As(mapped, &connectError) || connectError.Code() != test.wantConnect || connectError.Message() != test.wantKey {
				t.Fatalf("mapped error = %v", mapped)
			}
			detail := businessDetail(t, connectError)
			if detail.GetCode() != test.wantBusiness || detail.GetMessageKey() != test.wantKey {
				t.Fatalf("business detail = %+v", detail)
			}
		})
	}
}

func TestMapDoesNotExposeWrappedInternalMessage(t *testing.T) {
	const privateMessage = "postgres host=private password=secret"
	mapped := Map(stderrors.Join(identity.ErrIdentityRepositoryUnavailable, stderrors.New(privateMessage)))
	if strings.Contains(mapped.Error(), privateMessage) {
		t.Fatalf("mapped error leaked internal detail: %v", mapped)
	}
	if connect.CodeOf(mapped) != connect.CodeUnavailable {
		t.Fatalf("mapped code = %s, want unavailable", connect.CodeOf(mapped))
	}
}

func businessDetail(t testing.TB, connectError *connect.Error) *commonv1.BusinessErrorDetail {
	t.Helper()
	if len(connectError.Details()) != 1 {
		t.Fatalf("error details = %d, want 1", len(connectError.Details()))
	}
	message, err := connectError.Details()[0].Value()
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := message.(*commonv1.BusinessErrorDetail)
	if !ok {
		t.Fatalf("error detail type = %T", message)
	}
	return detail
}
