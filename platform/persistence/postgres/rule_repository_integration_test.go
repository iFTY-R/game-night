package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
)

func TestRuleRepositoryPersistsDraftsPresetsPendingStartsAndRoomSelection(t *testing.T) {
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), roomRepositoryIntegrationTimeout)
	defer cancel()
	applyTransactionTestMigrations(t, ctx, fixture)

	now := databaseIntegrationTime(t, ctx, fixture)
	hostID := uuid.New()
	createRoomTestUser(t, ctx, fixture, hostID, "RuleHost1", now)

	roomRepository := NewRoomRepository(fixture.Pool)
	created, err := roomDomain.New(uuid.New(), hostID, "RULE01", roomDomain.VisibilityPrivate, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := roomRepository.Create(ctx, created)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := stored.SelectGame(hostID, "dice", stored.Version(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	selected, err = roomRepository.UpdateCAS(ctx, stored, selected)
	if err != nil {
		t.Fatal(err)
	}
	loadedRoom, err := roomRepository.GetByID(ctx, selected.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedRoom.Snapshot().SelectedGameID != "dice" || loadedRoom.Snapshot().OwnershipEpoch != 1 {
		t.Fatalf("room rule fields were not preserved: %+v", loadedRoom.Snapshot())
	}

	repository := NewRuleRepository(fixture.Pool)
	config := testRuleConfig("dice", []byte(`{"rounds":3}`))
	draftDigest := config.Digest()
	draft, err := repository.UpdateDraft(ctx, roomDomain.RuleDraftUpdate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", Config: config,
		ExpectedRevision: 0, Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch,
		OperationID: "draft-op-1", RequestDigest: draftDigest, At: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Revision != 1 || draft.GameID != "dice" || !reflect.DeepEqual(draft.Config.Payload, config.Payload) {
		t.Fatalf("stored draft = %+v", draft)
	}
	replayedDraft, err := repository.UpdateDraft(ctx, roomDomain.RuleDraftUpdate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", Config: config,
		ExpectedRevision: 0, Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch,
		OperationID: "draft-op-1", RequestDigest: draftDigest, At: now.Add(3 * time.Second),
	})
	if err != nil || !reflect.DeepEqual(replayedDraft, draft) {
		t.Fatalf("draft replay = %+v err=%v", replayedDraft, err)
	}
	conflictConfig := testRuleConfig("dice", []byte(`{"rounds":4}`))
	if _, err := repository.UpdateDraft(ctx, roomDomain.RuleDraftUpdate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", Config: conflictConfig,
		ExpectedRevision: 1, Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch,
		OperationID: "draft-op-1", RequestDigest: conflictConfig.Digest(), At: now.Add(4 * time.Second),
	}); !errors.Is(err, roomDomain.ErrRuleOperationConflict) {
		t.Fatalf("draft operation conflict error = %v", err)
	}
	loadedDraft, err := repository.GetDraft(ctx, selected.Snapshot().ID, "dice")
	if err != nil || !reflect.DeepEqual(loadedDraft, draft) {
		t.Fatalf("loaded draft = %+v err=%v", loadedDraft, err)
	}
	drafts, err := repository.ListDrafts(ctx, selected.Snapshot().ID)
	if err != nil || len(drafts) != 1 || !reflect.DeepEqual(drafts[0], draft) {
		t.Fatalf("draft list = %+v err=%v", drafts, err)
	}

	presetDigest := config.Digest()
	presetID := uuid.New()
	preset, err := repository.SavePreset(ctx, roomDomain.RulePresetWrite{
		PresetID: presetID, OwnerUserID: hostID, GameID: "dice", Name: "Fast dice",
		Config: config, ExpectedRevision: 0, OperationID: "preset-save-1",
		RequestDigest: presetDigest, At: now.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedPreset, err := repository.SavePreset(ctx, roomDomain.RulePresetWrite{
		PresetID: presetID, OwnerUserID: hostID, GameID: "dice", Name: "Fast dice",
		Config: config, ExpectedRevision: 0, OperationID: "preset-save-1",
		RequestDigest: presetDigest, At: now.Add(6 * time.Second),
	})
	if err != nil || !reflect.DeepEqual(replayedPreset, preset) {
		t.Fatalf("preset replay = %+v err=%v", replayedPreset, err)
	}
	presets, err := repository.ListPresets(ctx, hostID, "dice")
	if err != nil || len(presets) != 1 || presets[0].ID != presetID {
		t.Fatalf("preset list = %+v err=%v", presets, err)
	}
	if err := repository.DeletePreset(ctx, hostID, presetID, preset.Revision, "preset-delete-1", presetDigest); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeletePreset(ctx, hostID, presetID, preset.Revision, "preset-delete-1", presetDigest); err != nil {
		t.Fatalf("delete replay error = %v", err)
	}
	presets, err = repository.ListPresets(ctx, hostID, "dice")
	if err != nil || len(presets) != 0 {
		t.Fatalf("presets after delete = %+v err=%v", presets, err)
	}

	startDigest := draftDigest
	start, err := repository.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", ConfigRevision: draft.Revision,
		Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch, OperationID: "start-op-1",
		RequestDigest: startDigest, Deadline: now.Add(10 * time.Second), At: now.Add(7 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedStart, err := repository.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", ConfigRevision: draft.Revision,
		Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch, OperationID: "start-op-1",
		RequestDigest: startDigest, Deadline: now.Add(10 * time.Second), At: now.Add(8 * time.Second),
	})
	if err != nil || replayedStart.ID != start.ID || replayedStart.CancelToken != start.CancelToken {
		t.Fatalf("start replay = %+v err=%v", replayedStart, err)
	}
	if _, err := repository.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", ConfigRevision: draft.Revision,
		Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch, OperationID: "start-op-2",
		RequestDigest: startDigest, Deadline: now.Add(11 * time.Second), At: now.Add(8 * time.Second),
	}); !errors.Is(err, roomDomain.ErrPendingStartInvalid) {
		t.Fatalf("parallel active start error = %v", err)
	}
	if err := repository.CancelPendingStart(ctx, selected.Snapshot().ID, start.ID, start.CancelToken, start.OwnershipEpoch, start.RequestDigest, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repository.CancelPendingStart(ctx, selected.Snapshot().ID, start.ID, start.CancelToken, start.OwnershipEpoch, start.RequestDigest, now.Add(9*time.Second)); err != nil {
		t.Fatalf("cancel replay error = %v", err)
	}
	secondStart, err := repository.BeginPendingStart(ctx, roomDomain.PendingStartCreate{
		RoomID: selected.Snapshot().ID, ActorUserID: hostID, GameID: "dice", ConfigRevision: draft.Revision,
		Expected: selected.Version(), OwnershipEpoch: selected.Snapshot().OwnershipEpoch, OperationID: "start-op-3",
		RequestDigest: startDigest, Deadline: now.Add(12 * time.Second), At: now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ConsumePendingStart(ctx, selected.Snapshot().ID, secondStart.ID, secondStart.CancelToken, secondStart.OperationID, secondStart.RequestDigest, now.Add(11*time.Second)); !errors.Is(err, roomDomain.ErrPendingStartInvalid) {
		t.Fatalf("early consume error = %v", err)
	}
	consumed, err := repository.ConsumePendingStart(ctx, selected.Snapshot().ID, secondStart.ID, secondStart.CancelToken, secondStart.OperationID, secondStart.RequestDigest, now.Add(13*time.Second))
	if err != nil || !consumed.Consumed {
		t.Fatalf("consumed start = %+v err=%v", consumed, err)
	}
}

func testRuleConfig(gameID string, payload []byte) roomDomain.ConfigEnvelope {
	return roomDomain.ConfigEnvelope{
		GameID: gameID, EngineVersion: "1.0.0", ProtocolVersion: "1.0.0",
		ClientVersion: "1.0.0", SchemaVersion: 1, MessageType: "dice.config",
		Payload: append([]byte(nil), payload...),
	}
}
