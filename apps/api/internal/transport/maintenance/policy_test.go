package maintenance

import (
	"fmt"
	"testing"

	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestEveryGeneratedUserProcedureHasOneReviewedClassification(t *testing.T) {
	want := generatedUserProcedures(t)
	if len(procedureClasses) != len(want) {
		t.Fatalf("reviewed procedures = %d, generated procedures = %d", len(procedureClasses), len(want))
	}
	for procedure := range want {
		class, reviewed := reviewedProcedureClass(procedure)
		if !reviewed || !class.valid() {
			t.Errorf("generated procedure %q has no valid reviewed classification", procedure)
		}
	}
	for procedure := range procedureClasses {
		if _, generated := want[procedure]; !generated {
			t.Errorf("reviewed procedure %q is absent from generated user descriptors", procedure)
		}
	}
}

func TestReviewedProcedureClassesPreserveMaintenanceBoundaries(t *testing.T) {
	tests := []struct {
		procedure string
		want      procedureClass
	}{
		{identityv1connect.IdentityServiceGetCurrentIdentityProcedure, procedureRead},
		{identityv1connect.IdentityServiceListDevicesProcedure, procedureRead},
		{identityv1connect.IdentityServiceBeginIdentityBootstrapProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceBootstrapIdentityProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceBeginRecoveryChallengeProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceBeginRecoveryProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceCompleteRecoveryProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceConfirmSecretReceiptProcedure, procedureAuthenticationRecovery},
		{identityv1connect.IdentityServiceCompleteOnboardingProcedure, procedureMutation},
		{identityv1connect.IdentityServiceChangeUsernameProcedure, procedureMutation},
		{identityv1connect.IdentityServiceRotateRecoveryCodeProcedure, procedureMutation},
		{identityv1connect.IdentityServiceRevokeDeviceProcedure, procedureMutation},
		{roomv1connect.RoomServiceGetRoomProcedure, procedureRead},
		{roomv1connect.RoomServiceListMyRoomsProcedure, procedureRead},
		{roomv1connect.RoomServiceListPublicRoomsProcedure, procedureRead},
		{roomv1connect.RoomServiceListGameRulePresetsProcedure, procedureRead},
		{roomv1connect.RoomServiceHeartbeatRoomProcedure, procedureMutation},
		{roomv1connect.RoomServiceCreateRoomProcedure, procedureMutation},
		{gamev1connect.GameServiceGetProjectionProcedure, procedureRead},
		{gamev1connect.GameServiceGetReplayProjectionProcedure, procedureRead},
		{gamev1connect.GameServiceGetReplayAccessProcedure, procedureRead},
		{gamev1connect.GameServiceOpenSubscriptionProcedure, procedureRead},
		{gamev1connect.GameServiceGameActionProcedure, procedureMutation},
		{gamev1connect.GameServiceSetReplayAccessProcedure, procedureMutation},
	}
	for _, test := range tests {
		t.Run(test.procedure, func(t *testing.T) {
			if got := classifyProcedure(test.procedure); got != test.want {
				t.Fatalf("classifyProcedure() = %v, want %v", got, test.want)
			}
		})
	}
	if got := classifyProcedure("/unreviewed.v1.Service/NewMutation"); got != procedureMutation {
		t.Fatalf("unknown procedure class = %v, want fail-closed mutation", got)
	}
}

func generatedUserProcedures(t testing.TB) map[string]struct{} {
	t.Helper()
	files := []protoreflect.FileDescriptor{
		identityv1.File_platform_identity_v1_identity_proto,
		roomv1.File_platform_room_v1_room_proto,
		gamev1.File_platform_game_v1_game_proto,
	}
	procedures := make(map[string]struct{})
	for _, file := range files {
		services := file.Services()
		if services.Len() != 1 {
			t.Fatalf("generated user contract %q contains %d services, want 1", file.Path(), services.Len())
		}
		service := services.Get(0)
		methods := service.Methods()
		for index := 0; index < methods.Len(); index++ {
			method := methods.Get(index)
			if method.IsStreamingClient() || method.IsStreamingServer() {
				t.Fatalf("generated user procedure %q is streaming but maintenance gate is unary-only", method.FullName())
			}
			procedure := fmt.Sprintf("/%s/%s", service.FullName(), method.Name())
			if _, duplicate := procedures[procedure]; duplicate {
				t.Fatalf("duplicate generated procedure %q", procedure)
			}
			procedures[procedure] = struct{}{}
		}
	}
	return procedures
}
