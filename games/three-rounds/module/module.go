package module

import (
	"time"

	"github.com/iFTY-R/game-night/games/three-rounds/engine"
	threeroundsv1 "github.com/iFTY-R/game-night/games/three-rounds/gen/go/game/three_rounds/v1"
	"github.com/iFTY-R/game-night/games/three-rounds/projection"
	game "github.com/iFTY-R/game-night/sdk/go/game"
)

const (
	ProtocolSchemaVersion uint32 = 1
	SnapshotVersion       uint32 = 1

	GameID          game.GameID  = "three-rounds"
	EngineVersion   game.Version = "1.0.0"
	ProtocolVersion game.Version = "1.0.0"
	ClientVersion   game.Version = "1.0.0"

	ConfigMessageType    game.Identifier = "session.config"
	StateMessageType     game.Identifier = "session.state"
	ViewMessageType      game.Identifier = "session.view"
	ViewDeltaMessageType game.Identifier = "view.delta"
	ReplayMessageType    game.Identifier = "session.replay"
	TimerMessageType     game.Identifier = "phase.timeout"

	CommandSubmitSelectionMessage  game.Identifier = "round.submit_selection"
	SystemFinishMessage            game.Identifier = "session.finish"
	EventParticipantRevokedMessage game.Identifier = "participant.revoked"

	TimerID game.Identifier = "phase.timeout"
)

// Module is stateless; one instance can serve every session.
type Module struct{}

// New returns a stateless three-rounds module.
func New() *Module { return &Module{} }

// NewModule is the explicit constructor alias used by registry generation.
func NewModule() *Module { return New() }

var (
	_ game.RuntimeServerGameModule         = (*Module)(nil)
	_ game.ParticipantRevocationGameModule = (*Module)(nil)
	_ game.ReplayProjectingV2GameModule    = (*Module)(nil)
	_ game.ResumeAdjustingGameModule       = (*Module)(nil)
)

// Manifest declares the retained exact release and viewer capabilities.
func (m *Module) Manifest() game.Manifest {
	return game.Manifest{
		GameID:       GameID,
		Versions:     game.VersionSet{Engine: EngineVersion, Protocol: ProtocolVersion, Client: ClientVersion},
		Participants: game.ParticipantLimits{Minimum: engine.MinimumPlayers, Maximum: engine.MaximumPlayers, RecommendedMinimum: 2, RecommendedMaximum: 6},
		Capabilities: game.Capabilities{
			Submission: game.SubmissionModeSimultaneous,
			Timers:     true,
			Spectating: true,
			Replay:     true,
			Reveal:     game.RevealPolicyRuleControlled,
		},
		Presentation: game.PresentationPreferences{
			TableShape:  game.TableShapeElongatedOval,
			Orientation: game.OrientationPortraitPreferred,
			ActionDock:  game.ActionDockBottomEdge,
		},
		Themes: game.ThemePreferences{
			Default:  "classic",
			Fallback: "classic",
			Variants: []game.Identifier{"classic", "felt", "noir"},
		},
	}
}

// Create decodes the frozen config, shuffles deterministically, and starts round one.
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
	state, facts, err := engine.NewState(config, participants, string(request.StartContext.HostUserID), request.Context.Now.UnixMilli(), request.Context.RandomSeed)
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(1, state, facts, request.Context.Now)
}

