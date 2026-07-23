// Package sensitive owns the reviewed RPC set that must not be cached or body-observed.
package sensitive

import (
	"context"
	stderrors "errors"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
)

const (
	cacheControlValue = "no-store"
	pragmaValue       = "no-cache"
)

// IdentityOperations contains every reviewed user-domain procedure; copies are returned by AllOperations.
var IdentityOperations = []string{
	identityv1connect.IdentityServiceBeginIdentityBootstrapProcedure,
	identityv1connect.IdentityServiceBootstrapIdentityProcedure,
	identityv1connect.IdentityServiceCompleteOnboardingProcedure,
	identityv1connect.IdentityServiceGetCurrentIdentityProcedure,
	identityv1connect.IdentityServiceChangeUsernameProcedure,
	identityv1connect.IdentityServiceRotateRecoveryCodeProcedure,
	identityv1connect.IdentityServiceBeginRecoveryChallengeProcedure,
	identityv1connect.IdentityServiceBeginRecoveryProcedure,
	identityv1connect.IdentityServiceCompleteRecoveryProcedure,
	identityv1connect.IdentityServiceConfirmSecretReceiptProcedure,
	identityv1connect.IdentityServiceListDevicesProcedure,
	identityv1connect.IdentityServiceRevokeDeviceProcedure,
}

// RoomOperations contains every authenticated room read and mutation because private membership must not be cached.
var RoomOperations = []string{
	roomv1connect.RoomServiceCreateRoomProcedure,
	roomv1connect.RoomServiceGetRoomProcedure,
	roomv1connect.RoomServiceHeartbeatRoomProcedure,
	roomv1connect.RoomServiceListMyRoomsProcedure,
	roomv1connect.RoomServiceListPublicRoomsProcedure,
	roomv1connect.RoomServiceJoinRoomProcedure,
	roomv1connect.RoomServiceApproveMemberProcedure,
	roomv1connect.RoomServiceSetAdmissionProcedure,
	roomv1connect.RoomServiceSelectRoomGameProcedure,
	roomv1connect.RoomServiceUpdateGameConfigProcedure,
	roomv1connect.RoomServiceListGameRulePresetsProcedure,
	roomv1connect.RoomServiceSaveGameRulePresetProcedure,
	roomv1connect.RoomServiceDeleteGameRulePresetProcedure,
	roomv1connect.RoomServiceBeginGameStartProcedure,
	roomv1connect.RoomServiceCancelGameStartProcedure,
	roomv1connect.RoomServiceStartGameProcedure,
	roomv1connect.RoomServiceFinishGameProcedure,
	roomv1connect.RoomServiceRemoveMemberProcedure,
	roomv1connect.RoomServiceCloseRoomProcedure,
}

// GameOperations contains every viewer-scoped game read, mutation, replay, and ticket handshake.
var GameOperations = []string{
	gamev1connect.GameServiceStartSessionProcedure,
	gamev1connect.GameServiceGameActionProcedure,
	gamev1connect.GameServiceGetProjectionProcedure,
	gamev1connect.GameServiceGetReplayProjectionProcedure,
	gamev1connect.GameServiceGetReplayAccessProcedure,
	gamev1connect.GameServiceSetReplayAccessProcedure,
	gamev1connect.GameServiceFinishSessionProcedure,
	gamev1connect.GameServiceOpenSubscriptionProcedure,
}

// AdminAuthOperations contains every reviewed administrator authentication procedure.
var AdminAuthOperations = []string{
	adminv1connect.AdminAuthServiceGetSetupStateProcedure,
	adminv1connect.AdminAuthServiceBeginAdminLoginProcedure,
	adminv1connect.AdminAuthServiceLoginPasswordProcedure,
	adminv1connect.AdminAuthServiceVerifyTotpProcedure,
	adminv1connect.AdminAuthServiceChangeInitialPasswordProcedure,
	adminv1connect.AdminAuthServiceBeginTotpEnrollmentProcedure,
	adminv1connect.AdminAuthServiceCompleteTotpEnrollmentProcedure,
	adminv1connect.AdminAuthServiceConfirmAdminSecretReceiptProcedure,
	adminv1connect.AdminAuthServiceRecoverAdminProcedure,
	adminv1connect.AdminAuthServiceChangeAdminPasswordProcedure,
	adminv1connect.AdminAuthServiceBeginTotpRebindProcedure,
	adminv1connect.AdminAuthServiceCompleteTotpRebindProcedure,
	adminv1connect.AdminAuthServiceRegenerateAdminRecoveryCodesProcedure,
	adminv1connect.AdminAuthServiceLogoutAdminProcedure,
	adminv1connect.AdminAuthServiceLogoutAllAdminSessionsProcedure,
}

