// Package server assembles isolated HTTP surfaces and owns API listener lifecycle.
package server

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
)

var errInvalidSurface = errors.New("invalid API surface configuration")

const (
	// MetricsPath exposes process metrics without allowing application code to mutate either service mux.
	MetricsPath = "/metrics"
)

// UserSurfaceConfig contains only user-domain dependencies. Its generated
// handler interface prevents an administrator adapter from being mounted here.
type UserSurfaceConfig struct {
	Identity     identityv1connect.IdentityServiceHandler
	Room         roomv1connect.RoomServiceHandler
	Game         gamev1connect.GameServiceHandler
	Interceptors []connect.Interceptor
	Readiness    *Readiness
}

// AdminSurfaceConfig contains only administrator-domain dependencies. User
// identity handlers cannot be registered through this configuration.
type AdminSurfaceConfig struct {
	Auth         adminv1connect.AdminAuthServiceHandler
	Identity     adminv1connect.AdminIdentityServiceHandler
	Interceptors []connect.Interceptor
	Readiness    *Readiness
}

// UserSurface exposes only IdentityService plus public readiness endpoints.
type UserSurface struct{ handler http.Handler }

// NewUserSurface builds an immutable user mux with its own interceptor chain.
func NewUserSurface(config UserSurfaceConfig) (*UserSurface, error) {
	if config.Identity == nil || config.Room == nil || config.Game == nil || config.Readiness == nil || invalidInterceptors(config.Interceptors) {
		return nil, errInvalidSurface
	}
	options := handlerOptions(config.Interceptors)
	identityPath, identityHandler := identityv1connect.NewIdentityServiceHandler(config.Identity, options...)
	roomPath, roomHandler := roomv1connect.NewRoomServiceHandler(config.Room, options...)
	gamePath, gameHandler := gamev1connect.NewGameServiceHandler(config.Game, options...)
	mux := http.NewServeMux()
	mux.Handle(identityPath, identityHandler)
	mux.Handle(roomPath, roomHandler)
	mux.Handle(gamePath, gameHandler)
	mountReadiness(mux, config.Readiness)
	return &UserSurface{handler: mux}, nil
}

// ServeHTTP keeps the underlying mux private so callers cannot add admin paths after construction.
func (surface *UserSurface) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if surface == nil || surface.handler == nil {
		http.NotFound(writer, request)
		return
	}
	surface.handler.ServeHTTP(writer, request)
}

// AdminSurface exposes only AdminAuthService, AdminIdentityService, and readiness endpoints.
type AdminSurface struct{ handler http.Handler }

// NewAdminSurface builds an immutable administrator mux with an interceptor
// chain independent from the one supplied to NewUserSurface.
func NewAdminSurface(config AdminSurfaceConfig) (*AdminSurface, error) {
	if config.Auth == nil || config.Identity == nil || config.Readiness == nil || invalidInterceptors(config.Interceptors) {
		return nil, errInvalidSurface
	}
	options := handlerOptions(config.Interceptors)
	authPath, authHandler := adminv1connect.NewAdminAuthServiceHandler(config.Auth, options...)
	identityPath, identityHandler := adminv1connect.NewAdminIdentityServiceHandler(config.Identity, options...)
	mux := http.NewServeMux()
	mux.Handle(authPath, authHandler)
	mux.Handle(identityPath, identityHandler)
	mountReadiness(mux, config.Readiness)
	return &AdminSurface{handler: mux}, nil
}

// ServeHTTP keeps the underlying mux private so callers cannot add user paths after construction.
func (surface *AdminSurface) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if surface == nil || surface.handler == nil {
		http.NotFound(writer, request)
		return
	}
	surface.handler.ServeHTTP(writer, request)
}

// HandlerConfig joins isolated user and administrator surfaces on one listener through fixed generated prefixes.
type HandlerConfig struct {
	User    *UserSurface
	Admin   *AdminSurface
	Metrics http.Handler
}

// NewHandler returns the process HTTP handler while preserving each surface's private interceptor chain.
func NewHandler(config HandlerConfig) (http.Handler, error) {
	if config.User == nil || config.Admin == nil || config.Metrics == nil {
		return nil, errInvalidSurface
	}
	mux := http.NewServeMux()
	mux.Handle("/"+identityv1connect.IdentityServiceName+"/", config.User)
	mux.Handle("/"+roomv1connect.RoomServiceName+"/", config.User)
	mux.Handle("/"+gamev1connect.GameServiceName+"/", config.User)
	mux.Handle("/"+adminv1connect.AdminAuthServiceName+"/", config.Admin)
	mux.Handle("/"+adminv1connect.AdminIdentityServiceName+"/", config.Admin)
	// Both immutable surfaces own the same readiness instance; routing through one avoids duplicate public paths.
	mux.Handle(ReadinessPath, config.User)
	mux.Handle(SensitiveReadinessPath, config.User)
	mux.Handle(MetricsPath, config.Metrics)
	return mux, nil
}

func handlerOptions(interceptors []connect.Interceptor) []connect.HandlerOption {
	cloned := append([]connect.Interceptor(nil), interceptors...)
	if len(cloned) == 0 {
		return nil
	}
	return []connect.HandlerOption{connect.WithInterceptors(cloned...)}
}

func invalidInterceptors(interceptors []connect.Interceptor) bool {
	for _, interceptor := range interceptors {
		if interceptor == nil {
			return true
		}
	}
	return false
}
