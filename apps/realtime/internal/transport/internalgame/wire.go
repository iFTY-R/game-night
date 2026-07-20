package internalgame

import (
	"time"

	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	roomdomain "github.com/iFTY-R/game-night/platform/room"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func roomToWire(room roomdomain.Room) *realtimev1.RoomSnapshot {
	snapshot := room.Snapshot()
	members := make([]*realtimev1.RoomMember, 0, len(snapshot.Members))
	for _, member := range snapshot.Members {
		members = append(members, &realtimev1.RoomMember{
			UserId: member.UserID.String(), Role: string(member.Role), RequestedRole: string(member.RequestedRole),
			SeatIndex: member.SeatIndex, JoinedAt: timeToWire(member.JoinedAt), LastSeenAt: timeToWire(member.LastSeenAt),
		})
	}
	return &realtimev1.RoomSnapshot{
		RoomId: snapshot.ID.String(), RoomCode: snapshot.RoomCode, Visibility: string(snapshot.Visibility),
		Status: string(snapshot.Status), HostUserId: snapshot.HostUserID.String(),
		ParticipantCapacity:  snapshot.ParticipantCapacity,
		ParticipantAdmission: string(snapshot.ParticipantAdmission), SpectatorAdmission: string(snapshot.SpectatorAdmission),
		Members: members, ActiveSessionId: optionalUUID(snapshot.ActiveSessionID), ActiveGameId: snapshot.ActiveGameID,
		RoomVersion: snapshot.RoomVersion, MembershipVersion: snapshot.MembershipVersion,
		CreatedAt: timeToWire(snapshot.CreatedAt), UpdatedAt: timeToWire(snapshot.UpdatedAt),
	}
}

func roomFromWire(value *realtimev1.RoomSnapshot) (roomdomain.Room, error) {
	if value == nil {
		return roomdomain.Room{}, gameruntime.ErrInvalidSessionInput
	}
	roomID, err := requiredUUID(value.GetRoomId())
	if err != nil {
		return roomdomain.Room{}, err
	}
	hostUserID, err := requiredUUID(value.GetHostUserId())
	if err != nil {
		return roomdomain.Room{}, err
	}
	activeSessionID, err := optionalUUIDFromWire(value.GetActiveSessionId())
	if err != nil {
		return roomdomain.Room{}, err
	}
	createdAt, err := requiredTime(value.GetCreatedAt())
	if err != nil {
		return roomdomain.Room{}, err
	}
	updatedAt, err := requiredTime(value.GetUpdatedAt())
	if err != nil {
		return roomdomain.Room{}, err
	}
	members := make([]roomdomain.MemberSnapshot, 0, len(value.GetMembers()))
	for _, member := range value.GetMembers() {
		if member == nil {
			return roomdomain.Room{}, gameruntime.ErrInvalidSessionInput
		}
		userID, parseErr := requiredUUID(member.GetUserId())
		if parseErr != nil {
			return roomdomain.Room{}, parseErr
		}
		joinedAt, parseErr := requiredTime(member.GetJoinedAt())
		if parseErr != nil {
			return roomdomain.Room{}, parseErr
		}
		lastSeenAt, parseErr := requiredTime(member.GetLastSeenAt())
		if parseErr != nil {
			return roomdomain.Room{}, parseErr
		}
		members = append(members, roomdomain.MemberSnapshot{
			UserID: userID, Role: roomdomain.MemberRole(member.GetRole()), RequestedRole: roomdomain.MemberRole(member.GetRequestedRole()),
			SeatIndex: member.GetSeatIndex(), JoinedAt: joinedAt, LastSeenAt: lastSeenAt,
		})
	}
	return roomdomain.Restore(roomdomain.RoomSnapshot{
		ID: roomID, RoomCode: value.GetRoomCode(), Visibility: roomdomain.Visibility(value.GetVisibility()),
		Status: roomdomain.RoomStatus(value.GetStatus()), HostUserID: hostUserID,
		ParticipantCapacity:  value.GetParticipantCapacity(),
		ParticipantAdmission: roomdomain.AdmissionMode(value.GetParticipantAdmission()),
		SpectatorAdmission:   roomdomain.AdmissionMode(value.GetSpectatorAdmission()),
		Members:              members, ActiveSessionID: activeSessionID, ActiveGameID: value.GetActiveGameId(),
		RoomVersion: value.GetRoomVersion(), MembershipVersion: value.GetMembershipVersion(),
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	})
}

func sessionToWire(session gameruntime.Session) *realtimev1.SessionSnapshot {
	snapshot := session.Snapshot()
	participants := make([]*realtimev1.Participant, 0, len(snapshot.Participants))
	for _, participant := range snapshot.Participants {
		participants = append(participants, &realtimev1.Participant{
			UserId: participant.UserID.String(), SeatIndex: participant.SeatIndex,
		})
	}
	timers := make([]*realtimev1.Timer, 0, len(snapshot.Timers))
	for _, timer := range snapshot.Timers {
		timers = append(timers, &realtimev1.Timer{
			TimerId: string(timer.TimerID), ExpectedStateVersion: timer.ExpectedStateVersion,
			DueAt: timeToWire(timer.DueAt), Message: envelopeToWire(snapshot.VersionKey, timer.Message),
		})
	}
	return &realtimev1.SessionSnapshot{
		SessionId: snapshot.ID.String(), RoomId: snapshot.RoomID.String(), GameId: string(snapshot.VersionKey.GameID),
		Version: versionToWire(snapshot.VersionKey), OwnershipEpoch: snapshot.OwnershipEpoch,
		Participants: participants, SnapshotVersion: snapshot.State.SnapshotVersion, StateVersion: snapshot.State.StateVersion,
		AuthoritativeState: envelopeToWire(snapshot.VersionKey, snapshot.State.State), Timers: timers,
		NextDeadlineAt: timeToWire(snapshot.NextDeadlineAt), Status: statusToWire(snapshot.Status),
		StartedAt: timeToWire(snapshot.StartedAt), UpdatedAt: timeToWire(snapshot.UpdatedAt), EndedAt: timeToWire(snapshot.EndedAt),
	}
}

func sessionFromWire(value *realtimev1.SessionSnapshot) (gameruntime.Session, error) {
	if value == nil || value.GetVersion() == nil || value.GetAuthoritativeState() == nil {
		return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(value.GetSessionId())
	if err != nil {
		return gameruntime.Session{}, err
	}
	roomID, err := requiredUUID(value.GetRoomId())
	if err != nil {
		return gameruntime.Session{}, err
	}
	version, err := versionFromWire(value.GetGameId(), value.GetVersion())
	if err != nil {
		return gameruntime.Session{}, err
	}
	stateVersion, stateMessage, err := envelopeFromWire(value.GetAuthoritativeState())
	if err != nil || stateVersion != version {
		return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
	}
	participants := make([]gameruntime.Participant, 0, len(value.GetParticipants()))
	for _, participant := range value.GetParticipants() {
		if participant == nil {
			return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
		}
		userID, parseErr := requiredUUID(participant.GetUserId())
		if parseErr != nil {
			return gameruntime.Session{}, parseErr
		}
		participants = append(participants, gameruntime.Participant{UserID: userID, SeatIndex: participant.GetSeatIndex()})
	}
	timers := make([]gameruntime.TimerSnapshot, 0, len(value.GetTimers()))
	for _, timer := range value.GetTimers() {
		if timer == nil {
			return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
		}
		timerID, parseErr := game.ParseIdentifier(timer.GetTimerId())
		if parseErr != nil {
			return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
		}
		dueAt, parseErr := requiredTime(timer.GetDueAt())
		if parseErr != nil {
			return gameruntime.Session{}, parseErr
		}
		timerVersion, message, parseErr := envelopeFromWire(timer.GetMessage())
		if parseErr != nil || timerVersion != version {
			return gameruntime.Session{}, gameruntime.ErrInvalidSessionInput
		}
		timers = append(timers, gameruntime.TimerSnapshot{
			TimerID: timerID, ExpectedStateVersion: timer.GetExpectedStateVersion(), DueAt: dueAt, Message: message,
		})
	}
	startedAt, err := requiredTime(value.GetStartedAt())
	if err != nil {
		return gameruntime.Session{}, err
	}
	updatedAt, err := requiredTime(value.GetUpdatedAt())
	if err != nil {
		return gameruntime.Session{}, err
	}
	nextDeadlineAt, err := optionalTime(value.GetNextDeadlineAt())
	if err != nil {
		return gameruntime.Session{}, err
	}
	endedAt, err := optionalTime(value.GetEndedAt())
	if err != nil {
		return gameruntime.Session{}, err
	}
	status, err := statusFromWire(value.GetStatus())
	if err != nil {
		return gameruntime.Session{}, err
	}
	return gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: roomID, VersionKey: version, OwnershipEpoch: value.GetOwnershipEpoch(),
		Participants: participants,
		State: game.Snapshot{
			SnapshotVersion: value.GetSnapshotVersion(), StateVersion: value.GetStateVersion(), State: stateMessage,
		},
		Timers: timers, NextDeadlineAt: nextDeadlineAt, Status: status,
		StartedAt: startedAt, UpdatedAt: updatedAt, EndedAt: endedAt,
	})
}

