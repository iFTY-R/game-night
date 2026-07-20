package gamewebsocket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	game "github.com/iFTY-R/game-night/sdk/go/game"
	"google.golang.org/protobuf/proto"
)

func TestConnectionSinkWritesUpdatesInCursorOrder(t *testing.T) {
	connection := newFakeSocketWriter()
	authorization := wireAuthorization(game.ViewerPlayer)
	sink := newTestConnectionSink(t, connection, authorization, 4)
	defer sink.Close(context.Canceled)

	first := wireUpdate(authorization, 2)
	first.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}
	second := wireUpdate(authorization, 3)
	second.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}
	if err := sink.Send(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(t.Context(), second); err != nil {
		t.Fatal(err)
	}

	frames := connection.waitForWrites(t, 2)
	if frames[0].GetDelta().GetFromStateVersion() != 1 || frames[0].GetDelta().GetToStateVersion() != 2 ||
		frames[1].GetDelta().GetFromStateVersion() != 2 || frames[1].GetDelta().GetToStateVersion() != 3 {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestConnectionSinkRejectsSlowClientWithoutGrowingQueue(t *testing.T) {
	connection := newFakeSocketWriter()
	connection.blockWrites = make(chan struct{})
	authorization := wireAuthorization(game.ViewerPlayer)
	sink := newTestConnectionSink(t, connection, authorization, 1)

	first := wireUpdate(authorization, 2)
	first.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}
	second := wireUpdate(authorization, 3)
	second.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}
	third := wireUpdate(authorization, 4)
	third.Delta = game.EventProjection{Messages: []game.Message{{MessageType: "viewer.delta", SchemaVersion: 1}}}
	if err := sink.Send(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	connection.waitForWriteStart(t)
	if err := sink.Send(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(t.Context(), third); !errors.Is(err, ErrSlowClient) {
		t.Fatalf("Send() error = %v", err)
	}
	close(connection.blockWrites)
	sink.Close(ErrSlowClient)
	if connection.closeCode() != closePolicyViolation {
		t.Fatalf("close code = %d", connection.closeCode())
	}
}

func TestConnectionSinkSendsDrainingBeforeServiceRestart(t *testing.T) {
	connection := newFakeSocketWriter()
	authorization := wireAuthorization(game.ViewerSpectator)
	sink := newTestConnectionSink(t, connection, authorization, 1)
	sink.Close(subscription.ErrHubClosed)

	frames := connection.frames(t)
	if len(frames) != 1 || frames[0].GetDraining().GetReason() != "service_restart" ||
		frames[0].GetDraining().GetReconnectAfter() == nil || connection.closeCode() != closeServiceRestart {
		t.Fatalf("frames=%+v close=%d", frames, connection.closeCode())
	}
}

func TestConnectionSinkClosesOnPingFailure(t *testing.T) {
	connection := newFakeSocketWriter()
	connection.pingErr = errors.New("pong timeout")
	authorization := wireAuthorization(game.ViewerPlayer)
	sink, err := newConnectionSink(t.Context(), connection, authorization, clock.NewFake(time.Now().UTC()), sinkConfig{
		WriteTimeout: time.Second, PingInterval: time.Millisecond, QueueCapacity: 1, MaxMessageBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.done:
	case <-time.After(time.Second):
		t.Fatal("sink did not close after ping failure")
	}
	if connection.closeCode() != closeInternalError {
		t.Fatalf("close code = %d", connection.closeCode())
	}
}

func newTestConnectionSink(
	t *testing.T,
	connection *fakeSocketWriter,
	authorization subscription.Authorization,
	capacity int,
) *connectionSink {
	t.Helper()
	sink, err := newConnectionSink(t.Context(), connection, authorization, clock.NewFake(time.Now().UTC()), sinkConfig{
		WriteTimeout: time.Second, PingInterval: time.Hour, QueueCapacity: capacity, MaxMessageBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

type fakeSocketWriter struct {
	mu          sync.Mutex
	writes      [][]byte
	writeStart  chan struct{}
	startOnce   sync.Once
	blockWrites chan struct{}
	pingErr     error
	closedCode  int
}

func newFakeSocketWriter() *fakeSocketWriter {
	return &fakeSocketWriter{writeStart: make(chan struct{})}
}

func (writer *fakeSocketWriter) WriteBinary(ctx context.Context, payload []byte) error {
	writer.startOnce.Do(func() { close(writer.writeStart) })
	if writer.blockWrites != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-writer.blockWrites:
		}
	}
	writer.mu.Lock()
	writer.writes = append(writer.writes, append([]byte(nil), payload...))
	writer.mu.Unlock()
	return nil
}

func (writer *fakeSocketWriter) Ping(context.Context) error { return writer.pingErr }

func (writer *fakeSocketWriter) Close(code int, _ string) error {
	writer.mu.Lock()
	writer.closedCode = code
	writer.mu.Unlock()
	return nil
}

func (writer *fakeSocketWriter) waitForWriteStart(t *testing.T) {
	t.Helper()
	select {
	case <-writer.writeStart:
	case <-time.After(time.Second):
		t.Fatal("socket write did not start")
	}
}

func (writer *fakeSocketWriter) waitForWrites(t *testing.T, count int) []*gamev1.ServerFrame {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		frames := writer.frames(t)
		if len(frames) >= count {
			return frames
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("socket writes did not reach %d", count)
	return nil
}

func (writer *fakeSocketWriter) frames(t *testing.T) []*gamev1.ServerFrame {
	t.Helper()
	writer.mu.Lock()
	writes := make([][]byte, len(writer.writes))
	for index, raw := range writer.writes {
		writes[index] = append([]byte(nil), raw...)
	}
	writer.mu.Unlock()
	frames := make([]*gamev1.ServerFrame, len(writes))
	for index, raw := range writes {
		frame := &gamev1.ServerFrame{}
		if err := proto.Unmarshal(raw, frame); err != nil {
			t.Fatal(err)
		}
		frames[index] = frame
	}
	return frames
}

func (writer *fakeSocketWriter) closeCode() int {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.closedCode
}
