package gamewebsocket

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/platform/clock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	closeNormal          = 1000
	closePolicyViolation = 1008
	closeInternalError   = 1011
	closeServiceRestart  = 1012
	reconnectDelay       = time.Second
)

var (
	ErrInvalidSinkConfig = errors.New("invalid realtime WebSocket sink configuration")
	ErrSlowClient        = errors.New("realtime WebSocket client is too slow")
	ErrFrameTooLarge     = errors.New("realtime WebSocket frame is too large")
)

// socketWriter is the narrow connection surface needed by the bounded writer and is implemented by the transport adapter.
type socketWriter interface {
	WriteBinary(context.Context, []byte) error
	Ping(context.Context) error
	Close(int, string) error
}

// sinkConfig bounds every queued frame, socket operation, and liveness check for one connection.
type sinkConfig struct {
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	QueueCapacity   int
	MaxMessageBytes int64
}

type outboundFrame struct {
	payload []byte
}

// connectionSink is the only goroutine allowed to write to one socket; producers enqueue immutable protobuf bytes.
type connectionSink struct {
	connection    socketWriter
	authorization subscription.Authorization
	clock         clock.Clock
	config        sinkConfig
	queue         chan outboundFrame

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	cursorMu  sync.Mutex
	cursor    uint64
	closeOnce sync.Once
}

// newConnectionSink starts the serialized writer before the Hub can publish its first update.
func newConnectionSink(
	parent context.Context,
	connection socketWriter,
	authorization subscription.Authorization,
	source clock.Clock,
	config sinkConfig,
) (*connectionSink, error) {
	if parent == nil || connection == nil || source == nil || !authorization.Viewer.Valid() || authorization.Cursor == 0 ||
		config.WriteTimeout < time.Millisecond || config.PingInterval < time.Millisecond || config.QueueCapacity < 1 ||
		config.MaxMessageBytes < 1 {
		return nil, ErrInvalidSinkConfig
	}
	ctx, cancel := context.WithCancelCause(parent)
	sink := &connectionSink{
		connection: connection, authorization: authorization, clock: source, config: config,
		queue: make(chan outboundFrame, config.QueueCapacity), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), cursor: authorization.Cursor,
	}
	go sink.run()
	return sink, nil
}

// Send converts one viewer-scoped Hub update and fails immediately when the bounded client queue is full.
func (sink *connectionSink) Send(ctx context.Context, update subscription.Update) error {
	if sink == nil || ctx == nil {
		return ErrInvalidUpdate
	}
	sink.cursorMu.Lock()
	defer sink.cursorMu.Unlock()
	frame, err := serverFrameForUpdate(update, sink.authorization, sink.cursor)
	if err != nil {
		return err
	}
	payload, err := marshalServerFrame(frame)
	if err != nil {
		return err
	}
	if int64(len(payload)) > sink.config.MaxMessageBytes {
		return ErrFrameTooLarge
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sink.ctx.Done():
		return context.Cause(sink.ctx)
	case sink.queue <- outboundFrame{payload: payload}:
		sink.cursor = update.StateVersion
		return nil
	default:
		return ErrSlowClient
	}
}

// SendPong serializes application-level ping replies through the same bounded writer as projection updates.
func (sink *connectionSink) SendPong(ctx context.Context, nonce uint64) error {
	frame := &gamev1.ServerFrame{Body: &gamev1.ServerFrame_Pong{Pong: &gamev1.ServerPong{Nonce: nonce}}}
	return sink.enqueueFrame(ctx, frame)
}

// Close terminates the writer once and waits until its final protocol frame and close code have been attempted.
func (sink *connectionSink) Close(cause error) {
	if sink == nil {
		return
	}
	if cause == nil {
		cause = context.Canceled
	}
	sink.closeOnce.Do(func() { sink.cancel(cause) })
	<-sink.done
}

func (sink *connectionSink) enqueueFrame(ctx context.Context, frame *gamev1.ServerFrame) error {
	if sink == nil || ctx == nil {
		return ErrInvalidUpdate
	}
	payload, err := marshalServerFrame(frame)
	if err != nil {
		return err
	}
	if int64(len(payload)) > sink.config.MaxMessageBytes {
		return ErrFrameTooLarge
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-sink.ctx.Done():
		return context.Cause(sink.ctx)
	case sink.queue <- outboundFrame{payload: payload}:
		return nil
	default:
		return ErrSlowClient
	}
}

func (sink *connectionSink) run() {
	defer close(sink.done)
	defer func() { sink.finish(context.Cause(sink.ctx)) }()
	ticker := time.NewTicker(sink.config.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sink.ctx.Done():
			return
		case frame := <-sink.queue:
			if err := sink.write(frame.payload); err != nil {
				sink.cancel(err)
				return
			}
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), sink.config.WriteTimeout)
			err := sink.connection.Ping(ctx)
			cancel()
			if err != nil {
				sink.cancel(err)
				return
			}
		}
	}
}

func (sink *connectionSink) write(payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), sink.config.WriteTimeout)
	defer cancel()
	return sink.connection.WriteBinary(ctx, payload)
}

func (sink *connectionSink) finish(cause error) {
	code, reason, finalFrame := closeSemantics(cause, sink.clock.Now().Round(0).UTC())
	if finalFrame != nil {
		if payload, err := marshalServerFrame(finalFrame); err == nil && int64(len(payload)) <= sink.config.MaxMessageBytes {
			_ = sink.write(payload)
		}
	}
	_ = sink.connection.Close(code, reason)
}

func closeSemantics(cause error, now time.Time) (int, string, *gamev1.ServerFrame) {
	switch {
	case errors.Is(cause, subscription.ErrHubClosed):
		frame := &gamev1.ServerFrame{Body: &gamev1.ServerFrame_Draining{Draining: &gamev1.SubscriptionDraining{
			Reason: "service_restart", ReconnectAfter: timestamppb.New(now.Add(reconnectDelay)),
		}}}
		return closeServiceRestart, "service restart", frame
	case errors.Is(cause, subscription.ErrUnauthorized):
		return closePolicyViolation, "subscription unauthorized", errorFrame("subscription_unauthorized")
	case errors.Is(cause, subscription.ErrAuthorizationChanged):
		return closePolicyViolation, "subscription changed", errorFrame("subscription_changed")
	case errors.Is(cause, ErrSlowClient):
		return closePolicyViolation, "client too slow", nil
	case errors.Is(cause, ErrInvalidClientFrame), errors.Is(cause, ErrFrameTooLarge):
		return closePolicyViolation, "invalid client frame", errorFrame("invalid_frame")
	case cause == nil, errors.Is(cause, context.Canceled):
		return closeNormal, "", nil
	default:
		return closeInternalError, "connection failed", errorFrame("connection_failed")
	}
}

func errorFrame(code string) *gamev1.ServerFrame {
	return &gamev1.ServerFrame{Body: &gamev1.ServerFrame_Error{Error: &gamev1.SubscriptionError{Code: code}}}
}
