package gamewebsocket

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestCoderTransportStreamsBinaryViewerProjection(t *testing.T) {
	authorization := wireAuthorization(game.ViewerPlayer)
	registered := make(chan struct{})
	server := newCoderTestServer(t, &fakeHandlerAuthorizer{authorization: authorization}, time.Second,
		func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
			update := wireUpdate(authorization, 2)
			update.Projection = game.Projection{
				View:           game.Message{MessageType: "viewer.state", SchemaVersion: 1, Payload: []byte("safe")},
				AllowedActions: []game.Identifier{"round.roll"},
			}
			if err := sink.Send(t.Context(), update); err != nil {
				return nil, err
			}
			close(registered)
			return &fakeHandlerHandle{close: sink.Close}, nil
		})
	defer server.Close()
	connection := dialCoderTestServer(t, server, handlerOrigin)
	defer connection.CloseNow()
	writeHello(t, connection)

	frame := readServerFrame(t, connection)
	projection := frame.GetProjection()
	if projection == nil || projection.GetSessionId() != authorization.SessionID.String() ||
		projection.GetStateVersion() != 2 || string(projection.GetView().GetPayload()) != "safe" {
		t.Fatalf("projection = %+v", projection)
	}
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("connection was not registered")
	}
}

func TestCoderTransportRejectsUnlistedOriginBeforeUpgrade(t *testing.T) {
	authorizer := &fakeHandlerAuthorizer{authorization: wireAuthorization(game.ViewerPlayer)}
	server := newCoderTestServer(t, authorizer, time.Second, func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
		return &fakeHandlerHandle{close: sink.Close}, nil
	})
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, coderServerURL(server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://admin.game-night.test"}},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden || authorizer.calls != 0 {
		t.Fatalf("error=%v response=%v authorizations=%d", err, response, authorizer.calls)
	}
}

func TestCoderTransportRejectsConsumedTicket(t *testing.T) {
	authorizer := &fakeHandlerAuthorizer{authorization: wireAuthorization(game.ViewerPlayer), err: subscription.ErrTicketRejected}
	server := newCoderTestServer(t, authorizer, time.Second, func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
		t.Fatal("rejected ticket reached registration")
		return &fakeHandlerHandle{close: sink.Close}, nil
	})
	defer server.Close()
	connection := dialCoderTestServer(t, server, handlerOrigin)
	defer connection.CloseNow()
	writeHello(t, connection)

	if code := readServerFrame(t, connection).GetError().GetCode(); code != "subscription_rejected" {
		t.Fatalf("error code = %q", code)
	}
	assertCloseStatus(t, connection, websocket.StatusPolicyViolation)
}

func TestCoderTransportEnforcesHelloDeadline(t *testing.T) {
	authorizer := &fakeHandlerAuthorizer{authorization: wireAuthorization(game.ViewerPlayer)}
	server := newCoderTestServer(t, authorizer, 10*time.Millisecond, func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
		t.Fatal("timed-out hello reached registration")
		return &fakeHandlerHandle{close: sink.Close}, nil
	})
	defer server.Close()
	connection := dialCoderTestServer(t, server, handlerOrigin)
	defer connection.CloseNow()

	if code := readServerFrame(t, connection).GetError().GetCode(); code != "invalid_frame" {
		t.Fatalf("error code = %q", code)
	}
	assertCloseStatus(t, connection, websocket.StatusPolicyViolation)
}

func TestCoderTransportDrainsWithServiceRestart(t *testing.T) {
	authorization := wireAuthorization(game.ViewerSpectator)
	server := newCoderTestServer(t, &fakeHandlerAuthorizer{authorization: authorization}, time.Second,
		func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
			go sink.Close(subscription.ErrHubClosed)
			return &fakeHandlerHandle{close: sink.Close}, nil
		})
	defer server.Close()
	connection := dialCoderTestServer(t, server, handlerOrigin)
	defer connection.CloseNow()
	writeHello(t, connection)

	draining := readServerFrame(t, connection).GetDraining()
	if draining == nil || draining.GetReason() != "service_restart" || draining.GetReconnectAfter() == nil {
		t.Fatalf("draining = %+v", draining)
	}
	assertCloseStatus(t, connection, websocket.StatusServiceRestart)
}

