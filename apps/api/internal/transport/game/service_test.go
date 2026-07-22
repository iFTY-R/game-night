package game

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	"github.com/iFTY-R/game-night/platform/replay"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

const gameTransportOrigin = "https://play.example.test"

func TestGameServiceDerivesViewerAndCurrentHostFinishAction(t *testing.T) {
	fixture := newGameTransportFixture(t, false)
	client := fixture.client(t)

	hostRequest := connect.NewRequest(&gamev1.GetProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_PLAYER,
	})
	authorizeGameRead(hostRequest, "host-device")
	hostProjection, err := client.GetProjection(t.Context(), hostRequest)
	if err != nil {
		t.Fatal(err)
	}
	if got := hostProjection.Msg.GetProjection().GetAllowedActions(); len(got) != 2 || got[0] != "round.roll" || got[1] != string(finishAction) {
		t.Fatalf("host allowed actions = %v", got)
	}
	if fixture.runtime.lastViewer.Kind != gameSDK.ViewerPlayer || fixture.runtime.lastViewer.SeatIndex != 0 {
		t.Fatalf("host viewer = %+v", fixture.runtime.lastViewer)
	}

	playerRequest := connect.NewRequest(&gamev1.GetProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_PLAYER,
	})
	authorizeGameRead(playerRequest, "player-device")
	playerProjection, err := client.GetProjection(t.Context(), playerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if got := playerProjection.Msg.GetProjection().GetAllowedActions(); len(got) != 1 || got[0] != "round.roll" {
		t.Fatalf("player allowed actions = %v", got)
	}

	spectatorRequest := connect.NewRequest(&gamev1.GetProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_SPECTATOR,
	})
	authorizeGameRead(spectatorRequest, "spectator-device")
	spectatorProjection, err := client.GetProjection(t.Context(), spectatorRequest)
	if err != nil || spectatorProjection.Msg.GetProjection().GetViewerKind() != gamev1.ViewerKind_VIEWER_KIND_SPECTATOR {
		t.Fatalf("spectator projection = %+v err=%v", spectatorProjection, err)
	}

	wrongRole := connect.NewRequest(&gamev1.GetProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_PLAYER,
	})
	authorizeGameRead(wrongRole, "spectator-device")
	if _, err := client.GetProjection(t.Context(), wrongRole); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong-role projection error = %v", err)
	}
}

func TestGameServiceStartForwardsDurableOperationBinding(t *testing.T) {
	fixture := newGameTransportFixture(t, false)
	operationID := gameTransportOperationID(t, 2)
	digest := sha256.Sum256([]byte("game-start-request"))
	request := connect.NewRequest(&gamev1.StartSessionRequest{
		RoomId: fixture.roomID.String(), GameId: "liars-dice",
		ExpectedRoomVersion: fixture.room.Snapshot().RoomVersion, ExpectedMembershipVersion: fixture.room.Snapshot().MembershipVersion,
		OperationId: operationID.Value(), RequestDigest: digest[:],
		Config: &gamev1.GameConfig{GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured")},
	})
	authorizeGameWrite(request, "host-device")
	response, err := fixture.client(t).StartSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := fixture.runtime.lastStart
	if command.ActorUserID != fixture.hostID || command.RoomID != fixture.roomID || command.OperationID.Value() != operationID.Value() ||
		command.RequestDigest == nil || *command.RequestDigest != idempotency.Digest(digest) {
		t.Fatalf("runtime start command = %+v", command)
	}
	if response.Msg.GetSession().GetSessionId() != fixture.sessionID.String() || len(fixture.fanout.events) != 1 {
		t.Fatalf("start response=%+v fanout=%+v", response.Msg, fixture.fanout.events)
	}
}

func TestGameServiceOpenSubscriptionBindsOneViewerGrant(t *testing.T) {
	fixture := newGameTransportFixture(t, false)
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.OpenSubscriptionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_PLAYER, LastStateVersion: 1,
	})
	authorizeGameWrite(request, "player-device")
	response, err := client.OpenSubscription(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Msg.GetTicket()) != "ticket-1" || !bytes.Equal(response.Msg.GetGrant(), fixture.tickets.grant) {
		t.Fatalf("subscription response = %+v", response.Msg)
	}
	var grant gamev1.SubscriptionGrant
	if err := proto.Unmarshal(fixture.tickets.grant, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.GetUserId() != fixture.playerID.String() || grant.GetRoomId() != fixture.roomID.String() ||
		grant.GetSessionId() != fixture.sessionID.String() || grant.GetViewerKind() != gamev1.ViewerKind_VIEWER_KIND_PLAYER ||
		grant.GetSeatIndex() != 1 || grant.GetOrigin() != gameTransportOrigin || grant.GetLastEventOrdinal() != 0 ||
		grant.GetLastStateVersion() != fixture.session.Snapshot().State.StateVersion {
		t.Fatalf("subscription grant = %+v", &grant)
	}

	invalidCursor := connect.NewRequest(&gamev1.OpenSubscriptionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_PLAYER, LastEventOrdinal: 1,
	})
	authorizeGameWrite(invalidCursor, "player-device")
	if _, err := client.OpenSubscription(t.Context(), invalidCursor); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("partial cursor error = %v", err)
	}
}

