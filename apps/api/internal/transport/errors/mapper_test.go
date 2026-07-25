package errors

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/replay"
	"github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
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
		{name: "rename conflicts in room", err: identity.ErrUsernameRoomConflict, wantConnect: connect.CodeAlreadyExists, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_TAKEN, wantKey: "room.username.taken"},
		{name: "room username taken", err: room.ErrUsernameConflict, wantConnect: connect.CodeAlreadyExists, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_TAKEN, wantKey: "room.username.taken"},
		{name: "device invalid", err: identity.ErrDeviceAuthentication, wantConnect: connect.CodeUnauthenticated, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_DEVICE_CREDENTIAL_INVALID, wantKey: "identity.device.invalid"},
		{name: "admin auth", err: admin.ErrAuthentication, wantConnect: connect.CodeUnauthenticated, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUTH_INVALID, wantKey: "admin.auth.invalid"},
		{name: "admin elevation required", err: admin.ErrElevationDenied, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ELEVATION_REQUIRED, wantKey: "admin.elevation.required"},
		{name: "admin elevation expired", err: admin.ErrElevationExpired, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ELEVATION_EXPIRED, wantKey: "admin.elevation.expired"},
		{name: "admin version conflict", err: admin.ErrConcurrentTransition, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_VERSION_CONFLICT, wantKey: "admin.version.conflict"},
		{name: "admin recovery codes exhausted", err: admin.ErrRecoveryCodeExhausted, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_RECOVERY_CODE_EXHAUSTED, wantKey: "admin.recovery_codes.exhausted"},
		{name: "admin MFA state conflict", err: admin.ErrMFAStateConflict, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_MFA_STATE_CONFLICT, wantKey: "admin.mfa.state_conflict"},
		{name: "room version", err: room.ErrRoomVersionConflict, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT, wantKey: "room.version.conflict"},
		{name: "pause request exists", err: room.ErrPauseRequestExists, wantConnect: connect.CodeAlreadyExists, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_EXISTS, wantKey: "room.pause.request_exists"},
		{name: "pause request missing", err: room.ErrPauseRequestNotFound, wantConnect: connect.CodeNotFound, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_NOT_FOUND, wantKey: "room.pause.request_not_found"},
		{name: "already paused", err: room.ErrGameAlreadyPaused, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_GAME_ALREADY_PAUSED, wantKey: "room.game.already_paused"},
		{name: "not paused", err: room.ErrGameNotPaused, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_GAME_NOT_PAUSED, wantKey: "room.game.not_paused"},
		{name: "invalid host transfer", err: room.ErrHostTransferTargetInvalid, wantConnect: connect.CodeInvalidArgument, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_TRANSFER_TARGET_INVALID, wantKey: "room.host.transfer_target_invalid"},
		{name: "pause participant required", err: room.ErrPauseParticipantRequired, wantConnect: connect.CodePermissionDenied, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, wantKey: "room.pause.participant_required"},
		{name: "room admission", err: room.ErrAdmissionClosed, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_ADMISSION_CLOSED, wantKey: "room.admission.closed"},
		{name: "game state version", err: gameruntime.ErrStateVersionConflict, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT, wantKey: "game.state.version_conflict"},
		{name: "game participant", err: gameruntime.ErrParticipantNotActive, wantConnect: connect.CodePermissionDenied, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, wantKey: "game.participant.not_active"},
		{name: "game replay", err: gameruntime.ErrReplayUnavailable, wantConnect: connect.CodeFailedPrecondition, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, wantKey: "game.replay.unavailable"},
		{name: "game replay access", err: replay.ErrAccessDenied, wantConnect: connect.CodePermissionDenied, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, wantKey: "game.replay.forbidden"},
		{name: "game replay policy conflict", err: replay.ErrPolicyConflict, wantConnect: connect.CodeAborted, wantBusiness: commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_ACCESS_CONFLICT, wantKey: "game.replay.access_conflict"},
		{name: "game rule", err: gameSDK.ErrInvalidContract, wantConnect: connect.CodeInvalidArgument, wantKey: "request.invalid"},
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

func TestInterceptorDoesNotForwardUnwrappedErrorCookies(t *testing.T) {
	intercepted := Interceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		response := connect.NewResponse(&commonv1.BusinessErrorDetail{})
		response.Header().Add("Set-Cookie", "__Host-gn_device=credential; Path=/; Secure; HttpOnly")
		return response, identity.ErrDeviceAuthentication
	})

	_, err := intercepted(t.Context(), connect.NewRequest(&commonv1.BusinessErrorDetail{}))
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) {
		t.Fatalf("mapped error type = %T", err)
	}
	if values := connectError.Meta().Values("Set-Cookie"); len(values) != 0 {
		t.Fatalf("unwrapped error Cookies = %v", values)
	}
}

func TestWithCookieExpiriesOnlyForwardsDestructiveCookies(t *testing.T) {
	intercepted := Interceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, WithCookieExpiries(identity.ErrDeviceAuthentication, []string{
			"__Host-gn_device=credential; Path=/; Secure; HttpOnly",
			"__Host-gn_device=; Path=/; Max-Age=0; Secure; HttpOnly",
		})
	})

	_, err := intercepted(t.Context(), connect.NewRequest(&commonv1.BusinessErrorDetail{}))
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) {
		t.Fatalf("mapped error type = %T", err)
	}
	values := connectError.Meta().Values("Set-Cookie")
	if len(values) != 1 || !strings.Contains(values[0], "__Host-gn_device=;") {
		t.Fatalf("approved error Cookie expiries = %v", values)
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
