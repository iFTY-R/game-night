package room

import (
	"context"
	"strings"

	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

// startGameInput validates the platform envelope while leaving game-owned payload validation to the resolved module.
func startGameInput(request *roomv1.StartGameRequest) (
	gameSDK.GameID,
	gameSDK.Message,
	idempotency.OperationID,
	idempotency.Digest,
	error,
) {
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.GetGameId()))
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	config, err := gameConfig(request.GetConfig(), gameID)
	if err != nil {
		return "", gameSDK.Message{}, idempotency.OperationID{}, idempotency.Digest{}, err
	}
	operationID, digest, err := operationBinding(request.GetOperationId(), request.GetRequestDigest())
	return gameID, config, operationID, digest, err
}

// finishGameInput binds a host finish to one exact module release and one opaque module command.
func finishGameInput(request *roomv1.FinishGameRequest) (
	idempotency.OperationID,
	uuid.UUID,
	gameSDK.VersionKey,
	gameSDK.Message,
	idempotency.Digest,
	error,
) {
	operationID, digest, err := operationBinding(request.GetOperationId(), request.GetRequestDigest())
	if err != nil {
		return idempotency.OperationID{}, uuid.Nil, gameSDK.VersionKey{}, gameSDK.Message{}, idempotency.Digest{}, err
	}
	sourceEventID, err := parseUUID(request.GetSourceEventId())
	if err != nil {
		return idempotency.OperationID{}, uuid.Nil, gameSDK.VersionKey{}, gameSDK.Message{}, idempotency.Digest{}, err
	}
	versionKey, command, err := gameEnvelope(request.GetCommand())
	if err != nil || command.MessageType != finishAction {
		return idempotency.OperationID{}, uuid.Nil, gameSDK.VersionKey{}, gameSDK.Message{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	return operationID, sourceEventID, versionKey, command, digest, nil
}

func gameConfig(value *gamev1.GameConfig, expectedGameID gameSDK.GameID) (gameSDK.Message, error) {
	if value == nil || value.GetGameId() != string(expectedGameID) {
		return gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	message := gameSDK.Message{
		MessageType: gameSDK.Identifier(value.GetMessageType()), SchemaVersion: value.GetSchemaVersion(),
		Payload: append([]byte(nil), value.GetPayload()...),
	}
	if !message.Valid() {
		return gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	return message, nil
}

func gameEnvelope(value *gamev1.GameEnvelope) (gameSDK.VersionKey, gameSDK.Message, error) {
	if value == nil || value.GetVersion() == nil {
		return gameSDK.VersionKey{}, gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	gameID, gameErr := gameSDK.ParseGameID(value.GetGameId())
	engine, engineErr := gameSDK.ParseVersion(value.GetVersion().GetEngine())
	protocol, protocolErr := gameSDK.ParseVersion(value.GetVersion().GetProtocol())
	client, clientErr := gameSDK.ParseVersion(value.GetVersion().GetClient())
	key := gameSDK.VersionKey{GameID: gameID, Engine: engine, Protocol: protocol, Client: client}
	message := gameSDK.Message{
		MessageType: gameSDK.Identifier(value.GetMessageType()), SchemaVersion: value.GetSchemaVersion(),
		Payload: append([]byte(nil), value.GetPayload()...),
	}
	if gameErr != nil || engineErr != nil || protocolErr != nil || clientErr != nil || !key.Valid() || !message.Valid() {
		return gameSDK.VersionKey{}, gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	return key, message, nil
}

func operationBinding(value string, digestBytes []byte) (idempotency.OperationID, idempotency.Digest, error) {
	operationID, err := idempotency.ParseOperationID(strings.TrimSpace(value))
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	digest, err := idempotency.NewDigest(digestBytes)
	if err != nil {
		return idempotency.OperationID{}, idempotency.Digest{}, gameruntime.ErrInvalidSessionInput
	}
	return operationID, digest, nil
}

// authorizeFinish uses current host authority and permits only an identical retry after the atomic terminal commit.
func (service *Service) authorizeFinish(
	ctx context.Context,
	actor, roomID, sessionID uuid.UUID,
	request *roomv1.FinishGameRequest,
) (roomDomain.Room, gameruntime.Session, error) {
	room, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	session, err := service.sessions.Get(ctx, sessionID)
	if err != nil {
		return roomDomain.Room{}, gameruntime.Session{}, err
	}
	roomSnapshot, sessionSnapshot := room.Snapshot(), session.Snapshot()
	if sessionSnapshot.RoomID != roomID {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
	}
	if roomSnapshot.HostUserID != actor {
		return roomDomain.Room{}, gameruntime.Session{}, roomDomain.ErrHostRequired
	}
	participant := false
	for _, candidate := range sessionSnapshot.Participants {
		participant = participant || candidate.UserID == actor
	}
	if !participant {
		return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrParticipantNotActive
	}
	if sessionSnapshot.Status.Terminal() {
		if roomSnapshot.Status != roomDomain.RoomStatusPostGame || roomSnapshot.ActiveSessionID != uuid.Nil {
			return roomDomain.Room{}, gameruntime.Session{}, gameruntime.ErrGameSessionIntegrity
		}
		return room, session, nil
	}
	expected := versionDomain(request.GetExpectedVersion())
	if room.Version() != expected || roomSnapshot.ActiveSessionID != sessionID {
		return roomDomain.Room{}, gameruntime.Session{}, roomDomain.ErrRoomVersionConflict
	}
	return room, session, nil
}

// publish emits a recoverable cursor only after PostgreSQL has accepted the authoritative transition.
func (service *Service) publish(ctx context.Context, session gameruntime.Session) error {
	snapshot := session.Snapshot()
	return service.fanout.PublishSessionFanout(ctx, redisstore.SessionFanoutEvent{
		SessionID: snapshot.ID, StateVersion: snapshot.State.StateVersion,
	})
}
