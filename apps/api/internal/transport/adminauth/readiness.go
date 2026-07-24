package adminauth

import (
	"context"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

// RuntimeReadinessService extends the domain authentication adapter with API-owned operational probes.
type RuntimeReadinessService struct {
	*admin.ConnectAdminService
	readiness *server.Readiness
}

// NewRuntimeReadinessService keeps readiness evaluation in the API layer while reusing the reviewed admin session path.
func NewRuntimeReadinessService(service *admin.Service, effects admin.AdminCookieEffects, readiness *server.Readiness) (*RuntimeReadinessService, error) {
	if readiness == nil {
		return nil, admin.ErrInvalidInput
	}
	adapter, err := admin.NewConnectAdminServiceWithCookieEffects(service, effects)
	if err != nil {
		return nil, err
	}
	return &RuntimeReadinessService{ConnectAdminService: adapter, readiness: readiness}, nil
}

// GetRuntimeReadiness requires a currently valid full administrator session before probing dependencies.
func (service *RuntimeReadinessService) GetRuntimeReadiness(
	ctx context.Context,
	_ *connect.Request[adminv1.GetRuntimeReadinessRequest],
) (*connect.Response[adminv1.GetRuntimeReadinessResponse], error) {
	current, err := service.GetCurrentAdminSession(ctx, connect.NewRequest(&adminv1.GetCurrentAdminSessionRequest{}))
	if err != nil {
		return nil, err
	}
	if current.Msg.GetSession().GetKind() != adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL {
		return nil, admin.ErrPermissionDenied
	}
	ordinary := service.readiness.RuntimeSnapshot(ctx, false)
	sensitive := service.readiness.RuntimeSnapshot(ctx, true)
	return connect.NewResponse(&adminv1.GetRuntimeReadinessResponse{
		Ordinary:  runtimeReadinessState(ordinary),
		Sensitive: runtimeReadinessState(sensitive),
	}), nil
}

func runtimeReadinessState(snapshot server.RuntimeReadinessSnapshot) *adminv1.RuntimeReadinessState {
	return &adminv1.RuntimeReadinessState{
		Mode:       snapshot.Mode,
		Ready:      snapshot.Ready,
		Components: snapshot.Components,
	}
}

var _ adminv1connect.AdminAuthServiceHandler = (*RuntimeReadinessService)(nil)
