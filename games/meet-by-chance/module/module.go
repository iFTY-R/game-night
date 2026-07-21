// Package module adapts the pure meet-by-chance rules to the platform game SDK.
package module

import (
	"time"

	"github.com/iFTY-R/game-night/games/meet-by-chance/engine"
	meetv1 "github.com/iFTY-R/game-night/games/meet-by-chance/gen/go/game/meet_by_chance/v1"
	"github.com/iFTY-R/game-night/games/meet-by-chance/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

const (
	// ProtocolSchemaVersion pins every module-owned protobuf envelope.
	ProtocolSchemaVersion uint32 = 1
	// SnapshotVersion versions the opaque SDK snapshot independently from protobuf.
	SnapshotVersion uint32 = 1

	GameID          game.GameID  = "meet-by-chance"
	EngineVersion   game.Version = "1.0.0"
	ProtocolVersion game.Version = "1.0.0"
	ClientVersion   game.Version = "1.0.0"

	ConfigMessageType    game.Identifier = "session.config"
	StateMessageType     game.Identifier = "session.state"
	ViewMessageType      game.Identifier = "session.view"
	ViewDeltaMessageType game.Identifier = "view.delta"
	ReplayMessageType    game.Identifier = "session.replay"
	TimerMessageType     game.Identifier = "action.timeout"
	TimerID              game.Identifier = "action-timeout"

	EventRoundStartedMessage       game.Identifier = "round.started"
	EventDiceRevealedMessage       game.Identifier = "dice.revealed"
	EventHandClassifiedMessage     game.Identifier = "hand.classified"
	EventMatchResolvedMessage      game.Identifier = "match.resolved"
	EventTargetSelectedMessage     game.Identifier = "target.selected"
	EventTargetRerolledMessage     game.Identifier = "target.rerolled"
	EventPenaltyRecordedMessage    game.Identifier = "penalty.recorded"
	EventParticipantRevokedMessage game.Identifier = "participant.revoked"
	EventSessionFinishedMessage    game.Identifier = "session.finished"
	EventSpecial235Message         game.Identifier = "special235.evaluated"
	EventRoundSettledMessage       game.Identifier = "round.settled"

	SystemFinishMessage game.Identifier = "session.finish"
)

// Module is stateless and can be shared by every game session.
type Module struct{}

func New() *Module       { return &Module{} }
func NewModule() *Module { return New() }

var (
	_ game.RuntimeServerGameModule         = (*Module)(nil)
	_ game.ParticipantRevocationGameModule = (*Module)(nil)
)

// Manifest declares the exact release and responsive public presentation contract.
func (m *Module) Manifest() game.Manifest {
	return game.Manifest{
		GameID:       GameID,
		Versions:     game.VersionSet{Engine: EngineVersion, Protocol: ProtocolVersion, Client: ClientVersion},
		Participants: game.ParticipantLimits{Minimum: engine.MinimumPlayers, Maximum: engine.MaximumPlayers, RecommendedMinimum: 3, RecommendedMaximum: 8},
		Capabilities: game.Capabilities{
			Submission: game.SubmissionModeTurnBased,
			Timers:     true,
			Spectating: true,
			Replay:     true,
			Reveal:     game.RevealPolicyRuleControlled,
		},
		Presentation: game.PresentationPreferences{
			TableShape:  game.TableShapeAdaptive,
			Orientation: game.OrientationPortraitPreferred,
			ActionDock:  game.ActionDockBottomEdge,
		},
		Themes: game.ThemePreferences{
			Default: "classic", Fallback: "classic",
			Variants: []game.Identifier{"classic", "copper", "night"},
		},
	}
}

// Create freezes trusted room identity and starts the first deterministic round.
func (m *Module) Create(request game.CreateRequest) (game.Transition, error) {
	if err := request.Validate(m.Manifest().Participants); err != nil {
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
	state, facts, err := engine.NewState(config, participants, string(request.StartContext.HostUserID), request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(1, state, facts, request.Context.Now)
}

// HandleCommand dispatches one authenticated target action. Session finish is
// deliberately excluded from this player-controlled entry point.
func (m *Module) HandleCommand(snapshot game.Snapshot, request game.CommandRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.Command.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("command request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var command meetv1.Command
	if err := unmarshalStrict(request.Command.Payload, &command); err != nil || command.GetCommand() == nil {
		return game.Transition{}, malformed("command payload is invalid")
	}
	actor := string(request.ActorUserID)
	now := request.Context.Now.UnixMilli()
	var next engine.State
	var facts []engine.Event
	switch request.Command.MessageType {
	case projection.ActionReroll:
		if command.GetReroll() == nil {
			return game.Transition{}, malformed("round.reroll payload is invalid")
		}
		next, facts, err = engine.Reroll(state, actor, now, request.Context.RandomSeed)
	case projection.ActionStand:
		if command.GetStand() == nil {
			return game.Transition{}, malformed("round.stand payload is invalid")
		}
		next, facts, err = engine.Stand(state, actor, now, request.Context.RandomSeed)
	default:
		return game.Transition{}, malformed("unknown or system-only command message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// HandleTimer applies only the exact token persisted for the current target decision.
func (m *Module) HandleTimer(snapshot game.Snapshot, request game.TimerRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.TimerID != TimerID ||
		request.Timer.MessageType != TimerMessageType || request.Timer.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("timer request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var value meetv1.ActionTimer
	if err := unmarshalStrict(request.Timer.Payload, &value); err != nil {
		return game.Transition{}, malformed("timer payload is invalid")
	}
	timer, err := actionTimerFromProto(&value)
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
	payload, err := marshalDeterministic(&meetv1.ParticipantRevoked{UserId: string(fact.UserID)})
	if err != nil {
		return game.Message{}, malformed("participant revocation encoding failed")
	}
	return game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// HandleSystem accepts durable participant revocation and runtime-authorized finish operations.
func (m *Module) HandleSystem(snapshot game.Snapshot, request game.SystemRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.System.SchemaVersion != ProtocolSchemaVersion {
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
		var value meetv1.ParticipantRevoked
		if err := unmarshalStrict(request.System.Payload, &value); err != nil || !revocationCommandValid(&value) {
			return game.Transition{}, malformed("participant.revoked payload is invalid")
		}
		next, facts, err = engine.RevokeParticipant(state, value.GetUserId(), request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	case SystemFinishMessage:
		var value meetv1.Command
		if err := unmarshalStrict(request.System.Payload, &value); err != nil || value.GetFinish() == nil || !finishCommandValid(value.GetFinish()) {
			return game.Transition{}, malformed("session.finish payload is invalid")
		}
		next, facts, err = engine.Finish(state, value.GetFinish().GetReason(), value.GetFinish().GetOperatorUserId())
	default:
		return game.Transition{}, malformed("unknown system message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

func finishCommandValid(value *meetv1.Finish) bool {
	if value == nil {
		return false
	}
	if value.GetReason() == "" || value.GetReason() == engine.FinishHostRequested {
		_, err := game.ParseIdentifier(value.GetOperatorUserId())
		return err == nil
	}
	return value.GetReason() == engine.FinishPlatformCancelled && value.GetOperatorUserId() == ""
}

func revocationCommandValid(value *meetv1.ParticipantRevoked) bool {
	return value != nil && value.GetUserId() != "" && proto.Equal(value, &meetv1.ParticipantRevoked{UserId: value.GetUserId()})
}

// Project returns one complete public view with viewer-specific actions.
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
	return game.Projection{
		View:           game.Message{MessageType: ViewMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
		AllowedActions: actions,
	}, nil
}

// ProjectEvents emits a viewer-safe current-view replacement after validating
// every committed event envelope and its state-version order.
func (m *Module) ProjectEvents(snapshot game.Snapshot, events []game.VersionedEvent, viewer game.Viewer) (game.EventProjection, error) {
	if !snapshot.Valid() || !viewer.Valid() || viewer.Kind == game.ViewerReplay || len(events) == 0 {
		return game.EventProjection{}, malformed("event projection request is invalid")
	}
	if err := validateVersionedEvents(events); err != nil {
		return game.EventProjection{}, err
	}
	projected, err := m.Project(snapshot, viewer)
	if err != nil {
		return game.EventProjection{}, err
	}
	var view meetv1.View
	if err := unmarshalStrict(projected.View.Payload, &view); err != nil || len(view.GetPlayers()) != 0 {
		return game.EventProjection{}, malformed("current view cannot be wrapped")
	}
	payload, err := marshalDeterministic(&meetv1.ViewDelta{View: &view})
	if err != nil {
		return game.EventProjection{}, malformed("view delta encoding failed")
	}
	return game.EventProjection{Messages: []game.Message{{
		MessageType: ViewDeltaMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload,
	}}}, nil
}

// ProjectReplay reduces ordered public events to settled rounds only.
func (m *Module) ProjectReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (game.Projection, error) {
	replay, err := projection.BuildReplay(events, viewer, policy)
	if err != nil {
		return game.Projection{}, err
	}
	payload, err := marshalDeterministic(replay)
	if err != nil {
		return game.Projection{}, malformed("replay encoding failed")
	}
	return game.Projection{View: game.Message{MessageType: ReplayMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}, nil
}

// Migrate canonicalizes current-schema snapshots and rejects unsupported paths.
func (m *Module) Migrate(snapshot game.Snapshot, fromVersion, toVersion uint32) (game.Snapshot, error) {
	if !snapshot.Valid() || snapshot.SnapshotVersion != SnapshotVersion || fromVersion != ProtocolSchemaVersion || toVersion != ProtocolSchemaVersion {
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

// transition owns canonical state/events and the complete replacement timer set.
func (m *Module) transition(version uint64, state engine.State, facts []engine.Event, now time.Time) (game.Transition, error) {
	if version == 0 {
		return game.Transition{}, malformed("state version overflow")
	}
	statePayload, err := EncodeState(state)
	if err != nil {
		return game.Transition{}, err
	}
	events, err := encodeEvents(facts, state)
	if err != nil {
		return game.Transition{}, err
	}
	transition := game.Transition{
		Snapshot: game.Snapshot{SnapshotVersion: SnapshotVersion, StateVersion: version, State: statePayload},
		Events:   events, Finished: state.Phase == engine.PhaseFinished,
	}
	if timer := engine.CurrentTimer(state); timer != nil {
		payload, encodeErr := marshalDeterministic(actionTimerToProto(timer))
		if encodeErr != nil {
			return game.Transition{}, malformed("timer encoding failed")
		}
		dueAt := time.UnixMilli(timer.DeadlineUnixMillis).UTC()
		if dueAt.Before(now) {
			// Preserve the authoritative deadline in the token while making a
			// replacement timer schedulable after a racing system transition.
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
