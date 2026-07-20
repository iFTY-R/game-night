package game

import (
	"bytes"
	"strings"

	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
)

const finishAction gameSDK.Identifier = "session.finish"

func parseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil || parsed.String() != strings.TrimSpace(value) {
		return uuid.Nil, gameruntime.ErrInvalidSessionInput
	}
	return parsed, nil
}

func parseOperationID(value string) (idempotency.OperationID, error) {
	operationID, err := idempotency.ParseOperationID(strings.TrimSpace(value))
	if err != nil {
		return idempotency.OperationID{}, gameruntime.ErrInvalidSessionInput
	}
	return operationID, nil
}

func parseRequestDigest(value []byte) (*idempotency.Digest, error) {
	digest, err := idempotency.NewDigest(value)
	if err != nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	return &digest, nil
}

func configMessage(value *gamev1.GameConfig, expectedGameID gameSDK.GameID) (gameSDK.Message, error) {
	if value == nil || value.GetGameId() != string(expectedGameID) {
		return gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	message := gameSDK.Message{
		MessageType:   gameSDK.Identifier(value.GetMessageType()),
		SchemaVersion: value.GetSchemaVersion(),
		Payload:       append([]byte(nil), value.GetPayload()...),
	}
	if !message.Valid() {
		return gameSDK.Message{}, gameruntime.ErrInvalidSessionInput
	}
	return message, nil
}

func envelopeMessage(value *gamev1.GameEnvelope) (gameSDK.VersionKey, gameSDK.Message, error) {
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

func sessionWire(session gameruntime.Session) *gamev1.GameSessionSummary {
	snapshot := session.Snapshot()
	return &gamev1.GameSessionSummary{
		SessionId: snapshot.ID.String(), RoomId: snapshot.RoomID.String(), GameId: string(snapshot.VersionKey.GameID),
		Version: &gamev1.VersionTuple{
			Engine: string(snapshot.VersionKey.Engine), Protocol: string(snapshot.VersionKey.Protocol), Client: string(snapshot.VersionKey.Client),
		},
		StateVersion: snapshot.State.StateVersion, OwnershipEpoch: snapshot.OwnershipEpoch, Status: statusWire(snapshot.Status),
	}
}

func statusWire(status gameruntime.Status) gamev1.GameSessionStatus {
	switch status {
	case gameruntime.StatusActive:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE
	case gameruntime.StatusSuspended:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED
	case gameruntime.StatusFinished:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_FINISHED
	case gameruntime.StatusCancelled:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED
	default:
		return gamev1.GameSessionStatus_GAME_SESSION_STATUS_UNSPECIFIED
	}
}

func projectionWire(session gameruntime.Session, viewerKind gameSDK.ViewerKind, projection gameSDK.Projection, canFinish bool) *gamev1.GameProjection {
	snapshot := session.Snapshot()
	projection = decorateProjection(projection, canFinish && snapshot.Status == gameruntime.StatusActive)
	actions := make([]string, len(projection.AllowedActions))
	for index, action := range projection.AllowedActions {
		actions[index] = string(action)
	}
	return &gamev1.GameProjection{
		SessionId: snapshot.ID.String(), StateVersion: snapshot.State.StateVersion, ViewerKind: viewerKindWire(viewerKind),
		View: envelopeWire(snapshot.VersionKey, projection.View), AllowedActions: actions,
	}
}

func decorateProjection(projection gameSDK.Projection, canFinish bool) gameSDK.Projection {
	actions := make([]gameSDK.Identifier, 0, len(projection.AllowedActions)+1)
	for _, action := range projection.AllowedActions {
		if action != finishAction {
			actions = append(actions, action)
		}
	}
	if canFinish {
		actions = append(actions, finishAction)
	}
	projection.AllowedActions = actions
	return projection
}

func envelopeWire(key gameSDK.VersionKey, message gameSDK.Message) *gamev1.GameEnvelope {
	return &gamev1.GameEnvelope{
		GameId: string(key.GameID), Version: &gamev1.VersionTuple{
			Engine: string(key.Engine), Protocol: string(key.Protocol), Client: string(key.Client),
		},
		SchemaVersion: message.SchemaVersion, MessageType: string(message.MessageType), Payload: append([]byte(nil), message.Payload...),
	}
}

func viewerKindWire(kind gameSDK.ViewerKind) gamev1.ViewerKind {
	switch kind {
	case gameSDK.ViewerPlayer:
		return gamev1.ViewerKind_VIEWER_KIND_PLAYER
	case gameSDK.ViewerSpectator:
		return gamev1.ViewerKind_VIEWER_KIND_SPECTATOR
	case gameSDK.ViewerReplay:
		return gamev1.ViewerKind_VIEWER_KIND_REPLAY
	default:
		return gamev1.ViewerKind_VIEWER_KIND_UNSPECIFIED
	}
}

func actionReceiptWire(receipt gameruntime.ActionReceipt, replayed bool) *gamev1.GameReceipt {
	snapshot := receipt.Snapshot()
	return &gamev1.GameReceipt{
		SessionId: snapshot.Key.SessionID.String(), OperationId: snapshot.Key.ActionID.Value(), StateVersion: snapshot.StateVersion,
		ResultCode: string(snapshot.ResultCode), RequestDigest: snapshot.RequestDigest.Bytes(), ResultDigest: snapshot.ResultDigest.Bytes(), Replayed: replayed,
	}
}

func systemReceiptWire(receipt gameruntime.SystemReceipt, replayed bool) *gamev1.GameReceipt {
	snapshot := receipt.Snapshot()
	return &gamev1.GameReceipt{
		SessionId: snapshot.Key.SessionID.String(), OperationId: snapshot.Key.OperationID.Value(), StateVersion: snapshot.StateVersion,
		ResultCode: string(snapshot.ResultCode), RequestDigest: snapshot.RequestDigest.Bytes(), ResultDigest: snapshot.ResultDigest.Bytes(), Replayed: replayed,
	}
}

func sameDigest(left *idempotency.Digest, right []byte) bool {
	return left != nil && bytes.Equal(left.Bytes(), right)
}
