// Package projection builds public 789 player, spectator, delta, and replay views.
package projection

import (
	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

// ReplayInitialization is carried only by the first turn.started event.
type ReplayInitialization struct {
	Config         engine.Config
	Players        []engine.Participant
	Pool           []engine.PoolLayer
	TotalPoolTicks dice.Ticks
}

// ReplayEvent retains protocol audit metadata that cannot be reconstructed from
// the engine fact's business-facing reason.
type ReplayEvent struct {
	Event          engine.Event
	Initialization *ReplayInitialization
	Cause          dice789v1.ResolutionCause
	Outcome        dice789v1.TurnOutcome
	// ProtocolEvent is the validated public fact used for lossless replay entries.
	ProtocolEvent *dice789v1.Event
}