func TestGameServiceActionUsesPersistedOwnershipAndPublishesCommittedCursor(t *testing.T) {
	fixture := newGameTransportFixture(t, false)
	client := fixture.client(t)
	operationID := gameTransportOperationID(t, 3)
	digest := sha256.Sum256([]byte("game-action-request"))
	request := connect.NewRequest(&gamev1.GameActionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(), ActionId: operationID.Value(),
		ExpectedStateVersion: fixture.session.Snapshot().State.StateVersion,
		Command:              gameTransportEnvelope(fixture.session.Snapshot().VersionKey, "round.roll", []byte("roll")),
		RequestDigest:        digest[:],
	})
	authorizeGameWrite(request, "player-device")
	response, err := client.GameAction(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	command := fixture.runtime.lastAction
	if command.ActorUserID != fixture.playerID || command.OwnershipEpoch != fixture.session.Snapshot().OwnershipEpoch ||
		command.VersionKey != fixture.session.Snapshot().VersionKey || command.RequestDigest == nil || command.RequestDigest.Bytes()[0] != digest[0] {
		t.Fatalf("runtime action command = %+v", command)
	}
	if response.Msg.GetReceipt().GetOperationId() != operationID.Value() || len(fixture.fanout.events) != 1 ||
		fixture.fanout.events[0].SessionID != fixture.sessionID {
		t.Fatalf("response=%+v fanout=%+v", response.Msg, fixture.fanout.events)
	}

	missingCSRF := connect.NewRequest(request.Msg)
	authorizeGameRead(missingCSRF, "player-device")
	missingCSRF.Header().Set(origin.HeaderName, gameTransportOrigin)
	if _, err := client.GameAction(t.Context(), missingCSRF); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("missing csrf error = %v", err)
	}
}

func TestGameServiceReplayRequiresTerminalSessionAndFrozenParticipation(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
	})
	authorizeGameRead(request, "player-device")
	response, err := client.GetReplayProjection(t.Context(), request)
	if err != nil || !response.Msg.GetComplete() || fixture.runtime.lastReplayPolicy != gameSDK.ReplayAccessParticipant {
		t.Fatalf("replay response=%+v policy=%s err=%v", response, fixture.runtime.lastReplayPolicy, err)
	}
}

func TestGameServiceReplayRejectsActiveSessionParticipant(t *testing.T) {
	fixture := newGameTransportFixture(t, false)
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
	})
	authorizeGameRead(request, "player-device")
	if _, err := client.GetReplayProjection(t.Context(), request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("active session replay error = %v", err)
	}
}

func TestGameServiceReplayDoesNotTreatCurrentSpectatorAsHistoricalMember(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
	})
	authorizeGameRead(request, "spectator-device")
	if _, err := client.GetReplayProjection(t.Context(), request); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("current spectator replay error = %v", err)
	}
}

