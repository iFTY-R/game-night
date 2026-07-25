package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
)

func TestSurfacesRejectCrossDomainServicePathsAndKeepInterceptorsIndependent(t *testing.T) {
	readiness := readyReadiness(t)
	var userCalls atomic.Int32
	var adminCalls atomic.Int32
	userSurface, err := NewUserSurface(UserSurfaceConfig{
		Identity: &testIdentityHandler{}, Room: &testRoomHandler{}, Game: &testGameHandler{}, Readiness: readiness,
		Interceptors: []connect.Interceptor{countingInterceptor(&userCalls)},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminSurface, err := NewAdminSurface(AdminSurfaceConfig{
		Auth:         &testAdminAuthHandler{},
		Interceptors: []connect.Interceptor{countingInterceptor(&adminCalls)},
	})
	if err != nil {
		t.Fatal(err)
	}

	userServer := httptest.NewServer(userSurface)
	adminServer := httptest.NewServer(adminSurface)
	t.Cleanup(userServer.Close)
	t.Cleanup(adminServer.Close)

	userClient := identityv1connect.NewIdentityServiceClient(userServer.Client(), userServer.URL)
	if _, err = userClient.GetCurrentIdentity(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{})); err != nil {
		t.Fatalf("call user surface: %v", err)
	}
	roomClient := roomv1connect.NewRoomServiceClient(userServer.Client(), userServer.URL)
	if _, err = roomClient.GetRoom(t.Context(), connect.NewRequest(&roomv1.GetRoomRequest{})); err != nil {
		t.Fatalf("call room surface: %v", err)
	}
	gameClient := gamev1connect.NewGameServiceClient(userServer.Client(), userServer.URL)
	if _, err = gameClient.GetProjection(t.Context(), connect.NewRequest(&gamev1.GetProjectionRequest{})); err != nil {
		t.Fatalf("call game surface: %v", err)
	}
	adminAuthClient := adminv1connect.NewAdminAuthServiceClient(adminServer.Client(), adminServer.URL)
	if _, err = adminAuthClient.GetSetupState(t.Context(), connect.NewRequest(&adminv1.GetSetupStateRequest{})); err != nil {
		t.Fatalf("call admin auth surface: %v", err)
	}
	if _, err = adminAuthClient.PreviewRevokeOtherAdminSessions(
		t.Context(),
		connect.NewRequest(&adminv1.PreviewRevokeOtherAdminSessionsRequest{}),
	); err != nil {
		t.Fatalf("call admin auth preview surface: %v", err)
	}

	assertHTTPStatus(t, userServer, http.MethodPost, adminv1connect.AdminAuthServiceGetSetupStateProcedure, http.StatusNotFound)
	assertHTTPStatus(t, userServer, http.MethodPost, "/platform.admin.v1.AdminIdentityService/GetUser", http.StatusNotFound)
	assertHTTPStatus(t, adminServer, http.MethodPost, identityv1connect.IdentityServiceGetCurrentIdentityProcedure, http.StatusNotFound)
	assertHTTPStatus(t, adminServer, http.MethodPost, roomv1connect.RoomServiceGetRoomProcedure, http.StatusNotFound)
	assertHTTPStatus(t, adminServer, http.MethodPost, gamev1connect.GameServiceGetProjectionProcedure, http.StatusNotFound)
	assertHTTPStatus(t, adminServer, http.MethodGet, ReadinessPath, http.StatusNotFound)
	assertHTTPStatus(t, adminServer, http.MethodGet, SensitiveReadinessPath, http.StatusNotFound)
	if userCalls.Load() != 3 || adminCalls.Load() != 2 {
		t.Fatalf("interceptor calls crossed surfaces: user=%d admin=%d", userCalls.Load(), adminCalls.Load())
	}
}

func TestHandlerRoutesOneListenerWithoutCrossingInterceptorChains(t *testing.T) {
	readiness := readyReadiness(t)
	var userCalls atomic.Int32
	var adminCalls atomic.Int32
	userSurface, err := NewUserSurface(UserSurfaceConfig{
		Identity: &testIdentityHandler{}, Room: &testRoomHandler{}, Game: &testGameHandler{}, Readiness: readiness,
		Interceptors: []connect.Interceptor{countingInterceptor(&userCalls)},
	})
	if err != nil {
		t.Fatal(err)
	}
	adminSurface, err := NewAdminSurface(AdminSurfaceConfig{
		Auth:         &testAdminAuthHandler{},
		Interceptors: []connect.Interceptor{countingInterceptor(&adminCalls)},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{
		User: userSurface, Admin: adminSurface,
		Metrics: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)

	userClient := identityv1connect.NewIdentityServiceClient(testServer.Client(), testServer.URL)
	if _, err = userClient.GetCurrentIdentity(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{})); err != nil {
		t.Fatal(err)
	}
	roomClient := roomv1connect.NewRoomServiceClient(testServer.Client(), testServer.URL)
	if _, err = roomClient.GetRoom(t.Context(), connect.NewRequest(&roomv1.GetRoomRequest{})); err != nil {
		t.Fatal(err)
	}
	gameClient := gamev1connect.NewGameServiceClient(testServer.Client(), testServer.URL)
	if _, err = gameClient.GetProjection(t.Context(), connect.NewRequest(&gamev1.GetProjectionRequest{})); err != nil {
		t.Fatal(err)
	}
	adminClient := adminv1connect.NewAdminAuthServiceClient(testServer.Client(), testServer.URL)
	if _, err = adminClient.GetSetupState(t.Context(), connect.NewRequest(&adminv1.GetSetupStateRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err = adminClient.PreviewRevokeOtherAdminSessions(
		t.Context(),
		connect.NewRequest(&adminv1.PreviewRevokeOtherAdminSessionsRequest{}),
	); err != nil {
		t.Fatal(err)
	}
	response, err := testServer.Client().Get(testServer.URL + MetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || userCalls.Load() != 3 || adminCalls.Load() != 2 {
		t.Fatalf("combined routing: metrics=%d user=%d admin=%d", response.StatusCode, userCalls.Load(), adminCalls.Load())
	}
}

func countingInterceptor(calls *atomic.Int32) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			calls.Add(1)
			return next(ctx, request)
		}
	})
}

func assertHTTPStatus(t testing.TB, server *httptest.Server, method, path string, want int) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s status = %d, want %d", method, path, response.StatusCode, want)
	}
}

