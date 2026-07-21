package replay

import (
	"context"

	"github.com/google/uuid"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

// Repository separates replay resource authorization from the game module's reveal-policy projection.
type Repository interface {
	Authorize(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (game.ReplayAccessPolicy, error)
	Get(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (Access, error)
	SetPolicy(context.Context, SetPolicyCommand) (Access, error)
}
