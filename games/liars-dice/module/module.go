// Package module adapts the pure liars-dice rules to the platform game SDK.
//
// The adapter owns protocol envelopes, deterministic state transitions, and
// viewer-safe serialization. It deliberately contains no persistence, clock,
// random source, or room authorization logic.
package module

import (
	"bytes"
	"time"

	"github.com/iFTY-R/game-night/games/liars-dice/engine"
	liarsdicev1 "github.com/iFTY-R/game-night/games/liars-dice/gen/go/game/liars_dice/v1"
	"github.com/iFTY-R/game-night/games/liars-dice/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// ProtocolSchemaVersion is the protobuf payload version pinned by this module.
	ProtocolSchemaVersion uint32 = 1
	// SnapshotVersion is the SDK snapshot envelope version, independent of protobuf schema.
	SnapshotVersion uint32 = 1

	// GameID is the immutable catalog and registry identity.
	GameID game.GameID = "liars-dice"
	// EngineVersion pins this exact authoritative rules implementation.
	EngineVersion game.Version = "1.0.0"
	// ProtocolVersion pins the protobuf envelopes emitted by this adapter.
	ProtocolVersion game.Version = "1.0.0"
	// ClientVersion pins the compatible browser game module.
	ClientVersion game.Version = "1.0.0"

	// ConfigMessageType owns the frozen creation payload.
	ConfigMessageType game.Identifier = "session.config"
	// StateMessageType owns authoritative persisted snapshots.
	StateMessageType game.Identifier = "session.state"
	// ViewMessageType owns a complete viewer-specific projection.
	ViewMessageType game.Identifier = "session.view"
	// ViewDeltaMessageType replaces a client's current viewer-safe view.
	ViewDeltaMessageType game.Identifier = "view.delta"
	// ReplayMessageType owns the settled-round replay artifact.
	ReplayMessageType game.Identifier = "session.replay"
	// TimerMessageType carries the exact round, actor, and deadline token.
	TimerMessageType game.Identifier = "action.timeout"
	// TimerID identifies the single replaceable action timer in one session.
	TimerID game.Identifier = "action-timeout"

	// Event message types are stable persisted fact identifiers.
	EventRoundStartedMessage       game.Identifier = "round.started"
	EventBidPlacedMessage          game.Identifier = "bid.placed"
	EventDiceRevealedMessage       game.Identifier = "dice.revealed"
	EventRoundSettledMessage       game.Identifier = "round.settled"
	EventParticipantRevokedMessage game.Identifier = "participant.revoked"
	EventSessionFinishedMessage    game.Identifier = "session.finished"
)

// Module is stateless; a value can be safely shared by all game sessions.
type Module struct{}

// New returns a stateless liars-dice module instance.
func New() *Module { return &Module{} }

// NewModule is an explicit constructor alias used by registry builders.
func NewModule() *Module { return New() }

var (
	_ game.RuntimeServerGameModule         = (*Module)(nil)
	_ game.ParticipantRevocationGameModule = (*Module)(nil)
)

// Manifest declares the exact retained release and viewer capabilities.
func (m *Module) Manifest() game.Manifest {
	return game.Manifest{
		GameID:       GameID,
		Versions:     game.VersionSet{Engine: EngineVersion, Protocol: ProtocolVersion, Client: ClientVersion},
		Participants: game.ParticipantLimits{Minimum: engine.MinimumPlayers, Maximum: engine.MaximumPlayers, RecommendedMinimum: 3, RecommendedMaximum: 6},
		Capabilities: game.Capabilities{
			Submission: game.SubmissionModeTurnBased,
			Timers:     true, Spectating: true, Replay: true,
			Reveal: game.RevealPolicyRuleControlled,
		},
		Presentation: game.PresentationPreferences{
			TableShape:  game.TableShapeCompactOval,
			Orientation: game.OrientationPortraitPreferred,
			ActionDock:  game.ActionDockSeatAnchored,
		},
		Themes: game.ThemePreferences{
			Default: "classic", Fallback: "classic",
			Variants: []game.Identifier{"classic", "copper", "night"},
		},
	}
}

