package gamewebsocket

import (
	"context"
	"errors"
	"net/http"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/realtime/internal/subscription"
	"github.com/iFTY-R/game-night/platform/clock"
)

type messageType uint8

const binaryMessage messageType = 2

// socketConnection adds bounded reads to the writer surface owned by connectionSink.
type socketConnection interface {
	socketWriter
	Read(context.Context) (messageType, []byte, error)
	SetReadLimit(int64)
}

// Acceptor upgrades one already Origin-checked request without introducing cookies or long-lived credentials.
type Acceptor interface {
	Accept(http.ResponseWriter, *http.Request) (socketConnection, error)
}

// Authorizer consumes the exact one-time grant and reloads current PostgreSQL room/session authority.
type Authorizer interface {
	Accept(context.Context, string, []byte, []byte) (subscription.Authorization, error)
}

// Handle removes a registered subscriber and carries the transport close cause to its Sink.
type Handle interface {
	Close(error)
}

// Register attaches one authorized connection to the Hub's viewer-specific projection worker.
type Register func(subscription.Authorization, subscription.Sink) (Handle, error)

// Config bounds pre-authentication work and every connection-owned queue or socket operation.
type Config struct {
	AllowedOrigins  sharedconfig.OriginAllowlist
	HelloTimeout    time.Duration
	WriteTimeout    time.Duration
	PingInterval    time.Duration
	MaxMessageBytes int64
	QueueCapacity   int
}

// Handler owns the public upgrade boundary; all identity and game disclosure decisions remain delegated.
type Handler struct {
	authorizer Authorizer
	register   Register
	acceptor   Acceptor
	clock      clock.Clock
	config     Config
	origins    map[string]struct{}
}

type readResult struct {
	kind    messageType
	payload []byte
	err     error
}

// NewHandler validates the complete public transport graph before it can be mounted.
func NewHandler(authorizer Authorizer, register Register, acceptor Acceptor, source clock.Clock, config Config) (*Handler, error) {
	if authorizer == nil || register == nil || acceptor == nil || source == nil || len(config.AllowedOrigins) == 0 ||
		config.HelloTimeout < time.Millisecond || config.WriteTimeout < time.Millisecond || config.PingInterval < time.Millisecond ||
		config.MaxMessageBytes < 1 || config.QueueCapacity < 1 {
		return nil, ErrInvalidSinkConfig
	}
	origins := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, configured := range config.AllowedOrigins {
		origin := string(configured)
		if origin == "" {
			return nil, ErrInvalidSinkConfig
		}
		if _, duplicate := origins[origin]; duplicate {
			return nil, ErrInvalidSinkConfig
		}
		origins[origin] = struct{}{}
	}
	return &Handler{
		authorizer: authorizer, register: register, acceptor: acceptor, clock: source, config: config, origins: origins,
	}, nil
}

// ServeHTTP accepts exactly one canonical binary hello before registering a viewer-scoped Hub subscription.
func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if handler == nil || request == nil || request.Method != http.MethodGet {
		http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	origin, allowed := handler.requestOrigin(request)
	if !allowed {
		http.Error(response, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	connection, err := handler.acceptor.Accept(response, request)
	if err != nil {
		return
	}
	connection.SetReadLimit(handler.config.MaxMessageBytes)
	// Hijacked connections outlive net/http request ownership and are closed explicitly by the Hub or socket reader.
	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	defer cancelConnection()

	helloCtx, cancelHello := context.WithTimeout(connectionCtx, handler.config.HelloTimeout)
	readCtx, cancelRead := context.WithCancel(connectionCtx)
	readComplete := make(chan readResult, 1)
	go func() {
		kind, payload, readErr := connection.Read(readCtx)
		readComplete <- readResult{kind: kind, payload: payload, err: readErr}
	}()
	var first readResult
	select {
	case first = <-readComplete:
		cancelRead()
	case <-helloCtx.Done():
		handler.reject(connection, ErrInvalidClientFrame)
		cancelRead()
		cancelHello()
		return
	}
	kind, raw, err := first.kind, first.payload, first.err
	if err != nil || kind != binaryMessage || int64(len(raw)) > handler.config.MaxMessageBytes {
		cancelHello()
		handler.reject(connection, ErrInvalidClientFrame)
		return
	}
	frame, err := parseClientFrame(raw)
	if err != nil || frame.GetHello() == nil {
		cancelHello()
		handler.reject(connection, ErrInvalidClientFrame)
		return
	}
	hello := frame.GetHello()
	authorization, err := handler.authorizer.Accept(helloCtx, origin, hello.GetTicket(), hello.GetGrant())
	cancelHello()
	if err != nil {
		handler.reject(connection, err)
		return
	}
	sink, err := newConnectionSink(connectionCtx, connection, authorization, handler.clock, sinkConfig{
		WriteTimeout: handler.config.WriteTimeout, PingInterval: handler.config.PingInterval,
		QueueCapacity: handler.config.QueueCapacity, MaxMessageBytes: handler.config.MaxMessageBytes,
	})
	if err != nil {
		handler.reject(connection, err)
		return
	}
	handle, err := handler.register(authorization, sink)
	if err != nil {
		sink.Close(err)
		return
	}
	defer handle.Close(context.Canceled)

	for {
		kind, raw, err = connection.Read(connectionCtx)
		if err != nil {
			return
		}
		if kind != binaryMessage || int64(len(raw)) > handler.config.MaxMessageBytes {
			handle.Close(ErrInvalidClientFrame)
			return
		}
		frame, err = parseClientFrame(raw)
		if err != nil || frame.GetPing() == nil {
			handle.Close(ErrInvalidClientFrame)
			return
		}
		if err = sink.SendPong(connectionCtx, frame.GetPing().GetNonce()); err != nil {
			handle.Close(err)
			return
		}
	}
}

func (handler *Handler) requestOrigin(request *http.Request) (string, bool) {
	values := request.Header.Values("Origin")
	if len(values) != 1 {
		return "", false
	}
	_, allowed := handler.origins[values[0]]
	return values[0], allowed
}

func (handler *Handler) reject(connection socketConnection, cause error) {
	code := "subscription_rejected"
	closeCode, reason := closePolicyViolation, "subscription rejected"
	if errors.Is(cause, ErrInvalidClientFrame) || errors.Is(cause, ErrFrameTooLarge) {
		code, reason = "invalid_frame", "invalid client frame"
	}
	if payload, err := marshalServerFrame(errorFrame(code)); err == nil && int64(len(payload)) <= handler.config.MaxMessageBytes {
		ctx, cancel := context.WithTimeout(context.Background(), handler.config.WriteTimeout)
		_ = connection.WriteBinary(ctx, payload)
		cancel()
	}
	_ = connection.Close(closeCode, reason)
}
