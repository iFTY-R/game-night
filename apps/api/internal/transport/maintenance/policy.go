// Package maintenance applies the PostgreSQL-authoritative user-mutation maintenance gate.
package maintenance

import (
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
)

// procedureClass separates operations that maintenance may block from recovery and read paths that must remain available.
// Mutation is deliberately the zero value so any procedure absent from the reviewed registry fails closed.
type procedureClass uint8

const (
	procedureMutation procedureClass = iota
	procedureRead
	procedureAuthenticationRecovery
)

// procedureClasses is kept explicit and descriptor-tested: adding a user RPC requires an intentional classification here.
var procedureClasses = map[string]procedureClass{
	identityv1connect.IdentityServiceBeginIdentityBootstrapProcedure: procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceBootstrapIdentityProcedure:      procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceCompleteOnboardingProcedure:     procedureMutation,
	identityv1connect.IdentityServiceGetCurrentIdentityProcedure:     procedureRead,
	identityv1connect.IdentityServiceChangeUsernameProcedure:         procedureMutation,
	identityv1connect.IdentityServiceRotateRecoveryCodeProcedure:     procedureMutation,
	identityv1connect.IdentityServiceBeginRecoveryChallengeProcedure: procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceBeginRecoveryProcedure:          procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceCompleteRecoveryProcedure:       procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceConfirmSecretReceiptProcedure:   procedureAuthenticationRecovery,
	identityv1connect.IdentityServiceListDevicesProcedure:            procedureRead,
	identityv1connect.IdentityServiceRevokeDeviceProcedure:           procedureMutation,

	roomv1connect.RoomServiceCreateRoomProcedure:             procedureMutation,
	roomv1connect.RoomServiceGetRoomProcedure:                procedureRead,
	roomv1connect.RoomServiceHeartbeatRoomProcedure:          procedureMutation,
	roomv1connect.RoomServiceListMyRoomsProcedure:            procedureRead,
	roomv1connect.RoomServiceListPublicRoomsProcedure:        procedureRead,
	roomv1connect.RoomServiceJoinRoomProcedure:               procedureMutation,
	roomv1connect.RoomServiceApproveMemberProcedure:          procedureMutation,
	roomv1connect.RoomServiceSetAdmissionProcedure:           procedureMutation,
	roomv1connect.RoomServiceSelectRoomGameProcedure:         procedureMutation,
	roomv1connect.RoomServiceUpdateGameConfigProcedure:       procedureMutation,
	roomv1connect.RoomServiceListGameRulePresetsProcedure:    procedureRead,
	roomv1connect.RoomServiceSaveGameRulePresetProcedure:     procedureMutation,
	roomv1connect.RoomServiceDeleteGameRulePresetProcedure:   procedureMutation,
	roomv1connect.RoomServiceBeginGameStartProcedure:         procedureMutation,
	roomv1connect.RoomServiceCancelGameStartProcedure:        procedureMutation,
	roomv1connect.RoomServiceStartGameProcedure:              procedureMutation,
	roomv1connect.RoomServiceRequestRoomPauseProcedure:       procedureMutation,
	roomv1connect.RoomServiceRejectRoomPauseRequestProcedure: procedureMutation,
	roomv1connect.RoomServicePauseRoomGameProcedure:          procedureMutation,
	roomv1connect.RoomServiceResumeRoomGameProcedure:         procedureMutation,
	roomv1connect.RoomServiceTransferRoomHostProcedure:       procedureMutation,
	roomv1connect.RoomServiceFinishGameProcedure:             procedureMutation,
	roomv1connect.RoomServiceRemoveMemberProcedure:           procedureMutation,
	roomv1connect.RoomServiceCloseRoomProcedure:              procedureMutation,

	gamev1connect.GameServiceStartSessionProcedure:        procedureMutation,
	gamev1connect.GameServiceGameActionProcedure:          procedureMutation,
	gamev1connect.GameServiceGetProjectionProcedure:       procedureRead,
	gamev1connect.GameServiceGetReplayProjectionProcedure: procedureRead,
	gamev1connect.GameServiceGetReplayAccessProcedure:     procedureRead,
	gamev1connect.GameServiceSetReplayAccessProcedure:     procedureMutation,
	gamev1connect.GameServiceFinishSessionProcedure:       procedureMutation,
	gamev1connect.GameServiceOpenSubscriptionProcedure:    procedureRead,
}

func (class procedureClass) valid() bool {
	return class == procedureMutation || class == procedureRead || class == procedureAuthenticationRecovery
}

// reviewedProcedureClass distinguishes reviewed user procedures from unknown values for exhaustive contract tests.
func reviewedProcedureClass(procedure string) (procedureClass, bool) {
	class, reviewed := procedureClasses[procedure]
	return class, reviewed
}

// classifyProcedure returns the mutation zero value for unreviewed procedures instead of guessing from method names.
func classifyProcedure(procedure string) procedureClass {
	class, reviewed := reviewedProcedureClass(procedure)
	if !reviewed {
		return procedureMutation
	}
	return class
}
