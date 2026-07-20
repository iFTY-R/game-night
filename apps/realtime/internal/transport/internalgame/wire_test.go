package internalgame

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
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

func TestWireRoundTripsDomainValues(t *testing.T) {
	fixture := newInternalGameFixture(t)
	now := fixture.session.Snapshot().UpdatedAt
	room, err := roomdomain.New(uuid.New(), fixture.actorID, "WIRE123", roomdomain.VisibilityPrivate, 8, now)
	if err != nil {
		t.Fatal(err)
	}
	restoredRoom, err := roomFromWire(roomToWire(room))
	if err != nil || !reflect.DeepEqual(restoredRoom.Snapshot(), room.Snapshot()) {
		t.Fatalf("room=%+v restored=%+v error=%v", room.Snapshot(), restoredRoom.Snapshot(), err)
	}

	sessionSnapshot := fixture.session.Snapshot()
	dueAt := now.Add(time.Minute)
	sessionSnapshot.Timers = []gameruntime.TimerSnapshot{{
		TimerID: "turn.timeout", ExpectedStateVersion: sessionSnapshot.State.StateVersion,
		DueAt: dueAt, Message: internalMessage("turn.timeout", []byte("timer")),
	}}
	sessionSnapshot.NextDeadlineAt = dueAt
	session, err := gameruntime.RestoreSession(sessionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredSession, err := sessionFromWire(sessionToWire(session))
	if err != nil || !reflect.DeepEqual(restoredSession.Snapshot(), session.Snapshot()) {
		t.Fatalf("session=%+v restored=%+v error=%v", session.Snapshot(), restoredSession.Snapshot(), err)
	}

	version := session.Snapshot().VersionKey
	message := internalMessage("round.roll", []byte("payload"))
	restoredVersion, restoredMessage, err := envelopeFromWire(envelopeToWire(version, message))
	if err != nil || restoredVersion != version || !reflect.DeepEqual(restoredMessage, message) {
		t.Fatalf("version=%+v message=%+v error=%v", restoredVersion, restoredMessage, err)
	}
	viewer := game.Viewer{Kind: game.ViewerPlayer, UserID: game.Identifier(fixture.actorID.String()), SeatIndex: 2}
	restoredViewer, err := viewerFromWire(viewerToWire(viewer))
	if err != nil || restoredViewer != viewer {
		t.Fatalf("viewer=%+v error=%v", restoredViewer, err)
	}
	projection := game.Projection{View: internalMessage("viewer.state", []byte("safe")), AllowedActions: []game.Identifier{"round.roll"}}
	restoredProjection, err := projectionFromWire(projectionToWire(session, viewer, projection))
	if err != nil || !reflect.DeepEqual(restoredProjection, projection) {
		t.Fatalf("projection=%+v error=%v", restoredProjection, err)
	}
}

func TestWireRoundTripsDurableReceipts(t *testing.T) {
	sessionID, actorID := uuid.New(), uuid.New()
	committedAt := time.Date(2026, time.July, 20, 14, 0, 0, 123000000, time.UTC)
	requestDigest := idempotency.Digest(sha256.Sum256([]byte("request")))
	resultDigest := idempotency.Digest(sha256.Sum256([]byte("result")))
	operationID := internalOperationID(t, 9)

	action, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
		Key:           gameruntime.ActionKey{SessionID: sessionID, ActorUserID: actorID, ActionID: operationID},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: resultDigest,
		StateVersion: 3, CommittedAt: committedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredAction, err := actionReceiptFromWire(actionReceiptToWire(action))
	if err != nil || !reflect.DeepEqual(restoredAction.Snapshot(), action.Snapshot()) {
		t.Fatalf("action=%+v error=%v", restoredAction.Snapshot(), err)
	}

	timer, err := gameruntime.NewTimerReceipt(gameruntime.TimerReceiptSnapshot{
		Key:        gameruntime.TimerKey{SessionID: sessionID, TimerID: "turn.timeout", ExpectedStateVersion: 2},
		ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: resultDigest, StateVersion: 3, CommittedAt: committedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredTimer, err := timerReceiptFromWire(timerReceiptToWire(timer))
	if err != nil || !reflect.DeepEqual(restoredTimer.Snapshot(), timer.Snapshot()) {
		t.Fatalf("timer=%+v error=%v", restoredTimer.Snapshot(), err)
	}

	system, err := gameruntime.NewSystemReceipt(gameruntime.SystemReceiptSnapshot{
		Key: gameruntime.SystemKey{
			SessionID: sessionID, OperationID: operationID,
			Source: gameruntime.SystemSource{Kind: gameruntime.SystemSourceHostAPI, EventID: uuid.New(), RequestedByUserID: actorID},
		},
		RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: resultDigest,
		StateVersion: 3, CommittedAt: committedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	restoredSystem, err := systemReceiptFromWire(systemReceiptToWire(system))
	if err != nil || !reflect.DeepEqual(restoredSystem.Snapshot(), system.Snapshot()) {
		t.Fatalf("system=%+v error=%v", restoredSystem.Snapshot(), err)
	}
}

func TestWireRejectsMalformedValues(t *testing.T) {
	fixture := newInternalGameFixture(t)
	version := fixture.session.Snapshot().VersionKey
	validEnvelope := func() *gamev1.GameEnvelope {
		return envelopeToWire(version, internalMessage("round.roll", nil))
	}
	validActionReceipt := func() *realtimev1.ActionReceipt {
		requestDigest := idempotency.Digest(sha256.Sum256([]byte("request")))
		resultDigest := idempotency.Digest(sha256.Sum256([]byte("result")))
		receipt, err := gameruntime.NewActionReceipt(gameruntime.ActionReceiptSnapshot{
			Key: gameruntime.ActionKey{
				SessionID: fixture.session.Snapshot().ID, ActorUserID: fixture.actorID, ActionID: internalOperationID(t, 10),
			},
			RequestDigest: requestDigest, ResultCode: gameruntime.ResultCodeAccepted, ResultDigest: resultDigest,
			StateVersion: 1, CommittedAt: fixture.session.Snapshot().UpdatedAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return actionReceiptToWire(receipt)
	}

	cases := []struct {
		name  string
		parse func() error
	}{
		{name: "nil room", parse: func() error { _, err := roomFromWire(nil); return err }},
		{name: "nil session", parse: func() error { _, err := sessionFromWire(nil); return err }},
		{name: "nil action receipt", parse: func() error { _, err := actionReceiptFromWire(nil); return err }},
		{name: "nil timer receipt", parse: func() error { _, err := timerReceiptFromWire(nil); return err }},
		{name: "nil system receipt", parse: func() error { _, err := systemReceiptFromWire(nil); return err }},
		{name: "noncanonical uuid", parse: func() error {
			_, err := requiredUUID("550E8400-E29B-41D4-A716-446655440000")
			return err
		}},
		{name: "zero uuid", parse: func() error { _, err := requiredUUID(uuid.Nil.String()); return err }},
		{name: "invalid timestamp", parse: func() error {
			_, err := requiredTime(&timestamppb.Timestamp{Seconds: 253402300800})
			return err
		}},
		{name: "zero timestamp", parse: func() error {
			_, err := requiredTime(timestamppb.New(time.Time{}))
			return err
		}},
		{name: "envelope without version", parse: func() error {
			_, _, err := envelopeFromWire(&gamev1.GameEnvelope{GameId: string(version.GameID), MessageType: "round.roll", SchemaVersion: 1})
			return err
		}},
		{name: "invalid semantic version", parse: func() error {
			value := validEnvelope()
			value.Version.Engine = "latest"
			_, _, err := envelopeFromWire(value)
			return err
		}},
		{name: "unspecified viewer", parse: func() error {
			_, err := viewerFromWire(&realtimev1.Viewer{UserId: fixture.actorID.String()})
			return err
		}},
		{name: "spectator with seat", parse: func() error {
			_, err := viewerFromWire(&realtimev1.Viewer{
				Kind: gamev1.ViewerKind_VIEWER_KIND_SPECTATOR, UserId: fixture.actorID.String(), SeatIndex: 1,
			})
			return err
		}},
		{name: "session with nil participant", parse: func() error {
			value := sessionToWire(fixture.session)
			value.Participants = []*realtimev1.Participant{nil}
			_, err := sessionFromWire(value)
			return err
		}},
		{name: "session state version key mismatch", parse: func() error {
			value := sessionToWire(fixture.session)
			value.AuthoritativeState.Version.Client = "2.0.0"
			_, err := sessionFromWire(value)
			return err
		}},
		{name: "action receipt with short digest", parse: func() error {
			value := validActionReceipt()
			value.RequestDigest = value.RequestDigest[:len(value.RequestDigest)-1]
			_, err := actionReceiptFromWire(value)
			return err
		}},
		{name: "timer receipt with invalid timer", parse: func() error {
			_, err := timerReceiptFromWire(&realtimev1.TimerReceipt{
				SessionId: fixture.session.Snapshot().ID.String(), TimerId: "NOT VALID", ExpectedStateVersion: 1,
				ResultCode: string(gameruntime.ResultCodeAccepted), ResultDigest: make([]byte, sha256.Size),
				StateVersion: 2, CommittedAt: timeToWire(fixture.session.Snapshot().UpdatedAt),
			})
			return err
		}},
		{name: "system receipt with invalid source", parse: func() error {
			_, err := systemReceiptFromWire(&realtimev1.SystemReceipt{
				SessionId: fixture.session.Snapshot().ID.String(), OperationId: internalOperationID(t, 11).Value(),
				SourceKind: "unknown", SourceEventId: uuid.New().String(), RequestDigest: make([]byte, sha256.Size),
				ResultCode: string(gameruntime.ResultCodeAccepted), ResultDigest: make([]byte, sha256.Size),
				StateVersion: 2, CommittedAt: timeToWire(fixture.session.Snapshot().UpdatedAt),
			})
			return err
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.parse(); !errors.Is(err, gameruntime.ErrInvalidSessionInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