func TestGameServiceHostCanReadAndUpdateReplayAccessPolicy(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	client := fixture.client(t)
	getRequest := connect.NewRequest(&gamev1.GetReplayAccessRequest{RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String()})
	authorizeGameRead(getRequest, "host-device")
	access, err := client.GetReplayAccess(t.Context(), getRequest)
	if err != nil || access.Msg.GetAccess().GetPolicy() != gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_PARTICIPANT || access.Msg.GetAccess().GetPolicyVersion() != 1 {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	setRequest := connect.NewRequest(&gamev1.SetReplayAccessRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		Policy: gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_ROOM_MEMBER, ExpectedPolicyVersion: 1,
	})
	authorizeGameWrite(setRequest, "host-device")
	updated, err := client.SetReplayAccess(t.Context(), setRequest)
	if err != nil || updated.Msg.GetAccess().GetPolicy() != gamev1.ReplayAccessPolicy_REPLAY_ACCESS_POLICY_ROOM_MEMBER || updated.Msg.GetAccess().GetPolicyVersion() != 2 {
		t.Fatalf("updated access=%+v err=%v", updated, err)
	}
}

func TestGameServiceReplayAccessRequiresCurrentHost(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayAccessRequest{RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String()})
	authorizeGameRead(request, "player-device")
	if _, err := client.GetReplayAccess(t.Context(), request); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-host replay access error = %v", err)
	}
}

func TestGameServiceReplayRetainsFrozenParticipantAccessAfterRemoval(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	now := fixture.room.Snapshot().UpdatedAt.Add(time.Second)
	removedRoom, err := roomDomain.Restore(roomDomain.RoomSnapshot{
		ID: fixture.roomID, RoomCode: fixture.room.Snapshot().RoomCode, Visibility: roomDomain.VisibilityPrivate,
		Status: roomDomain.RoomStatusPostGame, HostUserID: fixture.hostID, ParticipantCapacity: 4,
		ParticipantAdmission: roomDomain.AdmissionClosed, SpectatorAdmission: roomDomain.AdmissionOpen,
		Members: []roomDomain.MemberSnapshot{
			{UserID: fixture.hostID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 0, JoinedAt: now, LastSeenAt: now},
			{UserID: fixture.spectatorID, Role: roomDomain.MemberRoleSpectator, JoinedAt: now, LastSeenAt: now},
		},
		LastFinishedSessionID: fixture.sessionID, LastFinishedGameID: "liars-dice",
		RoomVersion: 5, MembershipVersion: 4, CreatedAt: fixture.room.Snapshot().CreatedAt, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.room = removedRoom
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
	})
	authorizeGameRead(request, "player-device")
	response, err := client.GetReplayProjection(t.Context(), request)
	if err != nil || !response.Msg.GetComplete() || fixture.runtime.lastReplayPolicy != gameSDK.ReplayAccessParticipant {
		t.Fatalf("removed participant replay response=%+v policy=%s err=%v", response, fixture.runtime.lastReplayPolicy, err)
	}
}