func actionReceiptToWire(receipt gameruntime.ActionReceipt) *realtimev1.ActionReceipt {
	snapshot := receipt.Snapshot()
	return &realtimev1.ActionReceipt{
		SessionId: snapshot.Key.SessionID.String(), ActorUserId: snapshot.Key.ActorUserID.String(),
		ActionId: snapshot.Key.ActionID.Value(), RequestDigest: snapshot.RequestDigest.Bytes(),
		ResultCode: string(snapshot.ResultCode), ResultDigest: snapshot.ResultDigest.Bytes(),
		StateVersion: snapshot.StateVersion, CommittedAt: timeToWire(snapshot.CommittedAt),
	}
}

func actionReceiptFromWire(value *realtimev1.ActionReceipt) (gameruntime.ActionReceipt, error) {
	if value == nil {
		return gameruntime.ActionReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	sessionID, actorUserID, operationID, requestDigest, resultDigest, committedAt, err := receiptFields(
		value.GetSessionId(), value.GetActorUserId(), value.GetActionId(), value.GetRequestDigest(), value.GetResultDigest(), value.GetCommittedAt(),
	)
	if err != nil {
		return gameruntime.ActionReceipt{}, err
	}
	return gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: sessionID, ActorUserID: actorUserID, ActionID: operationID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCode(value.GetResultCode()), ResultDigest: resultDigest,
		StateVersion: value.GetStateVersion(), CommittedAt: committedAt,
	})
}

