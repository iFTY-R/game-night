package adminauth

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

// NewHandler binds the existing field mapper to mandatory API Cookie effects and caller-owned interceptors.
func NewHandler(service *admin.Service, effects *CookieEffects, readiness *server.Readiness, options ...connect.HandlerOption) (string, http.Handler, error) {
	adapter, err := NewRuntimeReadinessService(service, effects, readiness)
	if err != nil {
		return "", nil, err
	}
	path, handler := adminv1connect.NewAdminAuthServiceHandler(adapter, options...)
	return path, handler, nil
}