func TestGameServiceReplayRejectsRemovedNonParticipant(t *testing.T) {
	fixture := newGameTransportFixture(t, true)
	now := fixture.room.Snapshot().UpdatedAt.Add(time.Second)
	removedRoom, err := roomDomain.Restore(roomDomain.RoomSnapshot{
		ID: fixture.roomID, RoomCode: fixture.room.Snapshot().RoomCode, Visibility: roomDomain.VisibilityPrivate,
		Status: roomDomain.RoomStatusPostGame, HostUserID: fixture.hostID, ParticipantCapacity: 4,
		ParticipantAdmission: roomDomain.AdmissionClosed, SpectatorAdmission: roomDomain.AdmissionOpen,
		Members:               []roomDomain.MemberSnapshot{{UserID: fixture.hostID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 0, JoinedAt: now, LastSeenAt: now}},
		LastFinishedSessionID: fixture.sessionID, LastFinishedGameID: "liars-dice",
		RoomVersion: 5, MembershipVersion: 4, CreatedAt: fixture.room.Snapshot().CreatedAt, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.room = removedRoom
	client := fixture.client(t)
	request := connect.NewRequest(&gamev1.GetReplayProjectionRequest{
		RoomId: fixture.roomID.String(), SessionId: fixture.sessionID.String(),
		ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
	})
	authorizeGameRead(request, "spectator-device")
	if _, err := client.GetReplayProjection(t.Context(), request); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("removed non-participant replay error = %v", err)
	}
}

type gameTransportFixture struct {
	hostID, playerID, spectatorID uuid.UUID
	roomID, sessionID             uuid.UUID
	room                          roomDomain.Room
	session                       gameruntime.Session
	runtime                       *gameTransportRuntime
	tickets                       *gameTransportTickets
	fanout                        *gameTransportFanout
	authenticator                 *gameTransportAuthenticator
	clock                         *clock.Fake
	replays                       *gameTransportReplayRepository
}

func newGameTransportFixture(t testing.TB, terminal bool) *gameTransportFixture {
	t.Helper()
	now := time.Date(2026, time.July, 20, 9, 0, 0, 0, time.UTC)
	hostID, playerID, spectatorID := uuid.New(), uuid.New(), uuid.New()
	roomID, sessionID := uuid.New(), uuid.New()
	roomStatus, activeSessionID, activeGameID := roomDomain.RoomStatusPlaying, sessionID, "liars-dice"
	sessionStatus, endedAt := gameruntime.StatusActive, time.Time{}
	if terminal {
		roomStatus, activeSessionID, activeGameID = roomDomain.RoomStatusLobby, uuid.Nil, ""
		sessionStatus, endedAt = gameruntime.StatusFinished, now.Add(time.Second)
	}
	room, err := roomDomain.Restore(roomDomain.RoomSnapshot{
		ID: roomID, RoomCode: "GAME01", Visibility: roomDomain.VisibilityPrivate, Status: roomStatus,
		HostUserID: hostID, ParticipantCapacity: 4, ParticipantAdmission: roomDomain.AdmissionClosed,
		SpectatorAdmission: roomDomain.AdmissionOpen,
		Members: []roomDomain.MemberSnapshot{
			{UserID: hostID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 0, JoinedAt: now, LastSeenAt: now},
			{UserID: playerID, Role: roomDomain.MemberRoleParticipant, SeatIndex: 1, JoinedAt: now.Add(time.Microsecond), LastSeenAt: now.Add(time.Microsecond)},
			{UserID: spectatorID, Role: roomDomain.MemberRoleSpectator, JoinedAt: now.Add(2 * time.Microsecond), LastSeenAt: now.Add(2 * time.Microsecond)},
		},
		ActiveSessionID: activeSessionID, ActiveGameID: activeGameID,
		RoomVersion: 4, MembershipVersion: 3, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	key := gameSDK.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"}
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: sessionID, RoomID: roomID, VersionKey: key, OwnershipEpoch: 7,
		Participants: []gameruntime.Participant{{UserID: hostID, SeatIndex: 0}, {UserID: playerID, SeatIndex: 1}},
		State:        gameSDK.Snapshot{SnapshotVersion: 1, StateVersion: 3, State: gameTransportMessage("round.state", []byte("safe"))},
		Status:       sessionStatus, StartedAt: now, UpdatedAt: now.Add(time.Second), EndedAt: endedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection := gameSDK.Projection{
		View:           gameTransportMessage("viewer.state", []byte("viewer-safe")),
		AllowedActions: []gameSDK.Identifier{"round.roll", finishAction},
	}
	runtime := &gameTransportRuntime{room: room, session: session, projection: projection}
	replays := &gameTransportReplayRepository{
		hostID: hostID,
		access: replay.Access{
			SessionID: sessionID, RoomID: roomID, Policy: replay.PolicyParticipant, Version: 1,
			MemberSnapshotCompletedAt: endedAt, CreatedAt: now, UpdatedAt: now.Add(time.Second),
		},
		policies: map[uuid.UUID]gameSDK.ReplayAccessPolicy{hostID: gameSDK.ReplayAccessParticipant, playerID: gameSDK.ReplayAccessParticipant},
	}
	return &gameTransportFixture{
		hostID: hostID, playerID: playerID, spectatorID: spectatorID,
		roomID: roomID, sessionID: sessionID, room: room, session: session, runtime: runtime,
		tickets: &gameTransportTickets{}, fanout: &gameTransportFanout{},
		authenticator: &gameTransportAuthenticator{actors: map[string]uuid.UUID{
			"host-device": hostID, "player-device": playerID, "spectator-device": spectatorID,
		}},
		clock: clock.NewFake(now.Add(2 * time.Second)), replays: replays,
	}
}

func (fixture *gameTransportFixture) client(t testing.TB) gamev1connect.GameServiceClient {
	t.Helper()
	origins, err := origin.NewUserValidator(sharedconfig.OriginAllowlist{gameTransportOrigin})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		fixture.runtime, gameTransportSessionReader{session: fixture.session}, gameTransportRoomReader{room: fixture.room},
		fixture.replays, fixture.authenticator, origins, csrf.NewUserValidator(), fixture.tickets, fixture.fanout,
		fixture.clock, 30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := gamev1connect.NewGameServiceHandler(service, connect.WithInterceptors(transporterrors.Interceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return gamev1connect.NewGameServiceClient(server.Client(), server.URL)
}

type gameTransportRuntime struct {
	room             roomDomain.Room
	session          gameruntime.Session
	projection       gameSDK.Projection
	lastViewer       gameSDK.Viewer
	lastStart        gameruntime.StartCommand
	lastAction       gameruntime.ActionCommand
	lastReplayPolicy gameSDK.ReplayAccessPolicy
}

func (runtime *gameTransportRuntime) Start(_ context.Context, command gameruntime.StartCommand) (roomDomain.Room, gameruntime.Session, error) {
	runtime.lastStart = command
	return runtime.room, runtime.session, nil
}

func (runtime *gameTransportRuntime) HandleAction(_ context.Context, command gameruntime.ActionCommand) (gameruntime.ActionResult, error) {
	runtime.lastAction = command
	operationID, err := idempotency.ParseOperationID(string(command.ActionID))
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	requestDigest := idempotency.Digest(sha256.Sum256([]byte("fallback")))
	if command.RequestDigest != nil {
		requestDigest = *command.RequestDigest
	}
	receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: command.SessionID, ActorUserID: command.ActorUserID, ActionID: operationID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted,
		ResultDigest: idempotency.Digest(sha256.Sum256([]byte("result"))),
		StateVersion: runtime.session.Snapshot().State.StateVersion, CommittedAt: runtime.session.Snapshot().UpdatedAt,
	})
	if err != nil {
		return gameruntime.ActionResult{}, err
	}
	return gameruntime.ActionResult{Session: runtime.session, Receipt: receipt, Projection: runtime.projection}, nil
}

func (*gameTransportRuntime) HandleSystem(context.Context, gameruntime.SystemCommand) (gameruntime.SystemCommitResult, error) {
	return gameruntime.SystemCommitResult{}, errors.New("unused test system command")
}

func (*gameTransportRuntime) Cancel(context.Context, gameruntime.CancelCommand) (roomDomain.Room, gameruntime.Session, error) {
	return roomDomain.Room{}, gameruntime.Session{}, errors.New("unused test cancel command")
}

func (runtime *gameTransportRuntime) ProjectCurrent(_ context.Context, _ uuid.UUID, viewer gameSDK.Viewer) (gameruntime.Session, gameSDK.Projection, error) {
	runtime.lastViewer = viewer
	return runtime.session, runtime.projection, nil
}

func (runtime *gameTransportRuntime) ProjectReplayCurrent(_ context.Context, _ uuid.UUID, viewer gameSDK.Viewer, policy gameSDK.ReplayAccessPolicy) (gameruntime.Session, gameSDK.Projection, error) {
	runtime.lastViewer, runtime.lastReplayPolicy = viewer, policy
	return runtime.session, runtime.projection, nil
}

type gameTransportSessionReader struct{ session gameruntime.Session }

func (reader gameTransportSessionReader) Get(context.Context, uuid.UUID) (gameruntime.Session, error) {
	return reader.session, nil
}

type gameTransportRoomReader struct{ room roomDomain.Room }

func (reader gameTransportRoomReader) GetByID(context.Context, uuid.UUID) (roomDomain.Room, error) {
	return reader.room, nil
}

type gameTransportReplayRepository struct {
	hostID   uuid.UUID
	access   replay.Access
	policies map[uuid.UUID]gameSDK.ReplayAccessPolicy
}

func (repository *gameTransportReplayRepository) Authorize(_ context.Context, actorID, roomID, sessionID uuid.UUID) (gameSDK.ReplayAccessPolicy, error) {
	if roomID != repository.access.RoomID || sessionID != repository.access.SessionID || repository.access.MemberSnapshotCompletedAt.IsZero() {
		return "", replay.ErrPolicyUnavailable
	}
	policy, allowed := repository.policies[actorID]
	if !allowed {
		return "", replay.ErrAccessDenied
	}
	return policy, nil
}

func (repository *gameTransportReplayRepository) Get(_ context.Context, actorID, roomID, sessionID uuid.UUID) (replay.Access, error) {
	if actorID == uuid.Nil || actorID != repository.hostID || roomID != repository.access.RoomID || sessionID != repository.access.SessionID {
		return replay.Access{}, replay.ErrAccessDenied
	}
	return repository.access, nil
}

func (repository *gameTransportReplayRepository) SetPolicy(_ context.Context, command replay.SetPolicyCommand) (replay.Access, error) {
	if command.ActorUserID != repository.hostID || command.ExpectedVersion != repository.access.Version {
		return replay.Access{}, replay.ErrPolicyConflict
	}
	repository.access.Policy = command.Policy
	repository.access.Version++
	repository.access.UpdatedAt = command.UpdatedAt
	return repository.access, nil
}

type gameTransportTickets struct{ grant []byte }

func (tickets *gameTransportTickets) IssueConnectionTicket(_ context.Context, grant []byte) (string, error) {
	tickets.grant = append([]byte(nil), grant...)
	return "ticket-1", nil
}

type gameTransportFanout struct {
	events []redisstore.SessionFanoutEvent
}

func (fanout *gameTransportFanout) PublishSessionFanout(_ context.Context, event redisstore.SessionFanoutEvent) error {
	fanout.events = append(fanout.events, event)
	return nil
}

type gameTransportAuthenticator struct{ actors map[string]uuid.UUID }

func (authenticator *gameTransportAuthenticator) Authenticate(_ context.Context, deviceToken, csrfToken string) (uuid.UUID, error) {
	if csrfToken == "" {
		return uuid.Nil, errors.New("missing csrf")
	}
	actor, present := authenticator.actors[deviceToken]
	if !present {
		return uuid.Nil, identityAuthenticationError{}
	}
	return actor, nil
}

type identityAuthenticationError struct{}

func (identityAuthenticationError) Error() string { return "test device is unknown" }

func authorizeGameWrite[T any](request *connect.Request[T], deviceToken string) {
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, csrf.TokenBytes))
	request.Header().Set(origin.HeaderName, gameTransportOrigin)
	request.Header().Set(csrf.HeaderName, csrfToken)
	request.Header().Set("Cookie", cookies.UserDeviceCookieName+"="+deviceToken+"; "+cookies.UserCSRFCookieName+"="+csrfToken)
}

func authorizeGameRead[T any](request *connect.Request[T], deviceToken string) {
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, csrf.TokenBytes))
	request.Header().Set("Cookie", cookies.UserDeviceCookieName+"="+deviceToken+"; "+cookies.UserCSRFCookieName+"="+csrfToken)
}

func gameTransportMessage(messageType string, payload []byte) gameSDK.Message {
	return gameSDK.Message{MessageType: gameSDK.Identifier(messageType), SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func gameTransportEnvelope(key gameSDK.VersionKey, messageType string, payload []byte) *gamev1.GameEnvelope {
	return &gamev1.GameEnvelope{
		GameId: string(key.GameID), Version: &gamev1.VersionTuple{
			Engine: string(key.Engine), Protocol: string(key.Protocol), Client: string(key.Client),
		},
		SchemaVersion: 1, MessageType: messageType, Payload: append([]byte(nil), payload...),
	}
}

func gameTransportOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

var _ Runtime = (*gameTransportRuntime)(nil)
