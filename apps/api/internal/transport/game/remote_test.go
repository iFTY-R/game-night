package game

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	"github.com/iFTY-R/game-night/platform/idempotency"
	redisstore "github.com/iFTY-R/game-night/platform/persistence/redis"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	gameSDK "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRemoteRuntimeRoutesActionOnlyToAllowlistedReadyOwner(t *testing.T) {
	actorID, roomID, sessionID := uuid.New(), uuid.New(), uuid.New()
	operationID := remoteOperationID(t, 4)
	token := string(bytes.Repeat([]byte{'t'}, 32))
	service := &remoteOwnerFixture{
		token: token, actorID: actorID, roomID: roomID, sessionID: sessionID, operationID: operationID,
	}
	path, handler := realtimev1connect.NewOwnerServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	service.routeAddress = server.URL
	runtime, err := NewRemoteRuntime(server.Client(), RemoteRuntimeConfig{
		BootstrapURL: server.URL, PeerURLs: []string{server.URL}, InternalToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := idempotency.Digest(sha256.Sum256([]byte("remote-action")))
	result, err := runtime.HandleAction(t.Context(), gameruntime.ActionCommand{
		SessionID: sessionID, ActorUserID: actorID, ActionID: gameSDK.ActionID(operationID.Value()),
		ExpectedStateVersion: 1, VersionKey: remoteVersion(), Command: remoteMessage("round.roll", []byte("roll")),
		RequestDigest: &digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.resolveCalls != 1 || service.actionCalls != 1 || !service.sawToken ||
		result.Session.Snapshot().OwnershipEpoch != 7 || result.Projection.View.MessageType != "viewer.state" {
		t.Fatalf("resolve=%d action=%d token=%t result=%+v", service.resolveCalls, service.actionCalls, service.sawToken, result)
	}
	closed, cancelled, err := runtime.Cancel(t.Context(), gameruntime.CancelCommand{
		RoomID: roomID, SessionID: sessionID, ExpectedRoom: roomDomain.Version{Room: 2, Membership: 1},
		OwnershipEpoch: 7, Reason: gameruntime.CancelReasonPlatformCancelled, CloseRoom: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.resolveCalls != 2 || service.cancelCalls != 1 || closed.Snapshot().Status != roomDomain.RoomStatusClosed ||
		cancelled.Snapshot().Status != gameruntime.StatusCancelled {
		t.Fatalf("resolve=%d cancel=%d room=%+v session=%+v", service.resolveCalls, service.cancelCalls, closed.Snapshot(), cancelled.Snapshot())
	}

	service.routeAddress = "http://not-allowlisted.internal:8091"
	if _, err := runtime.HandleAction(t.Context(), gameruntime.ActionCommand{
		SessionID: sessionID, ActorUserID: actorID, ActionID: gameSDK.ActionID(operationID.Value()),
		ExpectedStateVersion: 1, VersionKey: remoteVersion(), Command: remoteMessage("round.roll", nil), RequestDigest: &digest,
	}); !errors.Is(err, redisstore.ErrCoordinationUnavailable) || service.actionCalls != 1 {
		t.Fatalf("untrusted route error=%v action calls=%d", err, service.actionCalls)
	}
}

func TestRemoteRuntimeStartForwardsConfigRevisionAndRestoresFrozenStart(t *testing.T) {
	actorID, roomID := uuid.New(), uuid.New()
	sessionID := uuid.New()
	operationID := remoteOperationID(t, 6)
	token := string(bytes.Repeat([]byte{'s'}, 32))
	service := &remoteOwnerFixture{
		token: token, actorID: actorID, roomID: roomID, sessionID: sessionID, operationID: operationID,
	}
	path, handler := realtimev1connect.NewOwnerServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	service.routeAddress = server.URL
	runtime, err := NewRemoteRuntime(server.Client(), RemoteRuntimeConfig{
		BootstrapURL: server.URL, PeerURLs: []string{server.URL}, InternalToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := idempotency.Digest(sha256.Sum256([]byte("remote-start")))
	proof := &gameruntime.PendingStartProof{PendingStartID: uuid.New(), CancelToken: "pending-start-token"}
	room, session, err := runtime.Start(t.Context(), gameruntime.StartCommand{
		ActorUserID: actorID, RoomID: roomID, GameID: remoteVersion().GameID,
		Expected:       roomDomain.Version{Room: 2, Membership: 1},
		ConfigRevision: 9, PendingStartProof: proof,
		Config:      remoteMessage("session.config", []byte("configured")),
		OperationID: operationID, RequestDigest: &digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := session.Snapshot().Start
	if service.startCalls != 1 || service.lastStartRequest.GetConfigRevision() != 9 || room.Snapshot().ID != roomID ||
		service.lastStartRequest.GetPendingStartProof().GetPendingStartId() != proof.PendingStartID.String() ||
		service.lastStartRequest.GetPendingStartProof().GetCancelToken() != proof.CancelToken ||
		start.ConfigRevision != 9 || start.RoomVersion != 2 || start.MembershipVersion != 1 || start.RoomOwnershipEpoch != 7 {
		t.Fatalf("request=%+v room=%+v start=%+v", service.lastStartRequest, room.Snapshot(), start)
	}
}

func TestSessionFromRemoteAllowsLegacySnapshotWithoutFrozenStartConfig(t *testing.T) {
	session, err := sessionFromRemote(remoteSessionWire(uuid.New(), uuid.New(), uuid.New(), time.Date(2026, time.July, 20, 15, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(session.Snapshot().Start, gameruntime.FrozenStartConfig{}) {
		t.Fatalf("legacy start should stay zero: %+v", session.Snapshot().Start)
	}
}

func TestRemoteRuntimeReplayValidatesCancelledTerminalMeta(t *testing.T) {
	actorID, roomID, sessionID := uuid.New(), uuid.New(), uuid.New()
	token := string(bytes.Repeat([]byte{'r'}, 32))
	service := &remoteOwnerFixture{
		token: token, actorID: actorID, roomID: roomID, sessionID: sessionID, operationID: remoteOperationID(t, 8),
	}
	path, handler := realtimev1connect.NewOwnerServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	service.routeAddress = server.URL
	runtime, err := NewRemoteRuntime(server.Client(), RemoteRuntimeConfig{
		BootstrapURL: server.URL, PeerURLs: []string{server.URL}, InternalToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, projection, err := runtime.ProjectReplayCurrent(
		t.Context(),
		sessionID,
		gameSDK.Viewer{Kind: gameSDK.ViewerReplay, UserID: gameSDK.Identifier(actorID.String())},
		gameSDK.ReplayAccessParticipant,
	)
	if err != nil {
		t.Fatal(err)
	}
	if service.replayCalls != 1 || session.Snapshot().CancelReason != gameruntime.CancelReasonLegacyCancelled ||
		!projection.Valid() {
		t.Fatalf("replay calls=%d session=%+v projection=%+v", service.replayCalls, session.Snapshot(), projection)
	}
}

type remoteOwnerFixture struct {
	realtimev1connect.UnimplementedOwnerServiceHandler
	token, routeAddress        string
	actorID, roomID, sessionID uuid.UUID
	operationID                idempotency.OperationID
	lastStartRequest           *realtimev1.StartSessionRequest
	resolveCalls, actionCalls  int
	startCalls                 int
	cancelCalls                int
	replayCalls                int
	sawToken                   bool
}

func (service *remoteOwnerFixture) StartSession(
	_ context.Context,
	request *connect.Request[realtimev1.StartSessionRequest],
) (*connect.Response[realtimev1.StartSessionResponse], error) {
	service.startCalls++
	service.sawToken = service.sawToken || request.Header().Get(internalTokenHeader) == service.token
	service.lastStartRequest = request.Msg
	now := time.Date(2026, time.July, 20, 12, 30, 0, 0, time.UTC)
	session := remoteSessionWire(service.sessionID, service.roomID, service.actorID, now)
	session.Start = &realtimev1.FrozenStartConfig{
		Config:         envelopeWire(remoteVersion(), remoteMessage("session.config", []byte("configured"))),
		ConfigDigest:   runtimeStartDigest(timestamppb.New(now).AsTime(), request.Msg.GetConfigRevision()),
		ConfigRevision: request.Msg.GetConfigRevision(), RoomVersion: request.Msg.GetExpectedRoomVersion(),
		MembershipVersion: request.Msg.GetExpectedMembershipVersion(), RoomOwnershipEpoch: 7,
	}
	return connect.NewResponse(&realtimev1.StartSessionResponse{
		Room: &realtimev1.RoomSnapshot{
			RoomId: service.roomID.String(), RoomCode: "REMOTE", Visibility: string(roomDomain.VisibilityPrivate),
			Status: string(roomDomain.RoomStatusPlaying), HostUserId: service.actorID.String(), ParticipantCapacity: 3,
			ParticipantAdmission: string(roomDomain.AdmissionClosed), SpectatorAdmission: string(roomDomain.AdmissionClosed),
			Members: []*realtimev1.RoomMember{{
				UserId: service.actorID.String(), Role: string(roomDomain.MemberRoleParticipant), SeatIndex: 0,
				JoinedAt: timestamppb.New(now), LastSeenAt: timestamppb.New(now),
			}},
			ActiveSessionId: service.sessionID.String(), ActiveGameId: string(remoteVersion().GameID),
			RoomVersion: request.Msg.GetExpectedRoomVersion(), MembershipVersion: request.Msg.GetExpectedMembershipVersion(),
			CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now),
		},
		Session: session,
	}), nil
}

func (service *remoteOwnerFixture) ResolveOwner(
	_ context.Context,
	request *connect.Request[realtimev1.ResolveOwnerRequest],
) (*connect.Response[realtimev1.ResolveOwnerResponse], error) {
	service.resolveCalls++
	service.sawToken = service.sawToken || request.Header().Get(internalTokenHeader) == service.token
	return connect.NewResponse(&realtimev1.ResolveOwnerResponse{
		InstanceId: "realtime-a", Address: service.routeAddress, OwnershipEpoch: 7,
	}), nil
}

func (service *remoteOwnerFixture) GameAction(
	_ context.Context,
	request *connect.Request[realtimev1.GameActionRequest],
) (*connect.Response[realtimev1.GameActionResponse], error) {
	service.actionCalls++
	service.sawToken = service.sawToken || request.Header().Get(internalTokenHeader) == service.token
	now := time.Date(2026, time.July, 20, 13, 0, 0, 0, time.UTC)
	digest := request.Msg.GetRequestDigest()
	resultDigest := sha256.Sum256([]byte("accepted"))
	return connect.NewResponse(&realtimev1.GameActionResponse{
		Session: remoteSessionWire(service.sessionID, service.roomID, service.actorID, now),
		Receipt: &realtimev1.ActionReceipt{
			SessionId: service.sessionID.String(), ActorUserId: service.actorID.String(),
			ActionId: service.operationID.Value(), RequestDigest: append([]byte(nil), digest...),
			ResultCode: string(gameruntime.ResultCodeAccepted), ResultDigest: resultDigest[:],
			StateVersion: 1, CommittedAt: timestamppb.New(now),
		},
		Projection: &gamev1.GameProjection{
			SessionId: service.sessionID.String(), StateVersion: 1,
			ViewerKind:     gamev1.ViewerKind_VIEWER_KIND_PLAYER,
			View:           envelopeWire(remoteVersion(), remoteMessage("viewer.state", []byte("safe"))),
			AllowedActions: []string{"round.roll"},
		},
	}), nil
}

func (service *remoteOwnerFixture) CancelSession(
	_ context.Context,
	request *connect.Request[realtimev1.CancelSessionRequest],
) (*connect.Response[realtimev1.CancelSessionResponse], error) {
	service.cancelCalls++
	service.sawToken = service.sawToken || request.Header().Get(internalTokenHeader) == service.token
	if request.Msg.GetRoomId() != service.roomID.String() || request.Msg.GetSessionId() != service.sessionID.String() ||
		request.Msg.GetExpectedRoomVersion() != 2 || request.Msg.GetExpectedMembershipVersion() != 1 || !request.Msg.GetCloseRoom() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid cancel request"))
	}
	now := time.Date(2026, time.July, 20, 13, 1, 0, 0, time.UTC)
	session := remoteSessionWire(service.sessionID, service.roomID, service.actorID, now)
	session.Status = gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED
	session.EndedAt = timestamppb.New(now)
	return connect.NewResponse(&realtimev1.CancelSessionResponse{
		Room: &realtimev1.RoomSnapshot{
			RoomId: service.roomID.String(), RoomCode: "REMOTE", Visibility: string(roomDomain.VisibilityPrivate),
			Status: string(roomDomain.RoomStatusClosed), HostUserId: service.actorID.String(), ParticipantCapacity: 3,
			ParticipantAdmission: string(roomDomain.AdmissionClosed), SpectatorAdmission: string(roomDomain.AdmissionClosed),
			Members: []*realtimev1.RoomMember{{
				UserId: service.actorID.String(), Role: string(roomDomain.MemberRoleParticipant), SeatIndex: 0,
				JoinedAt: timestamppb.New(now), LastSeenAt: timestamppb.New(now),
			}},
			RoomVersion: 3, MembershipVersion: 1, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now),
		},
		Session: session,
	}), nil
}

func (service *remoteOwnerFixture) GetReplayProjection(
	_ context.Context,
	request *connect.Request[realtimev1.GetReplayProjectionRequest],
) (*connect.Response[realtimev1.GetReplayProjectionResponse], error) {
	service.replayCalls++
	service.sawToken = service.sawToken || request.Header().Get(internalTokenHeader) == service.token
	now := time.Date(2026, time.July, 20, 13, 2, 0, 0, time.UTC)
	session := remoteSessionWire(service.sessionID, service.roomID, service.actorID, now)
	session.Status = gamev1.GameSessionStatus_GAME_SESSION_STATUS_CANCELLED
	session.EndedAt = timestamppb.New(now)
	session.CancelReason = string(gameruntime.CancelReasonLegacyCancelled)
	return connect.NewResponse(&realtimev1.GetReplayProjectionResponse{
		Session: session,
		Projection: &gamev1.GameProjection{
			SessionId: service.sessionID.String(), StateVersion: 1,
			ViewerKind: gamev1.ViewerKind_VIEWER_KIND_REPLAY,
			View:       envelopeWire(remoteVersion(), remoteMessage("replay.view", []byte("safe"))),
		},
		TerminalMeta: &gamev1.ReplayTerminalMeta{
			Cancelled: true, EndedAt: timestamppb.New(now), CancelReason: string(gameruntime.CancelReasonLegacyCancelled),
		},
	}), nil
}

func remoteSessionWire(sessionID, roomID, actorID uuid.UUID, now time.Time) *realtimev1.SessionSnapshot {
	version := remoteVersion()
	return &realtimev1.SessionSnapshot{
		SessionId: sessionID.String(), RoomId: roomID.String(), GameId: string(version.GameID),
		Version: &gamev1.VersionTuple{
			Engine: string(version.Engine), Protocol: string(version.Protocol), Client: string(version.Client),
		},
		OwnershipEpoch: 7, Participants: []*realtimev1.Participant{{UserId: actorID.String(), SeatIndex: 2}},
		SnapshotVersion: 1, StateVersion: 1,
		AuthoritativeState: envelopeWire(version, remoteMessage("round.state", []byte("private"))),
		Status:             gamev1.GameSessionStatus_GAME_SESSION_STATUS_ACTIVE,
		StartedAt:          timestamppb.New(now), UpdatedAt: timestamppb.New(now),
	}
}

func runtimeStartDigest(now time.Time, revision uint64) []byte {
	session, err := gameruntime.RestoreSession(gameruntime.SessionSnapshot{
		ID: uuid.New(), RoomID: uuid.New(), VersionKey: remoteVersion(), OwnershipEpoch: 7,
		Participants: []gameruntime.Participant{{UserID: uuid.New(), SeatIndex: 1}},
		Start: gameruntime.FrozenStartConfig{
			Config: remoteMessage("session.config", []byte("configured")), ConfigRevision: revision,
			RoomVersion: 2, MembershipVersion: 1, RoomOwnershipEpoch: 7,
		},
		State: gameSDK.Snapshot{
			SnapshotVersion: 1, StateVersion: 1, State: remoteMessage("round.state", []byte("private")),
		},
		Status: gameruntime.StatusActive, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		panic(err)
	}
	return session.Snapshot().Start.ConfigDigest.Bytes()
}

func remoteVersion() gameSDK.VersionKey {
	return gameSDK.VersionKey{GameID: "liars-dice", Engine: "1.0.0", Protocol: "1.0.0", Client: "1.0.0"}
}

func remoteMessage(messageType gameSDK.Identifier, payload []byte) gameSDK.Message {
	return gameSDK.Message{MessageType: messageType, SchemaVersion: 1, Payload: append([]byte(nil), payload...)}
}

func remoteOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	operationID, err := idempotency.NewOperationID(bytes.Repeat([]byte{marker}, 16))
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}