// Create decodes the frozen config, starts the first deterministic round, and
// schedules the complete replacement timer set for the resulting snapshot.
func (m *Module) Create(request game.CreateRequest) (game.Transition, error) {
	manifest := m.Manifest()
	if err := request.Validate(manifest.Participants); err != nil {
		return game.Transition{}, malformed("create request is invalid")
	}
	config, err := DecodeConfig(request.Config, len(request.Participants))
	if err != nil {
		return game.Transition{}, err
	}
	participants := make([]engine.Participant, len(request.Participants))
	for index, participant := range request.Participants {
		participants[index] = engine.Participant{UserID: string(participant.UserID), SeatIndex: participant.SeatIndex}
	}
	state, facts, err := engine.NewState(config, participants, request.StartContext.StartingSeat, request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(1, state, facts, request.Context.Now)
}

// HandleCommand dispatches one authenticated player action by its stable action message type.
func (m *Module) HandleCommand(snapshot game.Snapshot, request game.CommandRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion || snapshot.SnapshotVersion != SnapshotVersion || request.Command.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("command request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var next engine.State
	var facts []engine.Event
	var command liarsdicev1.Command
	if err := unmarshalStrict(request.Command.Payload, &command); err != nil || command.GetCommand() == nil {
		return game.Transition{}, malformed("command payload is invalid")
	}
	switch request.Command.MessageType {
	case projection.ActionBid:
		placeBid := command.GetPlaceBid()
		if placeBid == nil || placeBid.GetBid() == nil {
			return game.Transition{}, malformed("round.bid payload is invalid")
		}
		bid, err := bidFromProto(placeBid.GetBid())
		if err != nil {
			return game.Transition{}, err
		}
		next, facts, err = engine.PlaceBid(state, string(request.ActorUserID), bid, request.Context.Now.UnixMilli())
	case projection.ActionOpen:
		if command.GetOpenDice() == nil {
			return game.Transition{}, malformed("round.open payload is invalid")
		}
		next, facts, err = engine.OpenDice(state, string(request.ActorUserID), request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	default:
		return game.Transition{}, malformed("unknown command message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// HandleTimer applies only the exact timer intent persisted for the current actor and round.
func (m *Module) HandleTimer(snapshot game.Snapshot, request game.TimerRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion || snapshot.SnapshotVersion != SnapshotVersion || request.TimerID != TimerID || request.Timer.MessageType != TimerMessageType || request.Timer.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("timer request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var timerMessage liarsdicev1.ActionTimer
	if err := unmarshalStrict(request.Timer.Payload, &timerMessage); err != nil {
		return game.Transition{}, malformed("timer payload is invalid")
	}
	timer, err := actionTimerFromProto(&timerMessage)
	if err != nil {
		return game.Transition{}, err
	}
	next, facts, err := engine.HandleTimeout(state, timer, request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// EncodeParticipantRevoked converts the room-owned fact into this module's canonical protobuf command.
func (*Module) EncodeParticipantRevoked(fact game.ParticipantRevocationFact) (game.Message, error) {
	if !fact.Valid() {
		return game.Message{}, malformed("participant revocation fact is invalid")
	}
	payload, err := marshalDeterministic(&liarsdicev1.ParticipantRevoked{UserId: string(fact.UserID)})
	if err != nil {
		return game.Message{}, malformed("participant revocation encoding failed")
	}
	return game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// HandleSystem accepts runtime-originated revocation and host/platform finish facts.
func (m *Module) HandleSystem(snapshot game.Snapshot, request game.SystemRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion || snapshot.SnapshotVersion != SnapshotVersion || request.System.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("system request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var next engine.State
	var facts []engine.Event
	switch request.System.MessageType {
	case EventParticipantRevokedMessage:
		var command liarsdicev1.ParticipantRevoked
		if err := unmarshalStrict(request.System.Payload, &command); err != nil || command.GetUserId() == "" {
			return game.Transition{}, malformed("participant.revoked payload is invalid")
		}
		next, facts, err = engine.RevokeParticipant(state, command.GetUserId(), request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	case game.Identifier("session.finish"):
		var command liarsdicev1.Command
		if err := unmarshalStrict(request.System.Payload, &command); err != nil || command.GetFinish() == nil {
			return game.Transition{}, malformed("session.finish payload is invalid")
		}
		next, facts, err = engine.Finish(state, engine.FinishHostRequested)
	default:
		return game.Transition{}, malformed("unknown system message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// Project returns a complete viewer-safe protobuf view. Private dice are only
// copied into a player view for that player's own user ID.
func (m *Module) Project(snapshot game.Snapshot, viewer game.Viewer) (game.Projection, error) {
	if !snapshot.Valid() || !viewer.Valid() || viewer.Kind == game.ViewerReplay {
		return game.Projection{}, malformed("projection request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Projection{}, err
	}
	view, actions, err := projection.BuildView(state, viewer)
	if err != nil {
		return game.Projection{}, err
	}
	payload, err := marshalDeterministic(view)
	if err != nil {
		return game.Projection{}, malformed("view encoding failed")
	}
	return game.Projection{View: game.Message{MessageType: ViewMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, AllowedActions: actions}, nil
}

// ProjectReplay derives a settled-round-only artifact. The viewer/policy have
// already been authorized by the platform, but both are still validated here.
func (m *Module) ProjectReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (game.Projection, error) {
	if !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return game.Projection{}, malformed("replay request is invalid")
	}
	engineEvents, err := decodeEvents(events)
	if err != nil {
		return game.Projection{}, err
	}
	replay, err := projection.BuildReplay(engineEvents)
	if err != nil {
		return game.Projection{}, err
	}
	payload, err := marshalDeterministic(replay)
	if err != nil {
		return game.Projection{}, malformed("replay encoding failed")
	}
	return game.Projection{View: game.Message{MessageType: ReplayMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}, nil
}

// ProjectEvents emits one viewer-safe current-view delta after validating every
// committed event envelope. A full current view avoids leaking event-specific
// private fields while retaining deterministic reconnect semantics.
func (m *Module) ProjectEvents(snapshot game.Snapshot, events []game.VersionedEvent, viewer game.Viewer) (game.EventProjection, error) {
	if !snapshot.Valid() || !viewer.Valid() || viewer.Kind == game.ViewerReplay || len(events) == 0 {
		return game.EventProjection{}, malformed("event projection request is invalid")
	}
	if err := validateVersionedEvents(events); err != nil {
		return game.EventProjection{}, err
	}
	projectionValue, err := m.Project(snapshot, viewer)
	if err != nil {
		return game.EventProjection{}, err
	}
	var view liarsdicev1.View
	if err := unmarshalStrict(projectionValue.View.Payload, &view); err != nil {
		return game.EventProjection{}, malformed("current view cannot be wrapped")
	}
	payload, err := marshalDeterministic(&liarsdicev1.ViewDelta{View: &view})
	if err != nil {
		return game.EventProjection{}, malformed("view delta encoding failed")
	}
	return game.EventProjection{Messages: []game.Message{{MessageType: ViewDeltaMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}}, nil
}

// Migrate canonicalizes a retained snapshot when the source and destination
// schema are the current version. Unsupported upgrades fail closed.
func (m *Module) Migrate(snapshot game.Snapshot, fromVersion, toVersion uint32) (game.Snapshot, error) {
	if !snapshot.Valid() || fromVersion != ProtocolSchemaVersion || toVersion != ProtocolSchemaVersion || snapshot.SnapshotVersion != SnapshotVersion {
		return game.Snapshot{}, &engine.RuleError{Code: engine.CodeUnsupportedMigration, Detail: "schema version is not supported"}
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Snapshot{}, err
	}
	encoded, err := EncodeState(state)
	if err != nil {
		return game.Snapshot{}, err
	}
	snapshot.State = encoded
	return snapshot, nil
}

// transition centralizes canonical snapshot/event encoding and the complete
// next-state timer replacement set for every module entry point.
func (m *Module) transition(version uint64, state engine.State, facts []engine.Event, now time.Time) (game.Transition, error) {
	if version == 0 {
		return game.Transition{}, malformed("state version overflow")
	}
	statePayload, err := EncodeState(state)
	if err != nil {
		return game.Transition{}, err
	}
	events, err := encodeEvents(facts)
	if err != nil {
		return game.Transition{}, err
	}
	transition := game.Transition{
		Snapshot: game.Snapshot{SnapshotVersion: SnapshotVersion, StateVersion: version, State: statePayload},
		Events:   events,
		Finished: state.Phase == engine.PhaseFinished,
	}
	if timer := engine.CurrentTimer(state); timer != nil {
		payload, encodeErr := marshalDeterministic(actionTimerToProto(timer))
		if encodeErr != nil {
			return game.Transition{}, malformed("timer encoding failed")
		}
		dueAt := time.UnixMilli(timer.DeadlineUnixMillis).UTC()
		if dueAt.Before(now) {
			// System transitions may race a due timer. Preserve the original
			// deadline inside the payload while making the replacement schedulable.
			dueAt = now.Round(0).UTC()
		}
		transition.Timers = []game.TimerIntent{{
			TimerID: TimerID, DueAt: dueAt,
			Message: game.Message{MessageType: TimerMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
		}}
	}
	if err := transition.Validate(version-1, now.Round(0).UTC()); err != nil {
		return game.Transition{}, malformed("transition violates sdk contract")
	}
	return transition, nil
}

func malformed(detail string) error {
	return &engine.RuleError{Code: engine.CodeMalformedPayload, Detail: detail}
}

func marshalDeterministic(message proto.Message) ([]byte, error) {
	if message == nil {
		return nil, malformed("nil protobuf message")
	}
	return (proto.MarshalOptions{Deterministic: true}).Marshal(message)
}

// unmarshalStrict rejects unknown fields and non-canonical wire encodings so
// the same logical command has exactly one persisted byte representation.
func unmarshalStrict(payload []byte, message proto.Message) error {
	if message == nil || len(payload) > game.MaximumMessageBytes {
		return malformed("protobuf payload exceeds module bound")
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, message); err != nil || hasUnknown(message.ProtoReflect()) {
		return malformed("protobuf payload is malformed or contains unknown fields")
	}
	canonical, err := marshalDeterministic(message)
	if err != nil || !bytes.Equal(canonical, payload) {
		return malformed("protobuf payload is not canonical")
	}
	return nil
}

func hasUnknown(message protoreflect.Message) bool {
	if len(message.GetUnknown()) != 0 {
		return true
	}
	unknown := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsList() && (field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind) {
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if hasUnknown(list.Get(index).Message()) {
					unknown = true
					return false
				}
			}
		} else if field.IsMap() && (field.MapValue().Kind() == protoreflect.MessageKind || field.MapValue().Kind() == protoreflect.GroupKind) {
			value.Map().Range(func(_ protoreflect.MapKey, nested protoreflect.Value) bool {
				if hasUnknown(nested.Message()) {
					unknown = true
					return false
				}
				return true
			})
		} else if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			if hasUnknown(value.Message()) {
				unknown = true
			}
		}
		return !unknown
	})
	return unknown
}
