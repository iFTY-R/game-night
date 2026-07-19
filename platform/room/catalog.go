package room

import "context"

// GameCatalog supplies server-authoritative participant constraints from registered game manifests.
type GameCatalog interface {
	MinimumParticipants(context.Context, string) (uint32, error)
}

// DisabledGameCatalog is the explicit production placeholder until the Game SDK registry is delivered.
// It prevents clients from choosing their own minimum participant count during the room stage.
type DisabledGameCatalog struct{}

// NewDisabledGameCatalog keeps StartGame fail-closed without hiding that the game registry is a later dependency.
func NewDisabledGameCatalog() *DisabledGameCatalog { return &DisabledGameCatalog{} }

// MinimumParticipants rejects every game until a signed build-time registry replaces this adapter.
func (*DisabledGameCatalog) MinimumParticipants(context.Context, string) (uint32, error) {
	return 0, ErrGameUnavailable
}

var _ GameCatalog = (*DisabledGameCatalog)(nil)