// AdminIdentityOperations contains every reviewed privileged profile, governance, and audit procedure.
var AdminIdentityOperations = []string{
	adminv1connect.AdminIdentityServiceGetUserProcedure,
	adminv1connect.AdminIdentityServiceGetRealNameProcedure,
	adminv1connect.AdminIdentityServiceUpdateRealNameProcedure,
	adminv1connect.AdminIdentityServiceCreateUserProfileExportProcedure,
	adminv1connect.AdminIdentityServiceGetUserProfileExportPageProcedure,
	adminv1connect.AdminIdentityServiceCompleteUserProfileExportProcedure,
	adminv1connect.AdminIdentityServiceAbortUserProfileExportProcedure,
	adminv1connect.AdminIdentityServiceCreateAssistedRecoveryGrantProcedure,
	adminv1connect.AdminIdentityServiceForceChangeUsernameProcedure,
	adminv1connect.AdminIdentityServiceSuspendUserProcedure,
	adminv1connect.AdminIdentityServiceUnsuspendUserProcedure,
	adminv1connect.AdminIdentityServiceDeleteUserProcedure,
	adminv1connect.AdminIdentityServiceRevokeUserDeviceProcedure,
	adminv1connect.AdminIdentityServiceListAuditEventsProcedure,
}

// Registry is immutable after construction so concurrent interceptors cannot change observation policy.
type Registry struct{ operations map[string]struct{} }

// New validates a complete caller-provided registry without retaining mutable slices.
func New(operations ...string) (*Registry, error) {
	if len(operations) == 0 {
		return nil, stderrors.New("sensitive RPC registry is empty")
	}
	registered := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		if operation == "" {
			return nil, stderrors.New("sensitive RPC registry is invalid")
		}
		if _, exists := registered[operation]; exists {
			return nil, stderrors.New("sensitive RPC registry is invalid")
		}
		registered[operation] = struct{}{}
	}
	return &Registry{operations: registered}, nil
}

// AllOperations returns an independent list suitable for metrics and the process-wide cache policy registry.
func AllOperations() []string {
	operations := make([]string, 0, len(IdentityOperations)+len(RoomOperations)+len(GameOperations)+len(AdminAuthOperations)+len(AdminIdentityOperations))
	operations = append(operations, IdentityOperations...)
	operations = append(operations, RoomOperations...)
	operations = append(operations, GameOperations...)
	operations = append(operations, AdminAuthOperations...)
	operations = append(operations, AdminIdentityOperations...)
	return operations
}

// Interceptor marks reviewed procedures in context and applies no-store headers to success and error responses.
func (registry *Registry) Interceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			if registry == nil || request == nil || !registry.Contains(request.Spec().Procedure) {
				return next(ctx, request)
			}
			ctx = context.WithValue(ctx, sensitiveContextKey{}, true)
			response, err := next(ctx, request)
			if response != nil {
				applyNoStore(response.Header())
			}
			var connectError *connect.Error
			if stderrors.As(err, &connectError) {
				applyNoStore(connectError.Meta())
			}
			return response, err
		}
	})
}

// Contains reports whether a procedure has passed explicit body-observation and caching review.
func (registry *Registry) Contains(operation string) bool {
	if registry == nil {
		return false
	}
	_, exists := registry.operations[operation]
	return exists
}

type sensitiveContextKey struct{}

// FromContext lets tracing and error sampling disable body capture before invoking the service method.
func FromContext(ctx context.Context) bool {
	sensitive, _ := ctx.Value(sensitiveContextKey{}).(bool)
	return sensitive
}

func applyNoStore(header interface{ Set(string, string) }) {
	header.Set("Cache-Control", cacheControlValue)
	header.Set("Pragma", pragmaValue)
}
