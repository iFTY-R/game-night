package internalgame

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/realtime/internal/owner"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

type errorDescriptor struct {
	connectCode  connect.Code
	businessCode commonv1.BusinessErrorCode
	messageKey   string
}

// ErrorInterceptor prevents private implementation messages from becoming a cross-process contract.
func ErrorInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			response, err := next(ctx, request)
			if err == nil {
				return response, nil
			}
			descriptor := classifyError(err)
			mapped := connect.NewError(descriptor.connectCode, errors.New(descriptor.messageKey))
			detail, detailErr := connect.NewErrorDetail(&commonv1.BusinessErrorDetail{
				Code: descriptor.businessCode, MessageKey: descriptor.messageKey,
			})
			if detailErr != nil {
				return nil, connect.NewError(connect.CodeInternal, errors.New("service.internal_error"))
			}
			mapped.AddDetail(detail)
			return nil, mapped
		}
	})
}

func classifyError(err error) errorDescriptor {
	switch {
	case errors.Is(err, context.Canceled):
		return errorDescriptor{connect.CodeCanceled, 0, "request.canceled"}
	case errors.Is(err, context.DeadlineExceeded):
		return errorDescriptor{connect.CodeDeadlineExceeded, 0, "request.deadline_exceeded"}
	case errors.Is(err, idempotency.ErrConflict):
		return errorDescriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_IDEMPOTENCY_CONFLICT, "operation.idempotency_conflict"}
	case errors.Is(err, gameruntime.ErrSessionNotFound):
		return errorDescriptor{connect.CodeNotFound, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_NOT_FOUND, "game.session.not_found"}
	case errors.Is(err, gameruntime.ErrStateVersionConflict):
		return errorDescriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_STATE_VERSION_CONFLICT, "game.state.version_conflict"}
	case errors.Is(err, gameruntime.ErrOwnershipLost), errors.Is(err, owner.ErrLeaseLost):
		return errorDescriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_OWNERSHIP_LOST, "game.ownership.lost"}
	case errors.Is(err, owner.ErrOwnedElsewhere):
		return errorDescriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "game.owner.redirect_required"}
	case errors.Is(err, owner.ErrOwnershipUnavailable), errors.Is(err, owner.ErrClosed):
		return errorDescriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "game.owner.unavailable"}
	case errors.Is(err, gameruntime.ErrSessionSuspended):
		return errorDescriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_SUSPENDED, "game.session.suspended"}
	case errors.Is(err, gameruntime.ErrSessionTerminal):
		return errorDescriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_SESSION_TERMINAL, "game.session.terminal"}
	case errors.Is(err, gameruntime.ErrParticipantNotActive):
		return errorDescriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PARTICIPANT_NOT_ACTIVE, "game.participant.not_active"}
	case errors.Is(err, gameruntime.ErrModuleUnavailable):
		return errorDescriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_MODULE_UNAVAILABLE, "game.module.unavailable"}
	case errors.Is(err, gameruntime.ErrReplayUnavailable):
		return errorDescriptor{connect.CodeFailedPrecondition, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_REPLAY_FORBIDDEN, "game.replay.unavailable"}
	case errors.Is(err, gameruntime.ErrProjectionUnsafe):
		return errorDescriptor{connect.CodeInternal, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_PROJECTION_UNSAFE, "game.projection.unsafe"}
	case errors.Is(err, roomdomain.ErrRoomVersionConflict):
		return errorDescriptor{connect.CodeAborted, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_VERSION_CONFLICT, "room.version.conflict"}
	case errors.Is(err, roomdomain.ErrHostRequired):
		return errorDescriptor{connect.CodePermissionDenied, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ROOM_HOST_REQUIRED, "room.host.required"}
	case errors.Is(err, gameruntime.ErrInvalidSessionInput), errors.Is(err, game.ErrInvalidContract),
		errors.Is(err, redisstore.ErrInvalidCoordinationInput):
		return errorDescriptor{connect.CodeInvalidArgument, 0, "request.invalid"}
	case errors.Is(err, gameruntime.ErrGameSessionRepositoryUnavailable), errors.Is(err, redisstore.ErrCoordinationUnavailable):
		return errorDescriptor{connect.CodeUnavailable, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "service.temporarily_unavailable"}
	default:
		return errorDescriptor{connect.CodeInternal, commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_SERVICE_TEMPORARILY_UNAVAILABLE, "service.internal_error"}
	}
}
