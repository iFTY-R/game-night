// Package errors maps domain failures to stable, non-sensitive Connect errors.
package errors

import (
	"context"
	stderrors "errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/maintenance"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	adminoperations "github.com/iFTY-R/game-night/platform/admin/operations"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/audit"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/identity"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/replay"
	"github.com/iFTY-R/game-night/platform/room"
	"github.com/iFTY-R/game-night/platform/secretresult"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Interceptor replaces every unary domain error before Connect serializes it to an untrusted client.
func Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			response, err := next(ctx, request)
			cookieExpiries := errorCookieExpiries(err)
			mapped := Map(err)
			attachErrorSetCookies(mapped, cookieExpiries)
			return response, mapped
		}
	})
}

// cookieExpiryError is an internal opt-in carrier for destructive Cookie writes on failed responses.
type cookieExpiryError struct {
	cause  error
	values []string
}

func (err *cookieExpiryError) Error() string { return err.cause.Error() }
func (err *cookieExpiryError) Unwrap() error { return err.cause }

// WithCookieExpiries explicitly carries destructive Cookie cleanup across an error response without permitting credential issuance.
func WithCookieExpiries(cause error, values []string) error {
	if cause == nil {
		return nil
	}
	expiries := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := http.ParseSetCookie(value)
		if err == nil && parsed.Value == "" && parsed.MaxAge < 0 {
			expiries = append(expiries, value)
		}
	}
	if len(expiries) == 0 {
		return cause
	}
	return &cookieExpiryError{cause: cause, values: expiries}
}

// errorCookieExpiries unwraps only the explicit carrier; ordinary response headers never cross the error boundary.
func errorCookieExpiries(err error) []string {
	var expiryError *cookieExpiryError
	if !stderrors.As(err, &expiryError) {
		return nil
	}
	return expiryError.values
}

// attachErrorSetCookies preserves explicitly approved Cookie expiry without forwarding arbitrary response headers on failures.
func attachErrorSetCookies(err error, values []string) {
	if err == nil || len(values) == 0 {
		return
	}
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) {
		return
	}
	for _, value := range values {
		connectError.Meta().Add("Set-Cookie", value)
	}
}

// Map returns one stable Connect code and BusinessErrorDetail without retaining the original error message.
func Map(err error) error {
	if err == nil {
		return nil
	}
	descriptor := classify(err)
	return newConnectError(descriptor, retryAt(err, time.Now()))
}

// AccountSuspended reports a terminal credential inspection without exposing unverified user state.
func AccountSuspended() error {
	return newConnectError(descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ACCOUNT_SUSPENDED, "identity.account.suspended"}, time.Time{})
}

// AccountDeleted reports a verified deleted account while preventing the credential from establishing a principal.
func AccountDeleted() error {
	return newConnectError(descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ACCOUNT_DELETED, "identity.account.deleted"}, time.Time{})
}

type descriptor struct {
	connectCode  connect.Code
	businessCode commonv1.BusinessErrorCode
	messageKey   string
}

