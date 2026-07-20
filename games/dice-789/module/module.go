// Package module adapts the pure dice-789 rules to the platform game SDK.
package module

import (
	"time"

	"github.com/iFTY-R/game-night/games/dice-789/engine"
	dice789v1 "github.com/iFTY-R/game-night/games/dice-789/gen/go/game/dice789/v1"
	"github.com/iFTY-R/game-night/games/dice-789/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"github.com/iFTY-R/game-night/sdk/go/game/dice"
)

const (
	// ProtocolSchemaVersion is pinned by every module-owned protobuf envelope.
	ProtocolSchemaVersion uint32 = 1
	// SnapshotVersion versions the opaque SDK snapshot independently from protobuf.
	SnapshotVersion uint32 = 1

	GameID          game.GameID  = "dice-789"
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

	EventTurnStartedMessage        game.Identifier = "turn.started"
	EventDiceRolledMessage         game.Identifier = "dice.rolled"
	EventEffectSelectedMessage     game.Identifier = "effect.selected"
	EventTargetSelectedMessage     game.Identifier = "target.selected"
	EventPoolChangedMessage        game.Identifier = "pot.changed"
	EventPenaltyRecordedMessage    game.Identifier = "penalty.recorded"
	EventDirectionChangedMessage   game.Identifier = "direction.changed"
	EventTurnSettledMessage        game.Identifier = "turn.settled"
	EventTurnDroppedMessage        game.Identifier = "turn.dropped_reported"
	EventParticipantRevokedMessage game.Identifier = "participant.revoked"
	EventSessionFinishedMessage    game.Identifier = "session.finished"

	SystemFinishMessage game.Identifier = "session.finish"
)

// Module is stateless and can be shared by every 789 session.
type Module struct{}

func New() *Module       { return &Module{} }
func NewModule() *Module { return New() }

var _ game.RuntimeServerGameModule = (*Module)(nil)

// Manifest declares the exact retained release and public projection capabilities.
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
			Variants: []game.Identifier{"classic", "stacked", "arcade"},
		},
	}
}

// Create validates trusted room context and schedules the first exact timer.
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
	state, facts, err := engine.NewState(
		config,
		participants,
		string(request.StartContext.HostUserID),
		request.StartContext.StartingSeat,
		request.Context.Now.UnixMilli(),
		request.Context.RandomSeed,
	)
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(1, state, facts, request.Context.Now)
}

