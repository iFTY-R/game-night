package room

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
)

func TestRoomGovernanceConnectFlow(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	fixture := newRoomTransportFixture(t, map[string]uuid.UUID{"host-device": host, "guest-device": guest})
	client := fixture.client

	create := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PRIVATE, ParticipantCapacity: 3,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
	})
	authorizeRoomWrite(create, "host-device")
	created, err := client.CreateRoom(t.Context(), create)
	if err != nil {
		t.Fatal(err)
	}
	join := connect.NewRequest(&roomv1.JoinRoomRequest{
		RoomCode: created.Msg.GetRoom().GetRoomCode(), Intent: roomv1.JoinIntent_JOIN_INTENT_PARTICIPANT,
	})
	authorizeRoomWrite(join, "guest-device")
	joined, err := client.JoinRoom(t.Context(), join)
	if err != nil {
		t.Fatal(err)
	}
	start := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: joined.Msg.GetRoom().GetRoomId(), GameId: "liars-dice", ExpectedVersion: joined.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: roomTransportOperationID(t, 11).Value(), RequestDigest: roomTransportDigest("governance-start"),
	})
	authorizeRoomWrite(start, "host-device")
	started, err := client.StartGame(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}

	requestPause := func(room *roomv1.Room) *roomv1.Room {
		t.Helper()
		request := connect.NewRequest(&roomv1.RequestRoomPauseRequest{
			RoomId: room.GetRoomId(), SessionId: started.Msg.GetSessionId(), ExpectedVersion: room.GetVersion(),
		})
		authorizeRoomWrite(request, "guest-device")
		response, requestErr := client.RequestRoomPause(t.Context(), request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		pending := response.Msg.GetRoom().GetPendingPauseRequest()
		if pending == nil || pending.GetRequestId() == "" || pending.GetRequestedByUserId() != guest.String() ||
			pending.GetSessionId() != started.Msg.GetSessionId() {
			t.Fatalf("pending pause request = %+v", pending)
		}
		return response.Msg.GetRoom()
	}

	requested := requestPause(started.Msg.GetRoom())
	reject := connect.NewRequest(&roomv1.RejectRoomPauseRequestRequest{
		RoomId: requested.GetRoomId(), RequestId: requested.GetPendingPauseRequest().GetRequestId(), ExpectedVersion: requested.GetVersion(),
	})
	authorizeRoomWrite(reject, "host-device")
	rejected, err := client.RejectRoomPauseRequest(t.Context(), reject)
	if err != nil || rejected.Msg.GetRoom().GetPendingPauseRequest() != nil {
		t.Fatalf("reject pause: room=%+v err=%v", rejected.Msg.GetRoom(), err)
	}

	requested = requestPause(rejected.Msg.GetRoom())
	// Fanout is advisory after the cross-aggregate transaction commits; polling must remain a valid repair path.
	fixture.fanout.setError(errors.New("redis unavailable"))
	pause := connect.NewRequest(&roomv1.PauseRoomGameRequest{
		RoomId: requested.GetRoomId(), SessionId: started.Msg.GetSessionId(),
		RequestId: requested.GetPendingPauseRequest().GetRequestId(), ExpectedVersion: requested.GetVersion(),
		OwnershipEpoch: requested.GetOwnershipEpoch(),
	})
	authorizeRoomWrite(pause, "host-device")
	paused, err := client.PauseRoomGame(t.Context(), pause)
	if err != nil || paused.Msg.GetRoom().GetPendingPauseRequest() != nil || paused.Msg.GetRoom().GetActivePause() == nil ||
		paused.Msg.GetRoom().GetActivePause().GetSource() != roomv1.PauseSource_PAUSE_SOURCE_APPROVED_REQUEST ||
		paused.Msg.GetSession().GetStatus() != gamev1.GameSessionStatus_GAME_SESSION_STATUS_SUSPENDED ||
		paused.Msg.GetSession().GetSuspendedAt() == nil {
		t.Fatalf("pause room: response=%+v err=%v", paused, err)
	}

	resume := connect.NewRequest(&roomv1.ResumeRoomGameRequest{
		RoomId: paused.Msg.GetRoom().GetRoomId(), SessionId: started.Msg.GetSessionId(),
		ExpectedVersion: paused.Msg.GetRoom().GetVersion(), OwnershipEpoch: paused.Msg.GetRoom().GetOwnershipEpoch(),
	})
	authorizeRoomWrite(resume, "host-device")
	resumed, err := client.ResumeRoomGame(t.Context(), resume)
	if err != nil || resumed.Msg.GetRoom().GetActivePause() != nil ||
		resumed.Msg.GetSession().GetStatus() != gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE ||
		resumed.Msg.GetSession().GetSuspendedAt() != nil {
		t.Fatalf("resume room: response=%+v err=%v", resumed, err)
	}

	oldEpoch := resumed.Msg.GetRoom().GetOwnershipEpoch()
	transfer := connect.NewRequest(&roomv1.TransferRoomHostRequest{
		RoomId: resumed.Msg.GetRoom().GetRoomId(), TargetUserId: guest.String(),
		ExpectedVersion: resumed.Msg.GetRoom().GetVersion(), OwnershipEpoch: oldEpoch,
	})
	authorizeRoomWrite(transfer, "host-device")
	transferred, err := client.TransferRoomHost(t.Context(), transfer)
	if err != nil || transferred.Msg.GetRoom().GetHostUserId() != guest.String() ||
		transferred.Msg.GetRoom().GetOwnershipEpoch() != oldEpoch+1 {
		t.Fatalf("transfer host: response=%+v err=%v", transferred, err)
	}

	stored, err := fixture.runtime.Get(t.Context(), uuid.MustParse(started.Msg.GetSessionId()))
	if err != nil || stored.Snapshot().Status != gameruntime.StatusActive {
		t.Fatalf("stored session status=%s err=%v", stored.Snapshot().Status, err)
	}
	fixture.fanout.mu.Lock()
	events := append([]struct{}(nil), make([]struct{}, len(fixture.fanout.events))...)
	fixture.fanout.mu.Unlock()
	if len(events) != 3 {
		t.Fatalf("fanout count = %d, want start + pause + resume", len(events))
	}
}
