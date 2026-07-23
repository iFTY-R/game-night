// Package gamerules adapts the registered game modules' typed configuration
// codecs to the platform-owned room envelope. The platform stores the envelope
// and revision, while each module remains authoritative for rule semantics.
package gamerules

import (
	"context"
	"strings"

	dice789engine "github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789module "github.com/iFTY-R/game-night/games/dice-789/module"
	liarsengine "github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsmodule "github.com/iFTY-R/game-night/games/liars-dice/module"
	meetengine "github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetmodule "github.com/iFTY-R/game-night/games/meet-by-chance/module"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

// Adapter owns only the config codec/default for one registered game.
type Adapter interface {
	Default(playerCount uint32) (gameSDK.Message, error)
	Normalize(gameSDK.Message, uint32) (gameSDK.Message, error)
	Version() gameSDK.VersionKey
}

// Catalog exposes the three currently playable rule adapters through stable IDs.
type Catalog struct {
	adapters map[string]Adapter
}

// NewCatalog constructs the complete rules catalog used by API and tests.
func NewCatalog() *Catalog {
	return &Catalog{adapters: map[string]Adapter{
		string(liarsmodule.GameID):   &liarsAdapter{},
		string(dice789module.GameID): &dice789Adapter{},
		string(meetmodule.GameID):    &meetAdapter{},
	}}
}

// Default returns a canonical envelope for the requested game and player count.
func (catalog *Catalog) Default(ctx context.Context, gameID string, playerCount uint32) (roomDomain.ConfigEnvelope, error) {
	if catalog == nil || ctx == nil {
		return roomDomain.ConfigEnvelope{}, roomDomain.ErrInvalidRoomInput
	}
	adapter, ok := catalog.adapters[strings.TrimSpace(gameID)]
	if !ok {
		return roomDomain.ConfigEnvelope{}, roomDomain.ErrGameUnavailable
	}
	message, err := adapter.Default(playerCount)
	if err != nil {
		return roomDomain.ConfigEnvelope{}, err
	}
	return envelope(adapter.Version(), message), nil
}

// Normalize validates an explicit module payload and returns its canonical bytes.
func (catalog *Catalog) Normalize(ctx context.Context, input roomDomain.ConfigEnvelope, playerCount uint32) (roomDomain.ConfigEnvelope, error) {
	if catalog == nil || ctx == nil || !input.Valid() {
		return roomDomain.ConfigEnvelope{}, roomDomain.ErrInvalidRoomInput
	}
	adapter, ok := catalog.adapters[strings.TrimSpace(input.GameID)]
	if !ok {
		return roomDomain.ConfigEnvelope{}, roomDomain.ErrGameUnavailable
	}
	version := adapter.Version()
	if input.EngineVersion != string(version.Engine) || input.ProtocolVersion != string(version.Protocol) || input.ClientVersion != string(version.Client) {
		return roomDomain.ConfigEnvelope{}, roomDomain.ErrGameUnavailable
	}
	message, err := adapter.Normalize(gameSDK.Message{MessageType: gameSDK.Identifier(input.MessageType), SchemaVersion: input.SchemaVersion, Payload: append([]byte(nil), input.Payload...)}, playerCount)
	if err != nil {
		return roomDomain.ConfigEnvelope{}, err
	}
	return envelope(version, message), nil
}

func envelope(version gameSDK.VersionKey, message gameSDK.Message) roomDomain.ConfigEnvelope {
	return roomDomain.ConfigEnvelope{
		GameID: string(version.GameID), EngineVersion: string(version.Engine), ProtocolVersion: string(version.Protocol), ClientVersion: string(version.Client),
		SchemaVersion: message.SchemaVersion, MessageType: string(message.MessageType), Payload: append([]byte(nil), message.Payload...),
	}
}

func effectivePlayers(value uint32, minimum uint32) uint32 {
	if value < minimum {
		return minimum
	}
	return value
}

type liarsAdapter struct{}

func (*liarsAdapter) Version() gameSDK.VersionKey {
	return gameSDK.VersionKey{GameID: liarsmodule.GameID, Engine: liarsmodule.EngineVersion, Protocol: liarsmodule.ProtocolVersion, Client: liarsmodule.ClientVersion}
}

func (*liarsAdapter) Default(playerCount uint32) (gameSDK.Message, error) {
	return liarsmodule.EncodeConfigForPlayers(liarsengine.DefaultConfig(effectivePlayers(playerCount, liarsengine.MinimumPlayers)), int(effectivePlayers(playerCount, liarsengine.MinimumPlayers)))
}

func (*liarsAdapter) Normalize(message gameSDK.Message, playerCount uint32) (gameSDK.Message, error) {
	players := effectivePlayers(playerCount, liarsengine.MinimumPlayers)
	config, err := liarsmodule.DecodeConfig(message, int(players))
	if err != nil {
		return gameSDK.Message{}, err
	}
	return liarsmodule.EncodeConfigForPlayers(config, int(players))
}

type dice789Adapter struct{}

func (*dice789Adapter) Version() gameSDK.VersionKey {
	return gameSDK.VersionKey{GameID: dice789module.GameID, Engine: dice789module.EngineVersion, Protocol: dice789module.ProtocolVersion, Client: dice789module.ClientVersion}
}

func (*dice789Adapter) Default(playerCount uint32) (gameSDK.Message, error) {
	players := effectivePlayers(playerCount, dice789engine.MinimumPlayers)
	return dice789module.EncodeConfigForPlayers(dice789engine.DefaultConfig(false), int(players))
}

func (*dice789Adapter) Normalize(message gameSDK.Message, playerCount uint32) (gameSDK.Message, error) {
	players := effectivePlayers(playerCount, dice789engine.MinimumPlayers)
	config, err := dice789module.DecodeConfig(message, int(players))
	if err != nil {
		return gameSDK.Message{}, err
	}
	return dice789module.EncodeConfigForPlayers(config, int(players))
}

type meetAdapter struct{}

func (*meetAdapter) Version() gameSDK.VersionKey {
	return gameSDK.VersionKey{GameID: meetmodule.GameID, Engine: meetmodule.EngineVersion, Protocol: meetmodule.ProtocolVersion, Client: meetmodule.ClientVersion}
}

func (*meetAdapter) Default(playerCount uint32) (gameSDK.Message, error) {
	players := effectivePlayers(playerCount, meetengine.MinimumPlayers)
	return meetmodule.EncodeConfigForPlayers(meetengine.DefaultConfig(), int(players))
}

func (*meetAdapter) Normalize(message gameSDK.Message, playerCount uint32) (gameSDK.Message, error) {
	players := effectivePlayers(playerCount, meetengine.MinimumPlayers)
	config, err := meetmodule.DecodeConfig(message, int(players))
	if err != nil {
		return gameSDK.Message{}, err
	}
	return meetmodule.EncodeConfigForPlayers(config, int(players))
}