// HandleCommand dispatches one authenticated and exact-version player action.
func (m *Module) HandleCommand(snapshot game.Snapshot, request game.CommandRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.Command.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("command request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var command dice789v1.Command
	if err := unmarshalStrict(request.Command.Payload, &command); err != nil || command.GetCommand() == nil {
		return game.Transition{}, malformed("command payload is invalid")
	}
	actor := string(request.ActorUserID)
	now := request.Context.Now.UnixMilli()
	var next engine.State
	var facts []engine.Event
	switch request.Command.MessageType {
	case projection.ActionRoll:
		if command.GetRoll() == nil {
			return game.Transition{}, malformed("turn.roll payload is invalid")
		}
		next, facts, err = engine.Roll(state, actor, now, request.Context.RandomSeed)
	case projection.ActionConfirmLanded:
		if command.GetConfirmLanded() == nil {
			return game.Transition{}, malformed("turn.confirm_landed payload is invalid")
		}
		next, facts, err = engine.ConfirmLanded(state, actor, now)
	case projection.ActionAdd:
		value := command.GetAddToPool()
		if value == nil {
			return game.Transition{}, malformed("pot.add payload is invalid")
		}
		next, facts, err = engine.AddToPool(state, actor, dice.Ticks(value.GetTicks()), now)
	case projection.ActionChooseTarget:
		value := command.GetChooseTarget()
		if value == nil || value.GetUserId() == "" {
			return game.Transition{}, malformed("turn.choose_target payload is invalid")
		}
		next, facts, err = engine.ChooseTarget(state, actor, value.GetUserId(), now)
	case projection.ActionReroll:
		if command.GetReroll() == nil {
			return game.Transition{}, malformed("turn.reroll payload is invalid")
		}
		next, facts, err = engine.Reroll(state, actor, now)
	case projection.ActionPass:
		if command.GetPass() == nil {
			return game.Transition{}, malformed("turn.pass payload is invalid")
		}
		next, facts, err = engine.Pass(state, actor, now)
	case projection.ActionReportDropped:
		value := command.GetReportDropped()
		if value == nil || value.GetReason() == "" {
			return game.Transition{}, malformed("turn.report_dropped payload is invalid")
		}
		next, facts, err = engine.ReportDropped(state, actor, string(request.ActionID), value.GetReason(), now)
	default:
		return game.Transition{}, malformed("unknown or system-only command message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// HandleTimer applies only the persisted timer matching every pending-effect field.
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
	var value dice789v1.ActionTimer
	if err := unmarshalStrict(request.Timer.Payload, &value); err != nil {
		return game.Transition{}, malformed("timer payload is invalid")
	}
	timer, err := actionTimerFromProto(&value)
	if err != nil {
		return game.Transition{}, err
	}
	next, facts, err := engine.HandleTimeout(state, timer, request.Context.Now.UnixMilli())
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
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
		var value dice789v1.ParticipantRevoked
		if err := unmarshalStrict(request.System.Payload, &value); err != nil || !revocationCommandValid(&value) {
			return game.Transition{}, malformed("participant.revoked payload is invalid")
		}
		next, facts, err = engine.RevokeParticipant(state, value.GetUserId(), request.Context.Now.UnixMilli())
	case SystemFinishMessage:
		var value dice789v1.Command
		if err := unmarshalStrict(request.System.Payload, &value); err != nil || value.GetFinish() == nil || !finishCommandValid(value.GetFinish()) {
			return game.Transition{}, malformed("session.finish payload is invalid")
		}
		next, facts, err = engine.Finish(state, value.GetFinish().GetReason())
		if err == nil && len(facts) == 1 {
			facts[0].OperatorUserID = value.GetFinish().GetOperatorUserId()
		}
	default:
		return game.Transition{}, malformed("unknown system message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

func finishCommandValid(value *dice789v1.Finish) bool {
	if value == nil {
		return false
	}
	reason := value.GetReason()
	if reason == "" || reason == engine.FinishHostRequested {
		_, err := game.ParseIdentifier(value.GetOperatorUserId())
		return err == nil
	}
	return reason == engine.FinishPlatformCancelled && value.GetOperatorUserId() == ""
}

func revocationCommandValid(value *dice789v1.ParticipantRevoked) bool {
	return value != nil && value.GetUserId() != "" && value.GetTurn() == 0 &&
		value.GetPhaseBefore() == dice789v1.Phase_PHASE_UNSPECIFIED && value.GetEffect() == dice789v1.Effect_EFFECT_UNSPECIFIED &&
		value.GetSourceUserId() == "" && value.GetTargetUserId() == "" && !value.GetPendingEffectCancelled() &&
		!value.GetTargetSelectionReopened() && value.GetNextUserId() == ""
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

// ProjectReplay strictly reduces ordered public events into a deterministic replay artifact.
func (m *Module) ProjectReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (game.Projection, error) {
	if !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return game.Projection{}, malformed("replay request is invalid")
	}
	engineEvents, err := decodeEvents(events)
	if err != nil {
		return game.Projection{}, err
	}
	replay, err := projection.BuildReplayWithInitialization(engineEvents)
	if err != nil {
		return game.Projection{}, err
	}
	payload, err := marshalDeterministic(replay)
	if err != nil {
		return game.Projection{}, malformed("replay encoding failed")
	}
	return game.Projection{View: game.Message{MessageType: ReplayMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}, nil
}

// ProjectEvents emits a current-view replacement after validating every public event envelope.
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
	var view dice789v1.View
	if err := unmarshalStrict(projected.View.Payload, &view); err != nil {
		return game.EventProjection{}, malformed("current view cannot be wrapped")
	}
	payload, err := marshalDeterministic(&dice789v1.ViewDelta{View: &view})
	if err != nil {
		return game.EventProjection{}, malformed("view delta encoding failed")
	}
	return game.EventProjection{Messages: []game.Message{{
		MessageType: ViewDeltaMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload,
	}}}, nil
}

// Migrate canonicalizes current-schema snapshots and fails closed for unsupported versions.
func (m *Module) Migrate(snapshot game.Snapshot, fromVersion, toVersion uint32) (game.Snapshot, error) {
	if !snapshot.Valid() || snapshot.SnapshotVersion != SnapshotVersion ||
		fromVersion != ProtocolSchemaVersion || toVersion != ProtocolSchemaVersion {
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
	state = normalizeState(state)
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
			dueAt = now.Round(0).UTC()
		}
		transition.Timers = []game.TimerIntent{{
			TimerID: TimerID,
			DueAt:   dueAt,
			Message: game.Message{MessageType: TimerMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload},
		}}
	}
	if err := transition.Validate(version-1, now.Round(0).UTC()); err != nil {
		return game.Transition{}, malformed("transition violates sdk contract")
	}
	return transition, nil
}
