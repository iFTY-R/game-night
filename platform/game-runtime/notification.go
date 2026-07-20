package gameruntime

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/google/uuid"
)

const maximumSessionNotificationBytes = 256

// SessionNotification is the secret-free durable wake-up derived from a committed session version.
type SessionNotification struct {
	SessionID    uuid.UUID
	StateVersion uint64
}

// Valid rejects notifications that cannot identify one authoritative committed cursor.
func (notification SessionNotification) Valid() bool {
	return notification.SessionID != uuid.Nil && notification.StateVersion > 0
}

// MarshalSessionNotification emits the stable camel-case outbox payload used by every session event type.
func MarshalSessionNotification(notification SessionNotification) ([]byte, error) {
	if !notification.Valid() {
		return nil, ErrInvalidSessionInput
	}
	return json.Marshal(struct {
		SessionID    string `json:"sessionId"`
		StateVersion uint64 `json:"stateVersion"`
	}{SessionID: notification.SessionID.String(), StateVersion: notification.StateVersion})
}

// ParseSessionNotification rejects unknown fields, trailing values, and non-canonical identifiers before Redis publish.
func ParseSessionNotification(payload []byte) (SessionNotification, error) {
	if len(payload) == 0 || len(payload) > maximumSessionNotificationBytes {
		return SessionNotification{}, ErrInvalidSessionInput
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire struct {
		SessionID    string `json:"sessionId"`
		StateVersion uint64 `json:"stateVersion"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return SessionNotification{}, ErrInvalidSessionInput
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return SessionNotification{}, ErrInvalidSessionInput
	}
	sessionID, err := uuid.Parse(wire.SessionID)
	if err != nil || sessionID == uuid.Nil || sessionID.String() != wire.SessionID || wire.StateVersion == 0 {
		return SessionNotification{}, ErrInvalidSessionInput
	}
	return SessionNotification{SessionID: sessionID, StateVersion: wire.StateVersion}, nil
}
