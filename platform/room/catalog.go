package room

import (
	"context"
	"errors"
	"strings"

	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

// GameCatalog supplies server-authoritative participant constraints from registered game manifests.
type GameCatalog interface {
	ParticipantLimits(context.Context, string) (gameSDK.ParticipantLimits, error)
}

// ManifestRegistry is the exact build-time registry surface needed by PartyRoom session admission.
type ManifestRegistry interface {
	DefaultManifest(context.Context, gameSDK.GameID) (gameSDK.Manifest, error)
}

// RegisteredGameCatalog adapts validated SDK manifests without exposing concrete game modules to PartyRoom.
type RegisteredGameCatalog struct {
	registry ManifestRegistry
}

// NewRegisteredGameCatalog requires an immutable registry before room creation can reference game metadata.
func NewRegisteredGameCatalog(registry ManifestRegistry) (*RegisteredGameCatalog, error) {
	if registry == nil {
		return nil, ErrInvalidRoomInput
	}
	return &RegisteredGameCatalog{registry: registry}, nil
}

// ParticipantLimits maps registry absence or invalid metadata to the stable room-level unavailable result.
func (catalog *RegisteredGameCatalog) ParticipantLimits(ctx context.Context, gameID string) (gameSDK.ParticipantLimits, error) {
	if catalog == nil || catalog.registry == nil || ctx == nil {
		return gameSDK.ParticipantLimits{}, ErrGameUnavailable
	}
	parsedID, err := gameSDK.ParseGameID(strings.TrimSpace(gameID))
	if err != nil {
		return gameSDK.ParticipantLimits{}, ErrGameUnavailable
	}
	manifest, err := catalog.registry.DefaultManifest(ctx, parsedID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return gameSDK.ParticipantLimits{}, err
		}
		return gameSDK.ParticipantLimits{}, ErrGameUnavailable
	}
	validated, err := gameSDK.ValidateManifest(manifest)
	if err != nil || validated.GameID != parsedID {
		return gameSDK.ParticipantLimits{}, ErrGameUnavailable
	}
	return validated.Participants, nil
}

// DisabledGameCatalog is the production placeholder until concrete modules and atomic GameSession creation are delivered.
// It prevents clients from choosing their own minimum participant count during the room stage.
type DisabledGameCatalog struct{}

// NewDisabledGameCatalog keeps StartGame fail-closed while the session runtime remains unavailable.
func NewDisabledGameCatalog() *DisabledGameCatalog { return &DisabledGameCatalog{} }

// ParticipantLimits rejects every game until GameSession creation and an exact module registry are wired atomically.
func (*DisabledGameCatalog) ParticipantLimits(context.Context, string) (gameSDK.ParticipantLimits, error) {
	return gameSDK.ParticipantLimits{}, ErrGameUnavailable
}

var _ GameCatalog = (*DisabledGameCatalog)(nil)
var _ GameCatalog = (*RegisteredGameCatalog)(nil)