// HandleCommand accepts only round.submit_selection.
func (m *Module) HandleCommand(snapshot game.Snapshot, request game.CommandRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.Command.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("command request is invalid")
	}
	if request.Command.MessageType != CommandSubmitSelectionMessage {
		return game.Transition{}, malformed("unknown command message type")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var command threeroundsv1.Command
	if err := unmarshalStrict(request.Command.Payload, &command); err != nil || command.GetSubmitSelection() == nil {
		return game.Transition{}, malformed("round.submit_selection payload is invalid")
	}
	selected := stringsToCardIDs(command.GetSubmitSelection().GetCardIds())
	next, facts, err := engine.SubmitSelection(state, string(request.ActorUserID), command.GetSubmitSelection().GetRound(), selected, false, request.Context.Now.UnixMilli())
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// HandleTimer applies only the exact persisted phase token.
func (m *Module) HandleTimer(snapshot game.Snapshot, request game.TimerRequest) (game.Transition, error) {
	if !snapshot.Valid() || !request.Valid() || request.ExpectedStateVersion != snapshot.StateVersion ||
		snapshot.SnapshotVersion != SnapshotVersion || request.TimerID != TimerID || request.Timer.MessageType != TimerMessageType ||
		request.Timer.SchemaVersion != ProtocolSchemaVersion {
		return game.Transition{}, malformed("timer request is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Transition{}, err
	}
	var timerProto threeroundsv1.Timer
	if err := unmarshalStrict(request.Timer.Payload, &timerProto); err != nil {
		return game.Transition{}, malformed("timer payload is invalid")
	}
	phase, ok := phaseFromProto(timerProto.GetPhase())
	if !ok {
		return game.Transition{}, malformed("timer phase is invalid")
	}
	next, facts, err := engine.HandleTimeout(state, engine.Timer{
		Phase:              phase,
		Round:              timerProto.GetRound(),
		DeadlineUnixMillis: timerProto.GetDeadlineUnixMillis(),
		PhaseGeneration:    timerProto.GetPhaseGeneration(),
	}, request.Context.Now.UnixMilli())
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// EncodeParticipantRevoked converts the room-owned fact into this module's canonical protobuf message.
func (*Module) EncodeParticipantRevoked(fact game.ParticipantRevocationFact) (game.Message, error) {
	if !fact.Valid() {
		return game.Message{}, malformed("participant revocation fact is invalid")
	}
	payload, err := marshalDeterministic(&threeroundsv1.ParticipantRevoked{UserId: string(fact.UserID)})
	if err != nil {
		return game.Message{}, malformed("participant revocation encoding failed")
	}
	return game.Message{MessageType: EventParticipantRevokedMessage, SchemaVersion: ProtocolSchemaVersion, Payload: payload}, nil
}

// HandleSystem accepts participant revocation and host-governed session.finish.
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
		var command threeroundsv1.ParticipantRevoked
		if err := unmarshalStrict(request.System.Payload, &command); err != nil || command.GetUserId() == "" {
			return game.Transition{}, malformed("participant.revoked payload is invalid")
		}
		next, facts, err = engine.RevokeParticipant(state, command.GetUserId(), request.Context.Now.UnixMilli())
	case SystemFinishMessage:
		if len(request.System.Payload) != 0 || request.RequestedByUserID == "" {
			return game.Transition{}, malformed("session.finish payload is invalid")
		}
		next, facts, err = engine.Finish(state, engine.FinishHostRequested, string(request.RequestedByUserID))
	default:
		return game.Transition{}, malformed("unknown system message type")
	}
	if err != nil {
		return game.Transition{}, err
	}
	return m.transition(snapshot.StateVersion+1, next, facts, request.Context.Now)
}

// Project returns one complete public view with viewer-specific allowed actions.
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

// ProjectReplay reduces ordered committed events into a replay artifact.
func (m *Module) ProjectReplay(events []game.Event, viewer game.Viewer, policy game.ReplayAccessPolicy) (game.Projection, error) {
	if !viewer.Valid() || viewer.Kind != game.ViewerReplay || !policy.Valid() {
		return game.Projection{}, malformed("replay request is invalid")
	}
	decoded, err := projection.BuildReplay(events, viewer, policy)
	if err != nil {
		return game.Projection{}, err
	}
	payload, err := marshalDeterministic(decoded)
	if err != nil {
		return game.Projection{}, malformed("replay encoding failed")
	}
	return game.Projection{View: game.Message{MessageType: ReplayMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}, nil
}

// ProjectReplayV2 supports runtime-owned cancelled replay metadata while staying event-only.
func (m *Module) ProjectReplayV2(request game.ReplayRequest) (game.Projection, error) {
	if !request.Valid() {
		return game.Projection{}, malformed("replay v2 request is invalid")
	}
	return m.ProjectReplay(request.Events, request.Viewer, request.Policy)
}

// ProjectEvents emits one viewer-safe current-view replacement after validating every committed event envelope.
func (m *Module) ProjectEvents(snapshot game.Snapshot, events []game.VersionedEvent, viewer game.Viewer) (game.EventProjection, error) {
	if !snapshot.Valid() || !viewer.Valid() || viewer.Kind == game.ViewerReplay || len(events) == 0 {
		return game.EventProjection{}, malformed("event projection request is invalid")
	}
	var lastVersion uint64
	for _, event := range events {
		if !event.Valid() || event.StateVersion < lastVersion {
			return game.EventProjection{}, malformed("versioned event ordering is invalid")
		}
		lastVersion = event.StateVersion
		if err := projectionValidateEvent(event.Event.Message); err != nil {
			return game.EventProjection{}, err
		}
	}
	projected, err := m.Project(snapshot, viewer)
	if err != nil {
		return game.EventProjection{}, err
	}
	var view threeroundsv1.View
	if err := unmarshalStrict(projected.View.Payload, &view); err != nil {
		return game.EventProjection{}, malformed("current view cannot be wrapped")
	}
	payload, err := marshalDeterministic(&threeroundsv1.ViewDelta{View: &view})
	if err != nil {
		return game.EventProjection{}, malformed("view delta encoding failed")
	}
	return game.EventProjection{Messages: []game.Message{{MessageType: ViewDeltaMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}}}, nil
}

// Migrate canonicalizes current-schema snapshots and fails closed for unsupported upgrades.
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

// AdjustResumed keeps opaque deadline fields aligned with the runtime-shifted timer set after a pause.
func (m *Module) AdjustResumed(snapshot game.Snapshot, timers []game.TimerIntent) (game.Snapshot, []game.TimerIntent, error) {
	if !snapshot.Valid() {
		return game.Snapshot{}, nil, malformed("resume snapshot is invalid")
	}
	state, err := DecodeState(snapshot.State)
	if err != nil {
		return game.Snapshot{}, nil, err
	}
	adjusted := cloneResumeTimers(timers)
	current := engine.CurrentTimer(state)
	if current == nil {
		return snapshot, adjusted, nil
	}
	if len(adjusted) != 1 || adjusted[0].TimerID != TimerID {
		return game.Snapshot{}, nil, malformed("resume timer set is invalid")
	}
	var token threeroundsv1.Timer
	if err := unmarshalStrict(adjusted[0].Message.Payload, &token); err != nil {
		return game.Snapshot{}, nil, malformed("timer payload is invalid")
	}
	deadline := adjusted[0].DueAt.UnixMilli()
	state.PhaseDeadlineUnixMillis = deadline
	token.DeadlineUnixMillis = deadline
	snapshot.State, err = EncodeState(state)
	if err != nil {
		return game.Snapshot{}, nil, err
	}
	payload, err := marshalDeterministic(&token)
	if err != nil {
		return game.Snapshot{}, nil, malformed("timer encoding failed")
	}
	adjusted[0].Message = game.Message{MessageType: TimerMessageType, SchemaVersion: ProtocolSchemaVersion, Payload: payload}
	return snapshot, adjusted, nil
}

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
		payload, encodeErr := marshalDeterministic(&threeroundsv1.Timer{
			Phase: phaseToProto(timer.Phase), Round: timer.Round, DeadlineUnixMillis: timer.DeadlineUnixMillis, PhaseGeneration: timer.PhaseGeneration,
		})
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

func cloneResumeTimers(values []game.TimerIntent) []game.TimerIntent {
	timers := make([]game.TimerIntent, len(values))
	for index, timer := range values {
		timer.Message = timer.Message.Clone()
		timers[index] = timer
	}
	return timers
}

func malformed(detail string) error {
	return &engine.RuleError{Code: engine.CodeMalformedPayload, Detail: detail}
}

func projectionValidateEvent(message game.Message) error {
	return projection.ValidateEvent(message)
}
