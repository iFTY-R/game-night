package gamewebsocket

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	"google.golang.org/protobuf/proto"
)

const handlerOrigin = "https://play.game-night.test"

func TestHandlerRejectsOriginBeforeUpgrade(t *testing.T) {
	fixture := newHandlerFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/realtime/game", nil)
	request.Header.Set("Origin", "https://admin.game-night.test")
	response := httptest.NewRecorder()

	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || fixture.acceptor.calls != 0 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d accepts=%d headers=%v", response.Code, fixture.acceptor.calls, response.Header())
	}
}

func TestHandlerConsumesBinaryHelloAndRepliesToClientPing(t *testing.T) {
	fixture := newHandlerFixture(t)
	fixture.connection.reads = []fakeSocketRead{
		{kind: binaryMessage, payload: clientFrameBytes(t, &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{
			Ticket: []byte("ticket"), Grant: []byte("grant"),
		}}})},
		{kind: binaryMessage, payload: clientFrameBytes(t, &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Ping{Ping: &gamev1.ClientPing{Nonce: 42}}})},
		{err: io.EOF, waitForWrite: true},
	}
	request := handlerRequest(t)
	fixture.handler.ServeHTTP(httptest.NewRecorder(), request)

	if fixture.authorizer.calls != 1 || fixture.authorizer.origin != handlerOrigin || fixture.registerCalls != 1 ||
		fixture.connection.readLimit != fixture.handler.config.MaxMessageBytes {
		t.Fatalf("authorizations=%d origin=%q registrations=%d limit=%d", fixture.authorizer.calls, fixture.authorizer.origin, fixture.registerCalls, fixture.connection.readLimit)
	}
	frames := fixture.connection.frames(t)
	if len(frames) != 1 || frames[0].GetPong().GetNonce() != 42 || fixture.connection.closeCode() != closeNormal {
		t.Fatalf("frames=%+v close=%d", frames, fixture.connection.closeCode())
	}
}

func TestHandlerRejectsTicketReplayWithoutRegistering(t *testing.T) {
	fixture := newHandlerFixture(t)
	fixture.authorizer.err = subscription.ErrTicketRejected
	fixture.connection.reads = []fakeSocketRead{{
		kind: binaryMessage,
		payload: clientFrameBytes(t, &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{
			Ticket: []byte("replayed"), Grant: []byte("grant"),
		}}}),
	}}
	fixture.handler.ServeHTTP(httptest.NewRecorder(), handlerRequest(t))

	frames := fixture.connection.frames(t)
	if fixture.registerCalls != 0 || len(frames) != 1 || frames[0].GetError().GetCode() != "subscription_rejected" ||
		fixture.connection.closeCode() != closePolicyViolation {
		t.Fatalf("registrations=%d frames=%+v close=%d", fixture.registerCalls, frames, fixture.connection.closeCode())
	}
}

func TestHandlerRejectsTextAndRepeatedHelloFrames(t *testing.T) {
	for name, reads := range map[string][]fakeSocketRead{
		"text hello": {{kind: 1, payload: []byte("hello")}},
		"repeated hello": {
			{kind: binaryMessage, payload: clientFrameBytes(t, &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{
				Ticket: []byte("ticket"), Grant: []byte("grant"),
			}}})},
			{kind: binaryMessage, payload: clientFrameBytes(t, &gamev1.ClientFrame{Body: &gamev1.ClientFrame_Hello{Hello: &gamev1.SubscriptionHello{
				Ticket: []byte("second"), Grant: []byte("grant"),
			}}})},
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newHandlerFixture(t)
			fixture.connection.reads = reads
			fixture.handler.ServeHTTP(httptest.NewRecorder(), handlerRequest(t))
			if fixture.connection.closeCode() != closePolicyViolation {
				t.Fatalf("close code = %d", fixture.connection.closeCode())
			}
		})
	}
}