func TestCoderTransportReconnectsFromNewGrantCursor(t *testing.T) {
	first := wireAuthorization(game.ViewerPlayer)
	second := first
	second.Cursor = 2
	second.CurrentVersion = 2
	authorizer := &sequenceAuthorizer{authorizations: []subscription.Authorization{first, second}}
	server := newCoderTestServer(t, authorizer, time.Second,
		func(authorization subscription.Authorization, sink subscription.Sink) (Handle, error) {
			update := wireUpdate(authorization, authorization.Cursor+1)
			update.Delta = game.EventProjection{Messages: []game.Message{{
				MessageType: "viewer.delta", SchemaVersion: 1, Payload: []byte{byte(authorization.Cursor + 1)},
			}}}
			if err := sink.Send(t.Context(), update); err != nil {
				return nil, err
			}
			return &fakeHandlerHandle{close: sink.Close}, nil
		})
	defer server.Close()

	firstConnection := dialCoderTestServer(t, server, handlerOrigin)
	writeHelloWithTicket(t, firstConnection, "ticket-1")
	firstDelta := readServerFrame(t, firstConnection).GetDelta()
	if firstDelta.GetFromStateVersion() != 1 || firstDelta.GetToStateVersion() != 2 {
		t.Fatalf("first delta = %+v", firstDelta)
	}
	_ = firstConnection.Close(websocket.StatusNormalClosure, "")

	secondConnection := dialCoderTestServer(t, server, handlerOrigin)
	defer secondConnection.CloseNow()
	writeHelloWithTicket(t, secondConnection, "ticket-2")
	secondDelta := readServerFrame(t, secondConnection).GetDelta()
	if secondDelta.GetFromStateVersion() != 2 || secondDelta.GetToStateVersion() != 3 {
		t.Fatalf("second delta = %+v", secondDelta)
	}
}

func newCoderTestServer(t *testing.T, authorizer Authorizer, helloTimeout time.Duration, register Register) *httptest.Server {
	t.Helper()
	origins := sharedconfig.OriginAllowlist{handlerOrigin}
	acceptor, err := NewAcceptor(origins)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(authorizer, register, acceptor, clock.NewFake(time.Now().UTC()), Config{
		AllowedOrigins: origins, HelloTimeout: helloTimeout, WriteTimeout: time.Second,
		PingInterval: time.Hour, MaxMessageBytes: 4096, QueueCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(handler)
}

func dialCoderTestServer(t *testing.T, server *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx, coderServerURL(server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{origin}},
	})
	if err != nil {
		t.Fatalf("Dial() error=%v response=%v", err, response)
	}
	return connection
}

func coderServerURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func writeHello(t *testing.T, connection *websocket.Conn) {
	t.Helper()
	writeHelloWithTicket(t, connection, "ticket")
}

func writeHelloWithTicket(t *testing.T, connection *websocket.Conn, ticket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := connection.Write(ctx, websocket.MessageBinary, clientFrameBytes(t, &gamev1.ClientFrame{
		Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{Ticket: []byte(ticket), Grant: []byte("grant")}},
	})); err != nil {
		t.Fatal(err)
	}
}

type sequenceAuthorizer struct {
	mu             sync.Mutex
	authorizations []subscription.Authorization
	calls          int
}

func (authorizer *sequenceAuthorizer) Accept(context.Context, string, []byte, []byte) (subscription.Authorization, error) {
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if authorizer.calls >= len(authorizer.authorizations) {
		return subscription.Authorization{}, subscription.ErrTicketRejected
	}
	result := authorizer.authorizations[authorizer.calls]
	authorizer.calls++
	return result, nil
}

func readServerFrame(t *testing.T, connection *websocket.Conn) *gamev1.ServerFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	kind, payload, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageBinary {
		t.Fatalf("message type = %v", kind)
	}
	frame := &gamev1.ServerFrame{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func assertCloseStatus(t *testing.T, connection *websocket.Conn, expected websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, _, err := connection.Read(ctx)
	if status := websocket.CloseStatus(err); status != expected {
		t.Fatalf("close status=%v error=%v", status, err)
	}
}
