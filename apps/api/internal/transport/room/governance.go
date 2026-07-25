package room

import (
	"context"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	gameruntime "github.com/iFTY-R/game-night/platform/game-runtime"
	roomDomain "github.com/iFTY-R/game-night/platform/room"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GameGovernance commits room pause metadata and the matching game-session lifecycle in one transaction.
type GameGovernance interface {
	PauseRoom(context.Context, gameruntime.PauseRoomCommand) (roomDomain.Room, gameruntime.Session, error)
	ResumeRoom(context.Context, gameruntime.ResumeRoomCommand) (roomDomain.Room, gameruntime.Session, error)
}

// RequestRoomPause records one participant request for the exact active game session.
func (service *Service) RequestRoomPause(
	ctx context.Context,
	request *connect.Request[roomv1.RequestRoomPauseRequest],
) (*connect.Response[roomv1.RequestRoomPauseResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	roomID, sessionID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.RequestPause(ctx, roomDomain.RequestPauseCommand{
		ActorUserID: actor, RoomID: roomID, SessionID: sessionID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.RequestRoomPauseResponse{Room: wireRoom}), nil
}

// RejectRoomPauseRequest rejects only the pending request observed by the current host.
func (service *Service) RejectRoomPauseRequest(
	ctx context.Context,
	request *connect.Request[roomv1.RejectRoomPauseRequestRequest],
) (*connect.Response[roomv1.RejectRoomPauseRequestResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	roomID, requestID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetRequestId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.RejectPause(ctx, roomDomain.RejectPauseCommand{
		ActorUserID: actor, RoomID: roomID, RequestID: requestID, Expected: versionDomain(request.Msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.RejectRoomPauseRequestResponse{Room: wireRoom}), nil
}

// PauseRoomGame lets the host pause directly or approve the exact pending participant request.
func (service *Service) PauseRoomGame(
	ctx context.Context,
	request *connect.Request[roomv1.PauseRoomGameRequest],
) (*connect.Response[roomv1.PauseRoomGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if service.governance == nil || request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil ||
		request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	requestID := uuid.Nil
	if strings.TrimSpace(request.Msg.GetRequestId()) != "" {
		requestID, err = parseUUID(request.Msg.GetRequestId())
		if err != nil {
			return nil, err
		}
	}
	updated, session, err := service.governance.PauseRoom(ctx, gameruntime.PauseRoomCommand{
		ActorUserID: actor, RoomID: roomID, SessionID: sessionID, RequestID: requestID,
		Expected: versionDomain(request.Msg.GetExpectedVersion()), OwnershipEpoch: request.Msg.GetOwnershipEpoch(),
	})
	if err != nil {
		return nil, err
	}
	if err := service.publish(ctx, session); err != nil {
		// The PostgreSQL transaction is already authoritative; fanout loss is repaired by client reconciliation.
		slog.WarnContext(ctx, "publish committed room pause", "session_id", sessionID.String(), "error", err)
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.PauseRoomGameResponse{Room: wireRoom, Session: gameSessionSummaryWire(session)}), nil
}

// ResumeRoomGame lets the current host resume the exact room-governed suspended session.
func (service *Service) ResumeRoomGame(
	ctx context.Context,
	request *connect.Request[roomv1.ResumeRoomGameRequest],
) (*connect.Response[roomv1.ResumeRoomGameResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if service.governance == nil || request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil ||
		request.Msg.GetOwnershipEpoch() == 0 {
		return nil, gameruntime.ErrInvalidSessionInput
	}
	roomID, sessionID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetSessionId())
	if err != nil {
		return nil, err
	}
	updated, session, err := service.governance.ResumeRoom(ctx, gameruntime.ResumeRoomCommand{
		ActorUserID: actor, RoomID: roomID, SessionID: sessionID,
		Expected: versionDomain(request.Msg.GetExpectedVersion()), OwnershipEpoch: request.Msg.GetOwnershipEpoch(),
	})
	if err != nil {
		return nil, err
	}
	if err := service.publish(ctx, session); err != nil {
		// Returning a failure here would invite a retry after the resume transaction has already committed.
		slog.WarnContext(ctx, "publish committed room resume", "session_id", sessionID.String(), "error", err)
	}
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.ResumeRoomGameResponse{Room: wireRoom, Session: gameSessionSummaryWire(session)}), nil
}

// TransferRoomHost immediately advances the governance fence and invalidates any old-host pending start.
func (service *Service) TransferRoomHost(
	ctx context.Context,
	request *connect.Request[roomv1.TransferRoomHostRequest],
) (*connect.Response[roomv1.TransferRoomHostResponse], error) {
	actor, err := service.authenticateWrite(ctx, requestHTTP(request))
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.GetExpectedVersion() == nil || request.Msg.GetOwnershipEpoch() == 0 {
		return nil, roomDomain.ErrInvalidRoomInput
	}
	roomID, targetUserID, err := twoUUIDs(request.Msg.GetRoomId(), request.Msg.GetTargetUserId())
	if err != nil {
		return nil, err
	}
	updated, err := service.domain.TransferHost(ctx, roomDomain.TransferHostCommand{
		ActorUserID: actor, RoomID: roomID, TargetUserID: targetUserID,
		Expected: versionDomain(request.Msg.GetExpectedVersion()), OwnershipEpoch: request.Msg.GetOwnershipEpoch(),
	})
	if err != nil {
		return nil, err
	}
	service.cancelPendingForRoom(ctx, roomID)
	wireRoom, err := service.roomWire(ctx, updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&roomv1.TransferRoomHostResponse{Room: wireRoom}), nil
}

func gameSessionSummaryWire(session gameruntime.Session) *gamev1.GameSessionSummary {
	snapshot := session.Snapshot()
	wire := &gamev1.GameSessionSummary{
		SessionId: snapshot.ID.String(), RoomId: snapshot.RoomID.String(), GameId: string(snapshot.VersionKey.GameID),
		Version: &gamev1.VersionTuple{
			Engine: string(snapshot.VersionKey.Engine), Protocol: string(snapshot.VersionKey.Protocol), Client: string(snapshot.VersionKey.Client),
		},
		StateVersion: snapshot.State.StateVersion, OwnershipEpoch: snapshot.OwnershipEpoch, Status: gameSessionStatusWire(snapshot.Status),
	}
	if !snapshot.SuspendedAt.IsZero() {
		wire.SuspendedAt = timestamppb.New(snapshot.SuspendedAt)
	}
	return wire
}

func gameSessionStatusWire(status gameruntime.Status) gamev1.GameSessionStatus {
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
