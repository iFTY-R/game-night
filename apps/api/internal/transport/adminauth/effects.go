// Package adminauth assembles administrator authentication transport state around the domain Connect adapter.
package adminauth

import (
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/platform/admin"
)

// CookieEffects implements the admin-owned delivery port with API-owned Cookie names and attributes.
type CookieEffects struct{ manager *cookies.Manager }

// NewCookieEffects rejects runtime assembly without the isolated administrator Cookie manager.
func NewCookieEffects(manager *cookies.Manager) (*CookieEffects, error) {
	if manager == nil {
		return nil, admin.ErrInvalidInput
	}
	return &CookieEffects{manager: manager}, nil
}

// SetAdminChallenge installs only the strict administrator challenge Cookie.
func (effects *CookieEffects) SetAdminChallenge(header admin.HeaderWriter, issued admin.IssuedChallenge) error {
	return effects.manager.SetAdminChallenge(header, issued)
}

// SetAdminSession atomically emits the administrator bearer and CSRF Cookie pair.
func (effects *CookieEffects) SetAdminSession(header admin.HeaderWriter, issued admin.IssuedSession) error {
	return effects.manager.SetAdminSession(header, issued)
}

// ClearAdminSession expires both administrator session Cookies with their issuance attributes.
func (effects *CookieEffects) ClearAdminSession(header admin.HeaderWriter) error {
	return effects.manager.ClearAdminSession(header)
}

var _ admin.AdminCookieEffects = (*CookieEffects)(nil)
