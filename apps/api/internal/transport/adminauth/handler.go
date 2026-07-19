package adminauth

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
)

// NewHandler binds the existing field mapper to mandatory API Cookie effects and caller-owned interceptors.
func NewHandler(service *admin.Service, effects *CookieEffects, options ...connect.HandlerOption) (string, http.Handler, error) {
	adapter, err := admin.NewConnectAdminServiceWithCookieEffects(service, effects)
	if err != nil {
		return "", nil, err
	}
	path, handler := adminv1connect.NewAdminAuthServiceHandler(adapter, options...)
	return path, handler, nil
}
