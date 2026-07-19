package outbox

import (
	"bytes"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIdentityEventRoutingValuesAreCanonical(t *testing.T) {
	for _, eventType := range []EventType{
		EventTypeIdentityRecoveryCompleted,
		EventTypeIdentityDeviceRevoked,
		EventTypeIdentityUserSuspended,
		EventTypeIdentityUserUnsuspended,
		EventTypeIdentityUserDeleted,
	} {
		if !eventType.Valid() {
			t.Fatalf("invalid identity event type %q", eventType)
		}
	}
	for _, aggregateType := range []AggregateType{
		AggregateTypeIdentityUser,
		AggregateTypeIdentityDevice,
	} {
		if !aggregateType.Valid() {
			t.Fatalf("invalid identity aggregate type %q", aggregateType)
		}
	}
}

func TestIdentityRecoveryCompletedPayloadIsDeterministicAndSecretFree(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	revokedDeviceIDs := []string{uuid.NewString(), uuid.NewString()}
	sort.Strings(revokedDeviceIDs)
	message := &identityv1.IdentityRecoveryCompletedEvent{
		SchemaVersion:              1,
		EventId:                    uuid.NewString(),
		RequestId:                  "request-recovery-1",
		OccurredAt:                 timestamppb.New(now),
		UserId:                     uuid.NewString(),
		NewDeviceCredentialId:      uuid.NewString(),
		NewRecoveryCredentialId:    uuid.NewString(),
		ResultId:                   uuid.NewString(),
		Source:                     identityv1.IdentityRecoverySource_IDENTITY_RECOVERY_SOURCE_RECOVERY_CODE,
		DevicePolicy:               identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_REVOKE_OTHER_DEVICES,
		RevokedDeviceCredentialIds: revokedDeviceIDs,
		RevokedAssistedGrantCount:  1,
	}
	first, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("deterministic identity recovery payload changed between serializations")
	}
	if _, err := NewEvent(
		uuid.New(), EventTypeIdentityRecoveryCompleted, AggregateTypeIdentityUser,
		uuid.MustParse(message.UserId), first, now, now,
	); err != nil {
		t.Fatalf("construct recovery outbox event: %v", err)
	}
	assertIdentityEventDescriptorHasNoSecrets(t, message.ProtoReflect().Descriptor())
}

func TestIdentityDeviceRevokedPayloadIsSecretFree(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	message := &identityv1.IdentityDeviceRevokedEvent{
		SchemaVersion:      1,
		EventId:            uuid.NewString(),
		CauseEventId:       uuid.NewString(),
		RequestId:          "request-revoke-1",
		OccurredAt:         timestamppb.New(now),
		UserId:             uuid.NewString(),
		DeviceCredentialId: uuid.NewString(),
		Reason:             identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_RECOVERY,
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Fatal("device revocation payload is empty")
	}
	if _, err := NewEvent(
		uuid.New(), EventTypeIdentityDeviceRevoked, AggregateTypeIdentityDevice,
		uuid.MustParse(message.DeviceCredentialId), payload, now, now,
	); err != nil {
		t.Fatalf("construct device revocation outbox event: %v", err)
	}
	assertIdentityEventDescriptorHasNoSecrets(t, message.ProtoReflect().Descriptor())
}

func TestIdentityUserStatusChangedPayloadIsDeterministicAndSecretFree(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	message := &identityv1.IdentityUserStatusChangedEvent{
		SchemaVersion: 1, EventId: uuid.NewString(), RequestId: "request-suspend-1", OccurredAt: timestamppb.New(now),
		UserId: uuid.NewString(), PreviousStatus: identityv1.UserStatus_USER_STATUS_ACTIVE,
		CurrentStatus: identityv1.UserStatus_USER_STATUS_SUSPENDED, ActorAdminId: uuid.NewString(),
	}
	first, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("status event was not deterministic: err=%v", err)
	}
	if _, err := NewEvent(uuid.New(), EventTypeIdentityUserSuspended, AggregateTypeIdentityUser, uuid.MustParse(message.UserId), first, now, now); err != nil {
		t.Fatalf("construct status outbox event: %v", err)
	}
	assertIdentityEventDescriptorHasNoSecrets(t, message.ProtoReflect().Descriptor())
}

func assertIdentityEventDescriptorHasNoSecrets(t testing.TB, descriptor protoreflect.MessageDescriptor) {
	t.Helper()
	forbidden := []string{"selector", "token", "code", "hash", "origin", "ip", "label"}
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		name := strings.ToLower(string(fields.Get(index).Name()))
		for _, fragment := range forbidden {
			if strings.Contains(name, fragment) {
				t.Fatalf("identity outbox field %q contains forbidden secret or request metadata fragment %q", name, fragment)
			}
		}
	}
}