func timerReceiptToWire(receipt gameruntime.TimerReceipt) *realtimev1.TimerReceipt {
	snapshot := receipt.Snapshot()
	return &realtimev1.TimerReceipt{
		SessionId: snapshot.Key.SessionID.String(), TimerId: string(snapshot.Key.TimerID),
		ExpectedStateVersion: snapshot.Key.ExpectedStateVersion, ResultCode: string(snapshot.ResultCode),
		ResultDigest: snapshot.ResultDigest.Bytes(), StateVersion: snapshot.StateVersion,
		CommittedAt: timeToWire(snapshot.CommittedAt),
	}
}

func timerReceiptFromWire(value *realtimev1.TimerReceipt) (gameruntime.TimerReceipt, error) {
	if value == nil {
		return gameruntime.TimerReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(value.GetSessionId())
	if err != nil {
		return gameruntime.TimerReceipt{}, err
	}
	timerID, err := game.ParseIdentifier(value.GetTimerId())
	if err != nil {
		return gameruntime.TimerReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	resultDigest, err := idempotency.NewDigest(value.GetResultDigest())
	if err != nil {
		return gameruntime.TimerReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	committedAt, err := requiredTime(value.GetCommittedAt())
	if err != nil {
		return gameruntime.TimerReceipt{}, err
	}
	return gameruntime.NewTimerReceipt(gameruntime.TimerReceiptSnapshot{
		Key: gameruntime.TimerKey{
			SessionID: sessionID, TimerID: timerID, ExpectedStateVersion: value.GetExpectedStateVersion(),
		},
		ResultCode: gameruntime.ResultCode(value.GetResultCode()), ResultDigest: resultDigest,
		StateVersion: value.GetStateVersion(), CommittedAt: committedAt,
	})
}

func systemReceiptToWire(receipt gameruntime.SystemReceipt) *realtimev1.SystemReceipt {
	snapshot := receipt.Snapshot()
	return &realtimev1.SystemReceipt{
		SessionId: snapshot.Key.SessionID.String(), OperationId: snapshot.Key.OperationID.Value(),
		SourceKind: string(snapshot.Key.Source.Kind), SourceEventId: snapshot.Key.Source.EventID.String(),
		RequestedByUserId: optionalUUID(snapshot.Key.Source.RequestedByUserID), RequestDigest: snapshot.RequestDigest.Bytes(),
		ResultCode: string(snapshot.ResultCode), ResultDigest: snapshot.ResultDigest.Bytes(),
		StateVersion: snapshot.StateVersion, CommittedAt: timeToWire(snapshot.CommittedAt),
	}
}

func systemReceiptFromWire(value *realtimev1.SystemReceipt) (gameruntime.SystemReceipt, error) {
	if value == nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	sessionID, err := requiredUUID(value.GetSessionId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	operationID, err := idempotency.ParseOperationID(value.GetOperationId())
	if err != nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	sourceEventID, err := requiredUUID(value.GetSourceEventId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	requestedByUserID, err := optionalUUIDFromWire(value.GetRequestedByUserId())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	requestDigest, err := idempotency.NewDigest(value.GetRequestDigest())
	if err != nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	resultDigest, err := idempotency.NewDigest(value.GetResultDigest())
	if err != nil {
		return gameruntime.SystemReceipt{}, gameruntime.ErrInvalidSessionInput
	}
	committedAt, err := requiredTime(value.GetCommittedAt())
	if err != nil {
		return gameruntime.SystemReceipt{}, err
	}
	return gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key: gameruntime.SystemKey{
			SessionID: sessionID, OperationID: operationID,
			Source: gameruntime.SystemSource{
				Kind: game.Identifier(value.GetSourceKind()), EventID: sourceEventID, RequestedByUserID: requestedByUserID,
			},
		},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCode(value.GetResultCode()), ResultDigest: resultDigest,
		StateVersion: value.GetStateVersion(), CommittedAt: committedAt,
	})
}

func projectionToWire(session gameruntime.Session, viewer game.Viewer, projection game.Projection) *gamev1.GameProjection {
	actions := make([]string, 0, len(projection.AllowedActions))
	for _, action := range projection.AllowedActions {
		actions = append(actions, string(action))
	}
	return &gamev1.GameProjection{
		SessionId: session.Snapshot().ID.String(), StateVersion: session.Snapshot().State.StateVersion,
		ViewerKind: viewerKindToWire(viewer.Kind), View: envelopeToWire(session.Snapshot().VersionKey, projection.View),
		AllowedActions: actions,
	}
}

func projectionFromWire(value *gamev1.GameProjection) (game.Projection, error) {
	if value == nil {
		return game.Projection{}, gameruntime.ErrInvalidSessionInput
	}
	_, view, err := envelopeFromWire(value.GetView())
	if err != nil {
		return game.Projection{}, err
	}
	actions := make([]game.Identifier, 0, len(value.GetAllowedActions()))
	for _, raw := range value.GetAllowedActions() {
		action, parseErr := game.ParseIdentifier(raw)
		if parseErr != nil {
			return game.Projection{}, gameruntime.ErrInvalidSessionInput
		}
		actions = append(actions, action)
	}
	projection := game.Projection{View: view, AllowedActions: actions}
	if !projection.Valid() {
		return game.Projection{}, gameruntime.ErrProjectionUnsafe
	}
	return projection, nil
}

func envelopeToWire(version game.VersionKey, message game.Message) *gamev1.GameEnvelope {
	return &gamev1.GameEnvelope{
		GameId: string(version.GameID), Version: versionToWire(version), SchemaVersion: message.SchemaVersion,
		MessageType: string(message.MessageType), Payload: append([]byte(nil), message.Payload...),
	}
}

func envelopeFromWire(value *gamev1.GameEnvelope) (game.VersionKey, game.Message, error) {
	if value == nil || value.GetVersion() == nil {
		return game.VersionKey{}, game.Message{}, gameruntime.ErrInvalidSessionInput
	}
	version, err := versionFromWire(value.GetGameId(), value.GetVersion())
	if err != nil {
		return game.VersionKey{}, game.Message{}, err
	}
	messageType, err := game.ParseIdentifier(value.GetMessageType())
	if err != nil {
		return game.VersionKey{}, game.Message{}, gameruntime.ErrInvalidSessionInput
	}
	message := game.Message{
		MessageType: messageType, SchemaVersion: value.GetSchemaVersion(), Payload: append([]byte(nil), value.GetPayload()...),
	}
	if !message.Valid() {
		return game.VersionKey{}, game.Message{}, gameruntime.ErrInvalidSessionInput
	}
	return version, message, nil
}

func versionToWire(version game.VersionKey) *gamev1.VersionTuple {
	return &gamev1.VersionTuple{
		Engine: string(version.Engine), Protocol: string(version.Protocol), Client: string(version.Client),
	}
}

func versionFromWire(gameID string, value *gamev1.VersionTuple) (game.VersionKey, error) {
	parsedGameID, err := game.ParseGameID(gameID)
	if err != nil || value == nil {
		return game.VersionKey{}, gameruntime.ErrInvalidSessionInput
	}
	version := game.VersionKey{
		GameID: parsedGameID, Engine: game.Version(value.GetEngine()),
		Protocol: game.Version(value.GetProtocol()), Client: game.Version(value.GetClient()),
	}
	if !version.Valid() {
		return game.VersionKey{}, gameruntime.ErrInvalidSessionInput
	}
	return version, nil
}

func viewerToWire(viewer game.Viewer) *realtimev1.Viewer {
	return &realtimev1.Viewer{
		Kind: viewerKindToWire(viewer.Kind), UserId: string(viewer.UserID), SeatIndex: viewer.SeatIndex,
	}
}

func viewerFromWire(value *realtimev1.Viewer) (game.Viewer, error) {
	if value == nil {
		return game.Viewer{}, gameruntime.ErrInvalidSessionInput
	}
	kind, err := viewerKindFromWire(value.GetKind())
	if err != nil {
		return game.Viewer{}, err
	}
	userID, err := game.ParseIdentifier(value.GetUserId())
	if err != nil {
		return game.Viewer{}, gameruntime.ErrInvalidSessionInput
	}
	viewer := game.Viewer{Kind: kind, UserID: userID, SeatIndex: value.GetSeatIndex()}
	if !viewer.Valid() {
		return game.Viewer{}, gameruntime.ErrInvalidSessionInput
	}
	return viewer, nil
}

func viewerKindToWire(kind game.ViewerKind) gamev1.ViewerKind {
	switch kind {
	case game.ViewerPlayer:
		return gamev1.ViewerKind_VIEWER_KIND_PLAYER
	case game.ViewerSpectator:
		return gamev1.ViewerKind_VIEWER_KIND_SPECTATOR
	case game.ViewerReplay:
		return gamev1.ViewerKind_VIEWER_KIND_REPLAY
	default:
		return gamev1.ViewerKind_VIEWER_KIND_UNSPECIFIED
	}
}

func viewerKindFromWire(kind gamev1.ViewerKind) (game.ViewerKind, error) {
	switch kind {
	case gamev1.ViewerKind_VIEWER_KIND_PLAYER:
		return game.ViewerPlayer, nil
	case gamev1.ViewerKind_VIEWER_KIND_SPECTATOR:
		return game.ViewerSpectator, nil
	case gamev1.ViewerKind_VIEWER_KIND_REPLAY:
		return game.ViewerReplay, nil
	default:
		return "", gameruntime.ErrInvalidSessionInput
	}
}

func statusToWire(status gameruntime.Status) gamev1.GameSessionStatus {
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

func statusFromWire(status gamev1.GameSessionStatus) (gameruntime.Status, error) {
	switch status {
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE:
		return gameruntime.StatusActive, nil
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED:
		return gameruntime.StatusSuspended, nil
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_FINISHED:
		return gameruntime.StatusFinished, nil
	case gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED:
		return gameruntime.StatusCancelled, nil
	default:
		return "", gameruntime.ErrInvalidSessionInput
	}
}

func receiptFields(
	sessionRaw, actorRaw, operationRaw string,
	requestRaw, resultRaw []byte,
	committedRaw *timestamppb.Timestamp,
) (uuid.UUID, uuid.UUID, idempotency.OperationID, idempotency.Digest, idempotency.Digest, time.Time, error) {
	sessionID, err := requiredUUID(sessionRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, time.Time{}, err
	}
	actorUserID, err := requiredUUID(actorRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, time.Time{}, err
	}
	operationID, err := idempotency.ParseOperationID(operationRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, time.Time{}, gameruntime.ErrInvalidSessionInput
	}
	requestDigest, err := idempotency.NewDigest(requestRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, time.Time{}, gameruntime.ErrInvalidSessionInput
	}
	resultDigest, err := idempotency.NewDigest(resultRaw)
	if err != nil {
		return uuid.Nil, uuid.Nil, idempotency.OperationID{}, idempotency.Digest{}, idempotency.Digest{}, time.Time{}, gameruntime.ErrInvalidSessionInput
	}
	committedAt, err := requiredTime(committedRaw)
	return sessionID, actorUserID, operationID, requestDigest, resultDigest, committedAt, err
}

func requiredUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, gameruntime.ErrInvalidSessionInput
	}
	return parsed, nil
}

func optionalUUID(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func optionalUUIDFromWire(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	return requiredUUID(value)
}

func timeToWire(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value.Round(0).UTC())
}

func requiredTime(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil || !value.IsValid() {
		return time.Time{}, gameruntime.ErrInvalidSessionInput
	}
	result := value.AsTime().Round(0).UTC()
	if result.IsZero() {
		return time.Time{}, gameruntime.ErrInvalidSessionInput
	}
	return result, nil
}

func optionalTime(value *timestamppb.Timestamp) (time.Time, error) {
	if value == nil {
		return time.Time{}, nil
	}
	return requiredTime(value)
}
