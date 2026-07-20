// Package gamewebsocket adapts viewer-safe subscription updates to the public protobuf WebSocket protocol.
package gamewebsocket

import (
	"bytes"
	"errors"

	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

const finishAction game.Identifier = "session.finish"

var (
	ErrInvalidClientFrame = errors.New("invalid realtime client frame")
	ErrInvalidUpdate      = errors.New("invalid realtime subscription update")
)

// parseClientFrame accepts only the canonical deterministic encoding promised by the public protocol.
func parseClientFrame(raw []byte) (*gamev1.ClientFrame, error) {
	if len(raw) == 0 {
		return nil, ErrInvalidClientFrame
	}
	frame := &gamev1.ClientFrame{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, frame); err != nil ||
		len(frame.ProtoReflect().GetUnknown()) != 0 || frame.GetBody() == nil {
		return nil, ErrInvalidClientFrame
	}
	hello := frame.GetHello()
	if hello == nil && frame.GetPing() == nil {
		return nil, ErrInvalidClientFrame
	}
	if hello != nil && (len(hello.GetTicket()) == 0 || len(hello.GetGrant()) == 0) {
		return nil, ErrInvalidClientFrame
	}
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(frame)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, ErrInvalidClientFrame
	}
	return frame, nil
}

// serverFrameForUpdate converts only the Hub's viewer projection surface; authoritative session state has no input path.
func serverFrameForUpdate(
	update subscription.Update,
	authorization subscription.Authorization,
	previousStateVersion uint64,
) (*gamev1.ServerFrame, error) {
	if !update.Valid() || !authorization.Viewer.Valid() || update.SessionID != authorization.SessionID ||
		previousStateVersion == 0 || previousStateVersion > update.StateVersion {
		return nil, ErrInvalidUpdate
	}
	if update.Snapshot() {
		projection := projectionToWire(update, authorization.Viewer.Kind)
		return &gamev1.ServerFrame{Body: &gamev1.ServerFrame_Projection{Projection: projection}}, nil
	}
	if previousStateVersion == update.StateVersion {
		return nil, ErrInvalidUpdate
	}
	messages := make([]*gamev1.GameEnvelope, len(update.Delta.Messages))
	for index, message := range update.Delta.Messages {
		messages[index] = envelopeToWire(update.VersionKey, message)
	}
	delta := &gamev1.GameDelta{
		SessionId: update.SessionID.String(), FromStateVersion: previousStateVersion,
		ToStateVersion: update.StateVersion, ViewerKind: viewerKindToWire(authorization.Viewer.Kind),
		Messages: messages,
	}
	return &gamev1.ServerFrame{Body: &gamev1.ServerFrame_Delta{Delta: delta}}, nil
}

// marshalServerFrame uses deterministic protobuf so clients can reject ambiguous or corrupted envelopes consistently.
func marshalServerFrame(frame *gamev1.ServerFrame) ([]byte, error) {
	if frame == nil || frame.GetBody() == nil {
		return nil, ErrInvalidUpdate
	}
	value, err := (proto.MarshalOptions{Deterministic: true}).Marshal(frame)
	if err != nil || len(value) == 0 {
		return nil, ErrInvalidUpdate
	}
	return value, nil
}

func projectionToWire(update subscription.Update, viewerKind game.ViewerKind) *gamev1.GameProjection {
	actions := make([]string, 0, len(update.Projection.AllowedActions)+1)
	for _, action := range update.Projection.AllowedActions {
		// Platform finish permission follows the current PartyRoom host and cannot be trusted from module state.
		if action != finishAction {
			actions = append(actions, string(action))
		}
	}
	if update.Host {
		actions = append(actions, string(finishAction))
	}
	return &gamev1.GameProjection{
		SessionId: update.SessionID.String(), StateVersion: update.StateVersion,
		ViewerKind: viewerKindToWire(viewerKind), View: envelopeToWire(update.VersionKey, update.Projection.View),
		AllowedActions: actions,
	}
}

func envelopeToWire(version game.VersionKey, message game.Message) *gamev1.GameEnvelope {
	return &gamev1.GameEnvelope{
		GameId: string(version.GameID),
		Version: &gamev1.VersionTuple{
			Engine: string(version.Engine), Protocol: string(version.Protocol), Client: string(version.Client),
		},
		SchemaVersion: message.SchemaVersion, MessageType: string(message.MessageType),
		Payload: append([]byte(nil), message.Payload...),
	}
}

func viewerKindToWire(kind game.ViewerKind) gamev1.ViewerKind {
	switch kind {
	case game.ViewerPlayer:
		return gamev1.ViewerKind_VIEWER_KIND_PLAYER
	case game.ViewerSpectator:
		return gamev1.ViewerKind_VIEWER_KIND_SPECTATOR
	default:
		return gamev1.ViewerKind_VIEWER_KIND_UNSPECIFIED
	}
}
