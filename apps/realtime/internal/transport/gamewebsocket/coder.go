package gamewebsocket

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

// coderAcceptor keeps RFC 6455 framing isolated from authorization, projection, and queue policy.
type coderAcceptor struct {
	options websocket.AcceptOptions
}

// NewAcceptor disables compression and accepts the request Origin policy owned by the handler.
// An optional allowlist remains available for focused tests and older embedders.
func NewAcceptor(allowlist ...sharedconfig.OriginAllowlist) (Acceptor, error) {
	var allowedOrigins sharedconfig.OriginAllowlist
	if len(allowlist) > 0 {
		allowedOrigins = allowlist[0]
	}
	patterns := make([]string, 0, len(allowedOrigins))
	seen := make(map[string]struct{}, len(allowedOrigins))
	for _, configured := range allowedOrigins {
		pattern := string(configured)
		if pattern == "" {
			return nil, ErrInvalidSinkConfig
		}
		if _, duplicate := seen[pattern]; duplicate {
			return nil, ErrInvalidSinkConfig
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	return &coderAcceptor{options: websocket.AcceptOptions{
		OriginPatterns: patterns, CompressionMode: websocket.CompressionDisabled,
	}}, nil
}

func (acceptor *coderAcceptor) Accept(response http.ResponseWriter, request *http.Request) (socketConnection, error) {
	connection, err := websocket.Accept(response, request, &acceptor.options)
	if err != nil {
		return nil, err
	}
	return &coderConnection{connection: connection}, nil
}

type coderConnection struct {
	connection *websocket.Conn
}

func (connection *coderConnection) Read(ctx context.Context) (messageType, []byte, error) {
	kind, payload, err := connection.connection.Read(ctx)
	return messageType(kind), payload, err
}

func (connection *coderConnection) SetReadLimit(limit int64) {
	connection.connection.SetReadLimit(limit)
}

func (connection *coderConnection) WriteBinary(ctx context.Context, payload []byte) error {
	return connection.connection.Write(ctx, websocket.MessageBinary, payload)
}

func (connection *coderConnection) Ping(ctx context.Context) error {
	return connection.connection.Ping(ctx)
}

func (connection *coderConnection) Close(code int, reason string) error {
	return connection.connection.Close(websocket.StatusCode(code), reason)
}
