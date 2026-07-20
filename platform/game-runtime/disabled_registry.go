package gameruntime

import (
	"context"

	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// DisabledRegistry keeps production GameService fail closed until Task 9 registers concrete retained modules.
type DisabledRegistry struct{}

// NewDisabledRegistry returns an immutable registry that never substitutes a placeholder game implementation.
func NewDisabledRegistry() *DisabledRegistry { return &DisabledRegistry{} }

func (*DisabledRegistry) DefaultManifest(context.Context, game.GameID) (game.Manifest, error) {
	return game.Manifest{}, ErrModuleUnavailable
}

func (*DisabledRegistry) DefaultModule(context.Context, game.GameID) (game.ServerGameModule, error) {
	return nil, ErrModuleUnavailable
}

func (*DisabledRegistry) Resolve(game.VersionKey) (game.ServerGameModule, error) {
	return nil, ErrModuleUnavailable
}

var _ Registry = (*DisabledRegistry)(nil)
