package room

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const pendingStartDuration = 3 * time.Second

// SelectRoomGame synchronizes the host's pregame table for every room member.
func (service *Service) SelectRoomGame(ctx context.Context, request *connect.Request[roomv1.SelectRoomGameRequest]) (*connect.Response[roomv1.SelectRoomGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.Msg.GetGameId()))
	if err != nil {
		return nil, roomDomain.ErrGameUnavailable
	}
	if _, err := service.catalog.ParticipantLimits(ctx, string(gameID)); err != nil {
		return nil, err
	}
	current, err := service.authorizeRuleHost(ctx, actor, roomID, versionDomain(request.Msg.GetExpectedVersion()), request.Msg.GetOwnershipEpoch())
	if err != nil {
		return nil, err
	}
	operationID, _, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.SelectGame(ctx, roomDomain.SelectGameCommand{
		ActorUserID: actor, RoomID: roomID, GameID: string(gameID), Expected: current.Version(),
	})
	if err != nil {
		return nil, err
	}
	if updated.Version() != current.Version() {
		_ = operationID
		service.cancelPendingForRoom(ctx, roomID, updated.Snapshot().OwnershipEpoch)
	}
	wire, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.SelectRoomGameResponse{Room: wire}), nil
}

// UpdateGameConfig validates and persists one complete game-owned envelope.
func (service *Service) UpdateGameConfig(ctx context.Context, request *connect.Request[roomv1.UpdateGameConfigRequest]) (*connect.Response[roomv1.UpdateGameConfigResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.Msg.GetGameId()))
	if err != nil {
		return nil, roomDomain.ErrGameUnavailable
	}
	current, err := service.authorizeRuleHost(ctx, actor, roomID, versionDomain(request.Msg.GetExpectedVersion()), request.Msg.GetOwnershipEpoch())
	if err != nil {
		return nil, err
	}
	if current.Snapshot().SelectedGameID != string(gameID) {
		return nil, roomDomain.ErrGameSelectionConflict
	}
	if current.Snapshot().Status != roomDomain.RoomStatusLobby && current.Snapshot().Status != roomDomain.RoomStatusPostGame {
		return nil, roomDomain.ErrRoomStatus
	}
	operationID, digest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	input, err := configEnvelopeFromWire(request.Msg.GetConfig())
	if err != nil || input.GameID != string(gameID) {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	canonical, err := service.ruleCatalog.Normalize(ctx, input, participantCount(current.Snapshot()))
	if err != nil {
		return nil, err
	}
	draft, err := service.ruleRepo.UpdateDraft(ctx, roomDomain.RuleDraftUpdate{
		RoomID: roomID, ActorUserID: actor, GameID: string(gameID), Config: canonical,
		ExpectedRevision: request.Msg.GetExpectedRevision(), Expected: current.Version(), OwnershipEpoch: current.Snapshot().OwnershipEpoch,
		OperationID: operationID.Value(), RequestDigest: [32]byte(digest), At: service.ruleNow(),
	})
	if err != nil {
		return nil, err
	}
	service.cancelPendingForRoom(ctx, roomID, current.Snapshot().OwnershipEpoch)
	wire, err := service.roomWire(ctx, current)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.UpdateGameConfigResponse{Room: wire, Draft: ruleDraftWire(draft)}), nil
}

