package room

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/outbox"
)

func TestParticipantRevokedEventRoundTripsCanonicalFact(t *testing.T) {
	fact := participantRevocationFactFixture()
	event, err := NewParticipantRevokedEvent(fact)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseParticipantRevokedEvent(event)
	if err != nil || parsed != fact {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
}

func TestParticipantRevokedEventRejectsMetadataAndPayloadDrift(t *testing.T) {
	fact := participantRevocationFactFixture()
	event, err := NewParticipantRevokedEvent(fact)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := event.Snapshot()
	tests := []struct {
		name   string
		mutate func(*outbox.EventSnapshot)
	}{
		{name: "event id", mutate: func(value *outbox.EventSnapshot) { value.ID = uuid.New() }},
		{name: "session", mutate: func(value *outbox.EventSnapshot) { value.AggregateID = uuid.New() }},
		{name: "unknown field", mutate: func(value *outbox.EventSnapshot) {
			value.Payload = bytes.Replace(value.Payload, []byte("}"), []byte(",\"extra\":true}"), 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := snapshot
			changed.Payload = bytes.Clone(snapshot.Payload)
			test.mutate(&changed)
			changed.Sequence = 1
			restored, restoreErr := outbox.RestoreEvent(changed)
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if _, parseErr := ParseParticipantRevokedEvent(restored); parseErr == nil {
				t.Fatal("drifted participant revocation accepted")
			}
		})
	}
}

func participantRevocationFactFixture() ParticipantRevocationFact {
	return ParticipantRevocationFact{
		EventID: uuid.New(), RoomID: uuid.New(), SessionID: uuid.New(), UserID: uuid.New(),
		ActorKind: RemovalActorHost, ActorID: uuid.New(), Reason: RemovalReasonHostRemoved,
		MembershipVersion: 4, OccurredAt: time.Date(2026, time.July, 21, 12, 0, 0, 123000000, time.UTC),
	}
}
