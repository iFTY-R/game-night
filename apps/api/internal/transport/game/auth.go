package game

import (
	"context"

	"github.com/google/uuid"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
)

// PrincipalAuthenticator verifies identity-owned device and CSRF authority without exposing identity repositories.
type PrincipalAuthenticator interface {
	Authenticate(context.Context, string, string) (uuid.UUID, error)
}

type identityAuthenticator struct {
	service *identityDomain.Service
}

// NewIdentityAuthenticator adapts the established identity domain boundary for game handlers.
func NewIdentityAuthenticator(service *identityDomain.Service) (PrincipalAuthenticator, error) {
	if service == nil {
		return nil, identityDomain.ErrInvalidIdentityRequest
	}
	return &identityAuthenticator{service: service}, nil
}

func (authenticator *identityAuthenticator) Authenticate(ctx context.Context, deviceToken, csrfToken string) (uuid.UUID, error) {
	if authenticator == nil || authenticator.service == nil {
		return uuid.Nil, identityDomain.ErrInvalidIdentityRequest
	}
	user, err := authenticator.service.AuthenticatePrincipal(ctx, identityDomain.AuthenticatePrincipalCommand{
		DeviceToken: deviceToken, CSRFToken: csrfToken,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return user.Snapshot().ID, nil
}

var _ PrincipalAuthenticator = (*identityAuthenticator)(nil)