// ListGameRulePresets returns only the authenticated user's personal presets.
func (service *Service) ListGameRulePresets(ctx context.Context, request *connect.Request[roomv1.ListGameRulePresetsRequest]) (*connect.Response[roomv1.ListGameRulePresetsResponse], error) {
	actor, err := service.authenticate(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	gameID := strings.TrimSpace(request.Msg.GetGameId())
	if gameID != "" {
		if _, err := gameSDK.ParseGameID(gameID); err != nil {
			return nil, roomDomain.ErrGameUnavailable
		}
	}
	presets, err := service.ruleRepo.ListPresets(ctx, actor, gameID)
	if err != nil {
		return nil, err
	}
	values := make([]*roomv1.GameRulePreset, 0, len(presets))
	for _, preset := range presets {
		values = append(values, rulePresetWire(preset))
	}
	return connect.NewResponse(&roomv1.ListGameRulePresetsResponse{Presets: values}), nil
}

// SaveGameRulePreset creates, overwrites, or copies one personal preset.
func (service *Service) SaveGameRulePreset(ctx context.Context, request *connect.Request[roomv1.SaveGameRulePresetRequest]) (*connect.Response[roomv1.SaveGameRulePresetResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	operationID, digest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.Msg.GetGameId()))
	if err != nil {
		return nil, roomDomain.ErrGameUnavailable
	}
	input, err := configEnvelopeFromWire(request.Msg.GetConfig())
	if err != nil || input.GameID != string(gameID) {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	canonical, err := service.ruleCatalog.Normalize(ctx, input, 0)
	if err != nil {
		return nil, err
	}
	mode := request.Msg.GetMode()
	if mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_UNSPECIFIED {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	var presetID uuid.UUID
	if request.Msg.GetPresetId() != "" {
		presetID, err = parseUUID(request.Msg.GetPresetId())
		if err != nil {
			return nil, err
		}
	}
	if mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_CREATE && presetID != uuid.Nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	if mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_CREATE && request.Msg.GetExpectedPresetRevision() != 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	if mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_OVERWRITE && (presetID == uuid.Nil || request.Msg.GetExpectedPresetRevision() == 0) {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	if mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_COPY && request.Msg.GetExpectedPresetRevision() != 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	preset, err := service.ruleRepo.SavePreset(ctx, roomDomain.RulePresetWrite{
		PresetID: presetID, OwnerUserID: actor, GameID: string(gameID), Name: request.Msg.GetName(), Config: canonical,
		ExpectedRevision: request.Msg.GetExpectedPresetRevision(), OperationID: operationID.Value(), RequestDigest: [32]byte(digest),
		At: service.ruleNow(), Copy: mode == roomv1.GameRulePresetWriteMode_GAME_RULE_PRESET_WRITE_MODE_COPY,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.SaveGameRulePresetResponse{Preset: rulePresetWire(preset)}), nil
}

// DeleteGameRulePreset removes one owner-scoped preset under revision fencing.
func (service *Service) DeleteGameRulePreset(ctx context.Context, request *connect.Request[roomv1.DeleteGameRulePresetRequest]) (*connect.Response[roomv1.DeleteGameRulePresetResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	presetID, err := parseUUID(request.Msg.GetPresetId())
	if err != nil {
		return nil, err
	}
	operationID, digest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	if err := service.ruleRepo.DeletePreset(ctx, actor, presetID, request.Msg.GetExpectedPresetRevision(), operationID.Value(), [32]byte(digest)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.DeleteGameRulePresetResponse{PresetId: presetID.String()}), nil
}

// BeginGameStart creates the server-authoritative three-second countdown.
func (service *Service) BeginGameStart(ctx context.Context, request *connect.Request[roomv1.BeginGameStartRequest]) (*connect.Response[roomv1.BeginGameStartResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, err := parseUUID(request.Msg.GetRoomId())
	if err != nil {
		return nil, err
	}
	gameID, err := gameSDK.ParseGameID(strings.TrimSpace(request.Msg.GetGameId()))
	if err != nil {
		return nil, roomDomain.ErrGameUnavailable
	}
	current, err := service.authorizeRuleHost(ctx, actor, roomID, versionDomain(request.Msg.GetExpectedVersion()), request.Msg.GetOwnershipEpoch())
	if err != nil {
		return nil, err
	}
	if current.Snapshot().SelectedGameID != string(gameID) {
		return nil, roomDomain.ErrGameSelectionConflict
	}
	if current.Snapshot().Status != roomDomain.RoomStatusLobby && current.Snapshot().Status != roomDomain.RoomStatusPostGame {
		return nil, roomDomain.ErrRoomStatus
	}
	operationID, digest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	participants := participantCount(current.Snapshot())
	draft, draftErr := service.ruleRepo.GetDraft(ctx, roomID, string(gameID))
	if errors.Is(draftErr, roomDomain.ErrRuleNotFound) {
		canonical, defaultErr := service.ruleCatalog.Default(ctx, string(gameID), participants)
		if defaultErr != nil {
			return nil, defaultErr
		}
		draft, draftErr = service.ruleRepo.UpdateDraft(ctx, roomDomain.RuleDraftUpdate{
			RoomID: roomID, ActorUserID: actor, GameID: string(gameID), Config: canonical,
			ExpectedRevision: 0, Expected: current.Version(), OwnershipEpoch: current.Snapshot().OwnershipEpoch,
			OperationID: operationID.Value(), RequestDigest: [32]byte(digest), At: service.ruleNow(),
		})
	}
	if draftErr != nil {
		return nil, draftErr
	}
	if request.Msg.GetConfigRevision() != 0 && request.Msg.GetConfigRevision() != draft.Revision {
		return nil, roomDomain.ErrRuleRevisionConflict
	}
	now := service.ruleNow()
	deadline := now.Add(pendingStartDuration)
	pending, err := service.ruleRepo.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
		RoomID: roomID, ActorUserID: actor, GameID: string(gameID), ConfigRevision: draft.Revision,
		Expected: current.Version(), OwnershipEpoch: current.Snapshot().OwnershipEpoch,
		OperationID: operationID.Value(), RequestDigest: [32]byte(digest), Deadline: deadline, At: now,
	})
	if err != nil {
		return nil, err
	}
	wire, err := service.roomWire(ctx, current)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.BeginGameStartResponse{Room: wire, PendingStart: pendingStartWire(pending)}), nil
}

// CancelGameStart invalidates a pending countdown using its opaque token.
func (service *Service) CancelGameStart(ctx context.Context, request *connect.Request[roomv1.CancelGameStartRequest]) (*connect.Response[roomv1.CancelGameStartResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, pendingID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetPendingStartId())
	if err != nil {
		return nil, err
	}
	current, err := service.authorizeRuleHost(ctx, actor, roomID, versionDomain(request.Msg.GetExpectedVersion()), request.Msg.GetOwnershipEpoch())
	if err != nil {
		return nil, err
	}
	operationID, digest, err := operationBinding(request.Msg.GetOperationId(), request.Msg.GetRequestDigest())
	if err != nil {
		return nil, err
	}
	if err := service.ruleRepo.CancelPendingStart(ctx, roomID, pendingID, request.Msg.GetCancelToken(), current.Snapshot().OwnershipEpoch, [32]byte(digest), service.ruleNow()); err != nil {
		return nil, err
	}
	_ = operationID
	wire, err := service.roomWire(ctx, current)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.CancelGameStartResponse{Room: wire}), nil
}

func (service *Service) authorizeRuleHost(ctx context.Context, actor, roomID uuid.UUID, expected roomDomain.Version, ownershipEpoch uint64) (roomDomain.Room, error) {
	room, err := service.rooms.GetByID(ctx, roomID)
	if err != nil {
		return roomDomain.Room{}, err
	}
	snapshot := room.Snapshot()
	if snapshot.HostUserID != actor {
		return roomDomain.Room{}, roomDomain.ErrHostRequired
	}
	if ownershipEpoch == 0 || snapshot.OwnershipEpoch != ownershipEpoch {
		return roomDomain.Room{}, roomDomain.ErrRulePermission
	}
	if room.Version() != expected {
		return roomDomain.Room{}, roomDomain.ErrRoomVersionConflict
	}
	return room, nil
}

func (service *Service) cancelPendingForRoom(ctx context.Context, roomID uuid.UUID, ownershipEpoch uint64) {
	pending, err := service.ruleRepo.GetPendingStart(ctx, roomID)
	if err != nil || pending.Cancelled || pending.Consumed || !pending.Deadline.After(service.ruleNow()) {
		return
	}
	_ = service.ruleRepo.CancelPendingStart(ctx, roomID, pending.ID, pending.CancelToken, ownershipEpoch, [32]byte{}, service.ruleNow())
}

func (service *Service) ruleNow() time.Time {
	if service == nil || service.ruleClock == nil {
		return time.Now().Round(0).UTC()
	}
	return service.ruleClock.Now().Round(0).UTC()
}

func participantCount(snapshot roomDomain.RoomSnapshot) uint32 {
	var count uint32
	for _, member := range snapshot.Members {
		if member.Role == roomDomain.MemberRoleParticipant {
			count++
		}
	}
	return count
}

func configEnvelopeFromWire(value *gamev1.GameEnvelope) (roomDomain.ConfigEnvelope, error) {
	key, message, err := gameEnvelope(value)
	if err != nil {
		return roomDomain.ConfigEnvelope{}, err
	}
	return roomDomain.ConfigEnvelope{
		GameID: string(key.GameID), EngineVersion: string(key.Engine), ProtocolVersion: string(key.Protocol), ClientVersion: string(key.Client),
		SchemaVersion: message.SchemaVersion, MessageType: string(message.MessageType), Payload: append([]byte(nil), message.Payload...),
	}, nil
}

func configEnvelopeFromGameConfig(value *gamev1.GameConfig, expectedGameID gameSDK.GameID) (roomDomain.ConfigEnvelope, error) {
	message, err := gameConfig(value, expectedGameID)
	if err != nil || value.GetVersion() == nil {
		return roomDomain.ConfigEnvelope{}, gameruntime.ErrInvalidSessionInput
	}
	engine, engineErr := gameSDK.ParseVersion(value.GetVersion().GetEngine())
	protocol, protocolErr := gameSDK.ParseVersion(value.GetVersion().GetProtocol())
	client, clientErr := gameSDK.ParseVersion(value.GetVersion().GetClient())
	if engineErr != nil || protocolErr != nil || clientErr != nil {
		return roomDomain.ConfigEnvelope{}, gameruntime.ErrInvalidSessionInput
	}
	result := roomDomain.ConfigEnvelope{
		GameID: string(expectedGameID), EngineVersion: string(engine), ProtocolVersion: string(protocol), ClientVersion: string(client),
		SchemaVersion: message.SchemaVersion, MessageType: string(message.MessageType), Payload: append([]byte(nil), message.Payload...),
	}
	if !result.Valid() {
		return roomDomain.ConfigEnvelope{}, gameruntime.ErrInvalidSessionInput
	}
	return result, nil
}

func legacyConfigEnvelope(value *gamev1.GameConfig, gameID gameSDK.GameID) roomDomain.ConfigEnvelope {
	result, err := configEnvelopeFromGameConfig(value, gameID)
	if err != nil {
		return roomDomain.ConfigEnvelope{}
	}
	return result
}

func configEnvelopeToWire(value roomDomain.ConfigEnvelope) *gamev1.GameEnvelope {
	if !value.Valid() {
		return nil
	}
	return &gamev1.GameEnvelope{GameId: value.GameID, Version: &gamev1.VersionTuple{Engine: value.EngineVersion, Protocol: value.ProtocolVersion, Client: value.ClientVersion}, SchemaVersion: value.SchemaVersion, MessageType: value.MessageType, Payload: append([]byte(nil), value.Payload...)}
}

func ruleDraftWire(value roomDomain.RuleDraft) *roomv1.RoomGameConfigDraft {
	return &roomv1.RoomGameConfigDraft{GameId: value.GameID, Config: configEnvelopeToWire(value.Config), Revision: value.Revision, UpdatedBy: value.UpdatedBy.String(), UpdatedAt: timestamppb.New(value.UpdatedAt)}
}

func pendingStartWire(value roomDomain.PendingStart) *roomv1.PendingGameStart {
	return &roomv1.PendingGameStart{PendingStartId: value.ID.String(), CancelToken: value.CancelToken, Deadline: timestamppb.New(value.Deadline), GameId: value.GameID, ConfigRevision: value.ConfigRevision, ExpectedVersion: &roomv1.RoomVersion{RoomVersion: value.Expected.Room, MembershipVersion: value.Expected.Membership}, OwnershipEpoch: value.OwnershipEpoch}
}

func rulePresetWire(value roomDomain.RulePreset) *roomv1.GameRulePreset {
	return &roomv1.GameRulePreset{PresetId: value.ID.String(), GameId: value.GameID, Name: value.Name, Config: configEnvelopeToWire(value.Config), PresetRevision: value.Revision, CreatedAt: timestamppb.New(value.CreatedAt), UpdatedAt: timestamppb.New(value.UpdatedAt), LastUsedAt: timestamppb.New(value.LastUsedAt), Compatible: value.Compatible}
}
