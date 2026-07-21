package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/outbox"
)

const (
	// ParticipantRevokedEventType is the durable room fact consumed by the game runtime.
	ParticipantRevokedEventType outbox.EventType = "room.participant.revoked.v1"
	// RoomSessionAggregateType scopes a revocation to the frozen session affected by membership removal.
	RoomSessionAggregateType outbox.AggregateType = "room.session"
	// participantRevocationSchemaVersion protects strict consumers from silently accepting incompatible payloads.
	participantRevocationSchemaVersion uint32 = 1
	// maximumParticipantRevocationPayloadBytes bounds strict JSON decoding independently from the shared outbox limit.
	maximumParticipantRevocationPayloadBytes = 1 << 12
)

// RemovalActorKind identifies the authority that committed a room membership revocation.
type RemovalActorKind string

const (
	RemovalActorHost  RemovalActorKind = "host"
	RemovalActorAdmin RemovalActorKind = "admin"
)

// RemovalReason is a stable governance reason suitable for audit and replay correlation.
type RemovalReason string

const (
	RemovalReasonHostRemoved      RemovalReason = "host_removed"
	RemovalReasonRoomBanned       RemovalReason = "room_banned"
	RemovalReasonAdminRemoved     RemovalReason = "admin_removed"
	RemovalReasonAccountSuspended RemovalReason = "account_suspended"
)

// ParticipantRevocationFact contains no game-private state and is safe for the durable shared outbox.
type ParticipantRevocationFact struct {
	EventID           uuid.UUID
	RoomID            uuid.UUID
	SessionID         uuid.UUID
	UserID            uuid.UUID
	ActorKind         RemovalActorKind
	ActorID           uuid.UUID
	Reason            RemovalReason
	MembershipVersion uint64
	OccurredAt        time.Time
}

// Valid binds the governance reason to the matching actor class and one committed membership version.
func (fact ParticipantRevocationFact) Valid() bool {
	fact.OccurredAt = canonicalRoomTime(fact.OccurredAt)
	if fact.EventID == uuid.Nil || fact.RoomID == uuid.Nil || fact.SessionID == uuid.Nil || fact.UserID == uuid.Nil ||
		fact.ActorID == uuid.Nil || fact.MembershipVersion == 0 || fact.OccurredAt.IsZero() {
		return false
	}
	switch fact.ActorKind {
	case RemovalActorHost:
		return fact.Reason == RemovalReasonHostRemoved || fact.Reason == RemovalReasonRoomBanned
	case RemovalActorAdmin:
		return fact.Reason == RemovalReasonAdminRemoved || fact.Reason == RemovalReasonAccountSuspended
	default:
		return false
	}
}

// NewParticipantRevokedEvent creates one canonical room-outbox event for a committed active participant removal.
func NewParticipantRevokedEvent(fact ParticipantRevocationFact) (outbox.Event, error) {
	fact.OccurredAt = canonicalRoomTime(fact.OccurredAt)
	if !fact.Valid() {
		return outbox.Event{}, ErrInvalidRoomInput
	}
	payload, err := marshalParticipantRevocation(fact)
	if err != nil {
		return outbox.Event{}, err
	}
	event, err := outbox.NewEvent(
		fact.EventID, ParticipantRevokedEventType, RoomSessionAggregateType, fact.SessionID,
		payload, fact.OccurredAt, fact.OccurredAt,
	)
	if err != nil {
		return outbox.Event{}, ErrInvalidRoomInput
	}
	return event, nil
}

// ParseParticipantRevokedEvent validates metadata and canonical payload bytes before runtime delivery.
func ParseParticipantRevokedEvent(event outbox.Event) (ParticipantRevocationFact, error) {
	snapshot := event.Snapshot()
	if snapshot.Type != ParticipantRevokedEventType || snapshot.AggregateType != RoomSessionAggregateType ||
		snapshot.AggregateID == uuid.Nil || snapshot.ID == uuid.Nil || len(snapshot.Payload) > maximumParticipantRevocationPayloadBytes {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Payload))
	decoder.DisallowUnknownFields()
	var wire participantRevocationWire
	if err := decoder.Decode(&wire); err != nil {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	fact, err := wire.fact()
	if err != nil || fact.EventID != snapshot.ID || fact.SessionID != snapshot.AggregateID || !fact.OccurredAt.Equal(snapshot.CreatedAt) {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	canonical, err := marshalParticipantRevocation(fact)
	if err != nil || !bytes.Equal(canonical, snapshot.Payload) {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	return fact, nil
}

type participantRevocationWire struct {
	SchemaVersion     uint32 `json:"schemaVersion"`
	EventID           string `json:"eventId"`
	RoomID            string `json:"roomId"`
	SessionID         string `json:"sessionId"`
	UserID            string `json:"userId"`
	ActorKind         string `json:"actorKind"`
	ActorID           string `json:"actorId"`
	Reason            string `json:"reason"`
	MembershipVersion uint64 `json:"membershipVersion"`
	OccurredAt        string `json:"occurredAt"`
}

func marshalParticipantRevocation(fact ParticipantRevocationFact) ([]byte, error) {
	if !fact.Valid() {
		return nil, ErrInvalidRoomInput
	}
	return json.Marshal(participantRevocationWire{
		SchemaVersion: participantRevocationSchemaVersion,
		EventID:       fact.EventID.String(), RoomID: fact.RoomID.String(), SessionID: fact.SessionID.String(),
		UserID: fact.UserID.String(), ActorKind: string(fact.ActorKind), ActorID: fact.ActorID.String(),
		Reason: string(fact.Reason), MembershipVersion: fact.MembershipVersion,
		OccurredAt: fact.OccurredAt.Format(time.RFC3339Nano),
	})
}

func (wire participantRevocationWire) fact() (ParticipantRevocationFact, error) {
	if wire.SchemaVersion != participantRevocationSchemaVersion {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	eventID, eventErr := parseCanonicalUUID(wire.EventID)
	roomID, roomErr := parseCanonicalUUID(wire.RoomID)
	sessionID, sessionErr := parseCanonicalUUID(wire.SessionID)
	userID, userErr := parseCanonicalUUID(wire.UserID)
	actorID, actorErr := parseCanonicalUUID(wire.ActorID)
	occurredAt, timeErr := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	fact := ParticipantRevocationFact{
		EventID: eventID, RoomID: roomID, SessionID: sessionID, UserID: userID,
		ActorKind: RemovalActorKind(wire.ActorKind), ActorID: actorID, Reason: RemovalReason(wire.Reason),
		MembershipVersion: wire.MembershipVersion, OccurredAt: canonicalRoomTime(occurredAt),
	}
	if eventErr != nil || roomErr != nil || sessionErr != nil || userErr != nil || actorErr != nil || timeErr != nil ||
		fact.OccurredAt.Format(time.RFC3339Nano) != wire.OccurredAt || !fact.Valid() {
		return ParticipantRevocationFact{}, ErrRoomIntegrity
	}
	return fact, nil
}

func parseCanonicalUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, fmt.Errorf("invalid canonical uuid")
	}
	return parsed, nil
}