func TestHandlerEnforcesHelloTimeout(t *testing.T) {
	fixture := newHandlerFixture(t)
	fixture.handler.config.HelloTimeout = time.Millisecond
	fixture.connection.waitForContext = true
	fixture.handler.ServeHTTP(httptest.NewRecorder(), handlerRequest(t))
	if fixture.authorizer.calls != 0 || fixture.connection.closeCode() != closePolicyViolation {
		t.Fatalf("authorizations=%d close=%d", fixture.authorizer.calls, fixture.connection.closeCode())
	}
}

type handlerFixture struct {
	handler       *Handler
	connection    *fakeSocketConnection
	acceptor      *fakeAcceptor
	authorizer    *fakeHandlerAuthorizer
	registerCalls int
}

func newHandlerFixture(t *testing.T) *handlerFixture {
	t.Helper()
	fixture := &handlerFixture{
		connection: &fakeSocketConnection{fakeSocketWriter: *newFakeSocketWriter()},
		authorizer: &fakeHandlerAuthorizer{authorization: wireAuthorization("player")},
	}
	fixture.acceptor = &fakeAcceptor{connection: fixture.connection}
	register := func(_ subscription.Authorization, sink subscription.Sink) (Handle, error) {
		fixture.registerCalls++
		return &fakeHandlerHandle{close: sink.Close}, nil
	}
	handler, err := NewHandler(fixture.authorizer, register, fixture.acceptor, clock.NewFake(time.Now().UTC()), Config{
		AllowedOrigins: sharedconfig.OriginAllowlist{handlerOrigin}, HelloTimeout: time.Second,
		WriteTimeout: time.Second, PingInterval: time.Hour, MaxMessageBytes: 4096, QueueCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler = handler
	return fixture
}

func handlerRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/realtime/game", nil)
	request.Header.Set("Origin", handlerOrigin)
	return request
}

func clientFrameBytes(t *testing.T, frame *gamev1.ClientFrame) []byte {
	t.Helper()
	value, err := (proto.MarshalOptions{Deterministic: true}).Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type fakeAcceptor struct {
	connection socketConnection
	calls      int
}

func (acceptor *fakeAcceptor) Accept(http.ResponseWriter, *http.Request) (socketConnection, error) {
	acceptor.calls++
	return acceptor.connection, nil
}

type fakeHandlerAuthorizer struct {
	authorization subscription.Authorization
	err           error
	calls         int
	origin        string
}

func (authorizer *fakeHandlerAuthorizer) Accept(_ context.Context, origin string, _, _ []byte) (subscription.Authorization, error) {
	authorizer.calls++
	authorizer.origin = origin
	return authorizer.authorization, authorizer.err
}

type fakeHandlerHandle struct {
	once  sync.Once
	close func(error)
}

func (handle *fakeHandlerHandle) Close(cause error) {
	handle.once.Do(func() { handle.close(cause) })
}

type fakeSocketRead struct {
	kind         messageType
	payload      []byte
	err          error
	waitForWrite bool
}

type fakeSocketConnection struct {
	fakeSocketWriter
	reads          []fakeSocketRead
	readIndex      int
	readLimit      int64
	waitForContext bool
}

func (connection *fakeSocketConnection) Read(ctx context.Context) (messageType, []byte, error) {
	if connection.waitForContext {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	if connection.readIndex >= len(connection.reads) {
		return 0, nil, io.EOF
	}
	result := connection.reads[connection.readIndex]
	connection.readIndex++
	if result.waitForWrite {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-connection.writeStart:
		}
	}
	return result.kind, append([]byte(nil), result.payload...), result.err
}

func (connection *fakeSocketConnection) SetReadLimit(limit int64) { connection.readLimit = limit }

func (connection *fakeSocketConnection) Ping(context.Context) error {
	return connection.fakeSocketWriter.pingErr
}

func (connection *fakeSocketConnection) Close(code int, reason string) error {
	return connection.fakeSocketWriter.Close(code, reason)
}

func (connection *fakeSocketConnection) WriteBinary(ctx context.Context, payload []byte) error {
	return connection.fakeSocketWriter.WriteBinary(ctx, payload)
}
