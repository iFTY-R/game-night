// Package adminidentity assembles the privileged identity adapter without user-domain transport state.
package adminidentity

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

// NewHandler wraps the complete domain adapter while caller-supplied admin interceptors own authentication context.
func NewHandler(service *admin.IdentityService, auth *admin.Service, options ...connect.HandlerOption) (string, http.Handler, error) {
	adapter, err := admin.NewConnectAdminIdentityService(service, auth)
	if err != nil {
		return "", nil, err
	}
	path, handler := adminv1connect.NewAdminIdentityServiceHandler(adapter, options...)
	return path, handler, nil
}