func classify(err error) descriptor {
	switch {
	case stderrors.Is(err, context.Canceled):
		return descriptor{connectCode: connect.CodeCanceled, messageKey: "request.canceled"}
	case stderrors.Is(err, context.DeadlineExceeded):
		return descriptor{connectCode: connect.CodeDeadlineExceeded, messageKey: "request.deadline_exceeded"}
	case stderrors.Is(err, origin.ErrNotAllowed):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ORIGIN_NOT_ALLOWED, "request.origin.not_allowed"}
	case stderrors.Is(err, csrf.ErrInvalid):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_CSRF_INVALID, "request.csrf.invalid"}
	case stderrors.Is(err, identifier.ErrUsernameLength), stderrors.Is(err, identifier.ErrUsernameCharacters):
		return descriptor{connect.CodeInvalidArgument, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_INVALID, "identity.username.invalid"}
	case stderrors.Is(err, identity.ErrUsernameUnavailable), stderrors.Is(err, identifier.ErrUsernameUnavailable):
		return descriptor{connect.CodeAlreadyExists, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_TAKEN, "identity.username.taken"}
	case stderrors.Is(err, identity.ErrOnboardingExpired), stderrors.Is(err, identity.ErrUserStatus):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_IDENTITY_ONBOARDING_REQUIRED, "identity.onboarding.required"}
	case stderrors.Is(err, identity.ErrDeviceAuthentication):
		return descriptor{connect.CodeUnauthenticated, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_DEVICE_CREDENTIAL_INVALID, "identity.device.invalid"}
	case stderrors.Is(err, identity.ErrDeviceUnavailable):
		return descriptor{connect.CodeUnauthenticated, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_DEVICE_REVOKED, "identity.device.revoked"}
	case stderrors.Is(err, identity.ErrRecoveryInvalid), stderrors.Is(err, secretresult.ErrReplayUnauthorized):
		return descriptor{connect.CodeUnauthenticated, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_RECOVERY_INVALID, "identity.recovery.invalid"}
	case stderrors.Is(err, secretresult.ErrIdempotencyConflict), stderrors.Is(err, admin.ErrIdempotencyConflict),
		stderrors.Is(err, adminoperations.ErrIdempotencyConflict),
		stderrors.Is(err, adminuser.ErrIdempotencyConflict),
		stderrors.Is(err, idempotency.ErrConflict), stderrors.Is(err, room.ErrRuleOperationConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_IDEMPOTENCY_CONFLICT, "operation.idempotency_conflict"}
	case stderrors.Is(err, secretresult.ErrSecretNoLongerAvailable):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SECRET_RESULT_NO_LONGER_AVAILABLE, "operation.secret_no_longer_available"}
	case stderrors.Is(err, ratelimit.ErrRejected):
		return descriptor{connect.CodeResourceExhausted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_RATE_LIMITED, "request.rate_limited"}
	case stderrors.Is(err, maintenance.ErrMutationBlocked), stderrors.Is(err, gameruntime.ErrMutationBlocked):
		return descriptor{connectCode: connect.CodeFailedPrecondition, messageKey: "service.maintenance.active"}
	case stderrors.Is(err, maintenance.ErrStateUnavailable), stderrors.Is(err, gameruntime.ErrMutationStateUnavailable):
		return descriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "service.temporarily_unavailable"}
	case stderrors.Is(err, room.ErrRoomNotFound):
		return descriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_NOT_FOUND, "room.not_found"}
	case stderrors.Is(err, room.ErrRuleNotFound):
		return descriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_NOT_FOUND, "room.rule.not_found"}
	case stderrors.Is(err, gameruntime.ErrSessionNotFound):
		return descriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_NOT_FOUND, "game.session.not_found"}
	case stderrors.Is(err, gameruntime.ErrStateVersionConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT, "game.state.version_conflict"}
	case stderrors.Is(err, gameruntime.ErrOwnershipLost):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_OWNERSHIP_LOST, "game.ownership.lost"}
	case stderrors.Is(err, gameruntime.ErrSessionSuspended):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_SUSPENDED, "game.session.suspended"}
	case stderrors.Is(err, gameruntime.ErrSessionTerminal):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_TERMINAL, "game.session.terminal"}
	case stderrors.Is(err, gameruntime.ErrParticipantNotActive):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, "game.participant.not_active"}
	case stderrors.Is(err, gameruntime.ErrModuleUnavailable):
		return descriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_MODULE_UNAVAILABLE, "game.module.unavailable"}
	case stderrors.Is(err, gameruntime.ErrReplayUnavailable):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, "game.replay.unavailable"}
	case stderrors.Is(err, replay.ErrAccessDenied):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, "game.replay.forbidden"}
	case stderrors.Is(err, replay.ErrPolicyConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_ACCESS_CONFLICT, "game.replay.access_conflict"}
	case stderrors.Is(err, replay.ErrPolicyUnavailable):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_ACCESS_UNAVAILABLE, "game.replay.access_unavailable"}
	case stderrors.Is(err, gameruntime.ErrProjectionUnsafe):
		return descriptor{connect.CodeInternal, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PROJECTION_UNSAFE, "game.projection.unsafe"}
	case stderrors.Is(err, room.ErrRoomCodeUnavailable):
		return descriptor{connect.CodeAlreadyExists, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_CODE_UNAVAILABLE, "room.code.unavailable"}
	case stderrors.Is(err, room.ErrUsernameConflict), stderrors.Is(err, identity.ErrUsernameRoomConflict):
		return descriptor{connect.CodeAlreadyExists, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_USERNAME_TAKEN, "room.username.taken"}
	case stderrors.Is(err, room.ErrRoomVersionConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT, "room.version.conflict"}
	case stderrors.Is(err, room.ErrPauseRequestExists):
		return descriptor{connect.CodeAlreadyExists, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_EXISTS, "room.pause.request_exists"}
	case stderrors.Is(err, room.ErrPauseRequestNotFound):
		return descriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_PAUSE_REQUEST_NOT_FOUND, "room.pause.request_not_found"}
	case stderrors.Is(err, room.ErrGameAlreadyPaused):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_GAME_ALREADY_PAUSED, "room.game.already_paused"}
	case stderrors.Is(err, room.ErrGameNotPaused):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_GAME_NOT_PAUSED, "room.game.not_paused"}
	case stderrors.Is(err, room.ErrHostTransferTargetInvalid):
		return descriptor{connect.CodeInvalidArgument, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_TRANSFER_TARGET_INVALID, "room.host.transfer_target_invalid"}
	case stderrors.Is(err, room.ErrPauseParticipantRequired):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, "room.pause.participant_required"}
	case stderrors.Is(err, room.ErrRuleRevisionConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT, "room.rule.revision_conflict"}
	case stderrors.Is(err, room.ErrAdmissionClosed):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_ADMISSION_CLOSED, "room.admission.closed"}
	case stderrors.Is(err, room.ErrRoomFull):
		return descriptor{connect.CodeResourceExhausted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_FULL, "room.full"}
	case stderrors.Is(err, room.ErrHostRequired):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_REQUIRED, "room.host.required"}
	case stderrors.Is(err, room.ErrRulePermission):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_REQUIRED, "room.rule.permission_denied"}
	case stderrors.Is(err, room.ErrMemberNotFound), stderrors.Is(err, room.ErrWaitingNotFound):
		return descriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_MEMBER_NOT_FOUND, "room.member.not_found"}
	case stderrors.Is(err, room.ErrRoomStatus), stderrors.Is(err, room.ErrRoomClosed),
		stderrors.Is(err, room.ErrSessionActive), stderrors.Is(err, room.ErrSessionNotFound),
		stderrors.Is(err, room.ErrInsufficientParticipants), stderrors.Is(err, room.ErrParticipantLimitExceeded),
		stderrors.Is(err, room.ErrCannotRemoveHost), stderrors.Is(err, room.ErrPendingStartInvalid),
		stderrors.Is(err, room.ErrGameUnavailable), stderrors.Is(err, room.ErrGameSelectionConflict):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_STATUS_INVALID, "room.status.invalid"}
	case stderrors.Is(err, admin.ErrTOTPInvalid):
		return descriptor{connect.CodeUnauthenticated, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_MFA_INVALID, "admin.mfa.invalid"}
	case stderrors.Is(err, admin.ErrElevationDenied), stderrors.Is(err, adminoperations.ErrElevationRequired):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ELEVATION_REQUIRED, "admin.elevation.required"}
	case stderrors.Is(err, admin.ErrElevationExpired):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ELEVATION_EXPIRED, "admin.elevation.expired"}
	case stderrors.Is(err, admin.ErrRecoveryCodeExhausted):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_RECOVERY_CODE_EXHAUSTED, "admin.recovery_codes.exhausted"}
	case stderrors.Is(err, admin.ErrMFAStateConflict):
		return descriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_MFA_STATE_CONFLICT, "admin.mfa.state_conflict"}
	case stderrors.Is(err, admin.ErrAuthentication), stderrors.Is(err, admin.ErrRecoveryInvalid),
		stderrors.Is(err, admin.ErrSessionExpired), stderrors.Is(err, admin.ErrSessionRevoked):
		return descriptor{connect.CodeUnauthenticated, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUTH_INVALID, "admin.auth.invalid"}
	case stderrors.Is(err, admin.ErrPermissionDenied), stderrors.Is(err, adminuser.ErrPermissionDenied), stderrors.Is(err, adminoperations.ErrPermissionDenied):
		return descriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUTH_INVALID, "admin.permission.denied"}
	case stderrors.Is(err, profile.ErrPIIKeyUnavailable):
		return descriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_PII_KEY_UNAVAILABLE, "profile.key.unavailable"}
	case stderrors.Is(err, audit.ErrSensitiveWriteBlocked), stderrors.Is(err, audit.ErrRepositoryUnavailable), stderrors.Is(err, adminuser.ErrAuditUnavailable), stderrors.Is(err, adminoperations.ErrAuditUnavailable):
		return descriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUDIT_WRITE_FAILED, "audit.write.unavailable"}
	case stderrors.Is(err, identity.ErrUserNotFound), stderrors.Is(err, profile.ErrProfileNotFound), stderrors.Is(err, admin.ErrNotFound), stderrors.Is(err, adminuser.ErrNotFound), stderrors.Is(err, adminoperations.ErrNotFound):
		return descriptor{connectCode: connect.CodeNotFound, messageKey: "resource.not_found"}
	case stderrors.Is(err, admin.ErrConcurrentTransition), stderrors.Is(err, adminuser.ErrConflict), stderrors.Is(err, adminoperations.ErrConflict):
		return descriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_VERSION_CONFLICT, "admin.version.conflict"}
	case stderrors.Is(err, identity.ErrIdentityConcurrentTransition), stderrors.Is(err, identity.ErrRecoveryConcurrentTransition),
		stderrors.Is(err, identity.ErrDeviceConcurrentTransition), stderrors.Is(err, profile.ErrProfileConcurrentTransition),
		stderrors.Is(err, audit.ErrHeadConflict),
		stderrors.Is(err, secretresult.ErrConcurrentTransition):
		return descriptor{connectCode: connect.CodeAborted, messageKey: "operation.concurrent_transition"}
	case stderrors.Is(err, identity.ErrInvalidIdentityRequest), stderrors.Is(err, identity.ErrInvalidUserInput),
		stderrors.Is(err, identity.ErrInvalidDeviceInput), stderrors.Is(err, identity.ErrInvalidRecoveryCredential),
		stderrors.Is(err, identity.ErrInvalidRecoveryAttempt), stderrors.Is(err, identity.ErrInvalidAssistedRecoveryGrant),
		stderrors.Is(err, profile.ErrInvalidProfileInput),
		stderrors.Is(err, admin.ErrInvalidInput), stderrors.Is(err, admin.ErrPasswordPolicy),
		stderrors.Is(err, adminuser.ErrInvalidInput), stderrors.Is(err, adminoperations.ErrInvalidInput),
		stderrors.Is(err, secretresult.ErrInvalidInput), stderrors.Is(err, room.ErrInvalidRoomInput),
		stderrors.Is(err, gameruntime.ErrInvalidSessionInput), stderrors.Is(err, gameruntime.ErrInvalidActionCommit),
		stderrors.Is(err, gameruntime.ErrInvalidSystemCommit), stderrors.Is(err, gameSDK.ErrInvalidContract),
		stderrors.Is(err, redisstore.ErrInvalidCoordinationInput):
		return descriptor{connectCode: connect.CodeInvalidArgument, messageKey: "request.invalid"}
	case stderrors.Is(err, adminoperations.ErrPreviewExpired):
		return descriptor{connectCode: connect.CodeFailedPrecondition, messageKey: "admin.operations.preview_expired"}
	case stderrors.Is(err, adminoperations.ErrRetryLimit):
		return descriptor{connectCode: connect.CodeFailedPrecondition, messageKey: "admin.operations.retry_limit"}
	case stderrors.Is(err, admin.ErrUnavailable):
		return descriptor{connectCode: connect.CodeFailedPrecondition, messageKey: "operation.failed_precondition"}
	case stderrors.Is(err, ratelimit.ErrUnavailable), stderrors.Is(err, identity.ErrIdentityRepositoryUnavailable),
		stderrors.Is(err, profile.ErrProfileRepositoryUnavailable), stderrors.Is(err, admin.ErrRepositoryUnavailable),
		stderrors.Is(err, adminuser.ErrRepositoryUnavailable), stderrors.Is(err, adminoperations.ErrRepositoryUnavailable), stderrors.Is(err, secretresult.ErrRepositoryUnavailable), stderrors.Is(err, room.ErrRoomRepositoryUnavailable),
		stderrors.Is(err, gameruntime.ErrGameSessionRepositoryUnavailable), stderrors.Is(err, redisstore.ErrCoordinationUnavailable):
		return descriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "service.temporarily_unavailable"}
	default:
		return descriptor{connect.CodeInternal, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "service.internal_error"}
	}
}

func retryAt(err error, now time.Time) time.Time {
	var failure *ratelimit.Failure
	if !stderrors.As(err, &failure) || failure.Kind() != ratelimit.FailureRejected || failure.RetryAfter() <= 0 {
		return time.Time{}
	}
	return now.Add(failure.RetryAfter()).Round(0).UTC()
}

func newConnectError(value descriptor, retry time.Time) error {
	connectError := connect.NewError(value.connectCode, stderrors.New(value.messageKey))
	detailMessage := &commonv1.BusinessErrorDetail{Code: value.businessCode, MessageKey: value.messageKey}
	if !retry.IsZero() {
		detailMessage.RetryAt = timestamppb.New(retry)
	}
	detail, err := connect.NewErrorDetail(detailMessage)
	if err != nil {
		return connect.NewError(connect.CodeInternal, stderrors.New("service.internal_error"))
	}
	connectError.AddDetail(detail)
	return connectError
}