type testIdentityHandler struct {
	identityv1connect.UnimplementedIdentityServiceHandler
}

func (*testIdentityHandler) GetCurrentIdentity(context.Context, *connect.Request[identityv1.GetCurrentIdentityRequest]) (*connect.Response[identityv1.GetCurrentIdentityResponse], error) {
	return connect.NewResponse(&identityv1.GetCurrentIdentityResponse{}), nil
}

type testAdminAuthHandler struct {
	adminv1connect.UnimplementedAdminAuthServiceHandler
}

type testRoomHandler struct {
	roomv1connect.UnimplementedRoomServiceHandler
}

type testGameHandler struct {
	gamev1connect.UnimplementedGameServiceHandler
}

func (*testGameHandler) GetProjection(context.Context, *connect.Request[gamev1.GetProjectionRequest]) (*connect.Response[gamev1.GetProjectionResponse], error) {
	return connect.NewResponse(&gamev1.GetProjectionResponse{}), nil
}

func (*testRoomHandler) GetRoom(context.Context, *connect.Request[roomv1.GetRoomRequest]) (*connect.Response[roomv1.GetRoomResponse], error) {
	return connect.NewResponse(&roomv1.GetRoomResponse{}), nil
}

func (*testAdminAuthHandler) GetSetupState(context.Context, *connect.Request[adminv1.GetSetupStateRequest]) (*connect.Response[adminv1.GetSetupStateResponse], error) {
	return connect.NewResponse(&adminv1.GetSetupStateResponse{}), nil
}

func (*testAdminAuthHandler) PreviewRevokeOtherAdminSessions(
	context.Context,
	*connect.Request[adminv1.PreviewRevokeOtherAdminSessionsRequest],
) (*connect.Response[adminv1.PreviewRevokeOtherAdminSessionsResponse], error) {
	return connect.NewResponse(&adminv1.PreviewRevokeOtherAdminSessionsResponse{}), nil
}
