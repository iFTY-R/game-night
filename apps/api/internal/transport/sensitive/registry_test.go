package sensitive

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestAllGeneratedProceduresAreExplicitlyRegistered(t *testing.T) {
	registry, err := New(AllOperations()...)
	if err != nil {
		t.Fatal(err)
	}
	services := []protoreflect.ServiceDescriptor{
		identityv1.File_platform_identity_v1_identity_proto.Services().ByName("IdentityService"),
		roomv1.File_platform_room_v1_room_proto.Services().ByName("RoomService"),
		gamev1.File_platform_game_v1_game_proto.Services().ByName("GameService"),
		adminv1.File_platform_admin_v1_admin_auth_proto.Services().ByName("AdminAuthService"),
		adminv1.File_platform_admin_v1_admin_user_proto.Services().ByName("AdminUserService"),
		adminv1.File_platform_admin_v1_admin_room_proto.Services().ByName("AdminRoomService"),
	}
	seen := make(map[string]struct{})
	for _, service := range services {
		for index := range service.Methods().Len() {
			operation := "/" + string(service.FullName()) + "/" + string(service.Methods().Get(index).Name())
			if !registry.Contains(operation) {
				t.Errorf("generated procedure %s is not registered", operation)
			}
			seen[operation] = struct{}{}
		}
	}
	if len(seen) != len(AllOperations()) {
		t.Fatalf("registry has %d operations for %d generated procedures", len(AllOperations()), len(seen))
	}
}

func TestAdminUserOperationsMatchGeneratedServiceExactly(t *testing.T) {
	service := adminv1.File_platform_admin_v1_admin_user_proto.Services().ByName("AdminUserService")
	expected := make([]string, 0, service.Methods().Len())
	for index := range service.Methods().Len() {
		expected = append(expected, "/"+string(service.FullName())+"/"+string(service.Methods().Get(index).Name()))
	}
	if !slices.Equal(AdminUserOperations, expected) {
		t.Fatalf("AdminUserOperations mismatch\nexpected: %v\nactual:   %v", expected, AdminUserOperations)
	}
}

func TestAdminAuthOperationsMatchGeneratedServiceExactly(t *testing.T) {
	service := adminv1.File_platform_admin_v1_admin_auth_proto.Services().ByName("AdminAuthService")
	expected := make([]string, 0, service.Methods().Len())
	for index := range service.Methods().Len() {
		expected = append(expected, "/"+string(service.FullName())+"/"+string(service.Methods().Get(index).Name()))
	}
	if !slices.Equal(AdminAuthOperations, expected) {
		t.Fatalf("AdminAuthOperations mismatch\nexpected: %v\nactual:   %v", expected, AdminAuthOperations)
	}
}

func TestAdminRoomOperationsMatchGeneratedServiceExactly(t *testing.T) {
	service := adminv1.File_platform_admin_v1_admin_room_proto.Services().ByName("AdminRoomService")
	expected := make([]string, 0, service.Methods().Len())
	for index := range service.Methods().Len() {
		expected = append(expected, "/"+string(service.FullName())+"/"+string(service.Methods().Get(index).Name()))
	}
	if !slices.Equal(AdminRoomOperations, expected) {
		t.Fatalf("AdminRoomOperations mismatch\nexpected: %v\nactual:   %v", expected, AdminRoomOperations)
	}
}

func TestAllOperationsExcludeDeletedAdminIdentityProcedures(t *testing.T) {
	for _, operation := range AllOperations() {
		if len(operation) >= len("/platform.admin.v1.AdminIdentityService/") &&
			operation[:len("/platform.admin.v1.AdminIdentityService/")] == "/platform.admin.v1.AdminIdentityService/" {
			t.Fatalf("deleted admin identity procedure still registered: %s", operation)
		}
	}
}

func TestInterceptorMarksContextAndAppliesNoStore(t *testing.T) {
	const procedure = "/test.Service/Sensitive"
	registry, err := New(procedure)
	if err != nil {
		t.Fatal(err)
	}
	handler := connect.NewUnaryHandler(procedure, func(ctx context.Context, _ *connect.Request[identityv1.GetCurrentIdentityRequest]) (*connect.Response[identityv1.GetCurrentIdentityResponse], error) {
		if !FromContext(ctx) {
			t.Fatal("sensitive context marker is missing")
		}
		return connect.NewResponse(&identityv1.GetCurrentIdentityResponse{}), nil
	}, connect.WithInterceptors(registry.Interceptor()))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := connect.NewClient[identityv1.GetCurrentIdentityRequest, identityv1.GetCurrentIdentityResponse](server.Client(), server.URL+procedure)
	response, callErr := client.CallUnary(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{}))
	if callErr != nil {
		t.Fatal(callErr)
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
}

func TestNoStoreCanBeAppliedToConnectErrors(t *testing.T) {
	connectError := connect.NewError(connect.CodeInvalidArgument, errors.New("request.invalid"))
	applyNoStore(connectError.Meta())
	if connectError.Meta().Get("Cache-Control") != "no-store" || connectError.Meta().Get("Pragma") != "no-cache" {
		t.Fatalf("error cache headers = %v", connectError.Meta())
	}
}
