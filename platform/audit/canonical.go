package audit

import (
	"bytes"

	auditv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/audit/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

func canonicalEvent(snapshot EventSnapshot) ([]byte, error) {
	message := &auditv1.AuditEvent{
		SchemaVersion: snapshot.SchemaVersion,
		ChainId:       string(snapshot.ChainID),
		EventId:       snapshot.EventID.String(),
		Sequence:      snapshot.Sequence,
		PreviousHash:  snapshot.PreviousHash.Bytes(),
		RequestId:     snapshot.RequestID,
		OccurredAt:    timestamppb.New(snapshot.OccurredAt),
		Actor: &auditv1.AuditActor{
			Type:    auditv1.AuditActorType(snapshot.Actor.Type()),
			ActorId: snapshot.Actor.ID(),
		},
		Target: &auditv1.AuditTarget{
			Type:     auditv1.AuditTargetType(snapshot.Target.Type()),
			TargetId: snapshot.Target.ID(),
		},
		Action:            auditv1.AuditAction(snapshot.Action),
		ReasonCode:        snapshot.ReasonCode,
		DetailDigest:      bytes.Clone(snapshot.DetailDigest),
		SigningKeyVersion: snapshot.SigningKeyVersion,
	}
	if err := message.OccurredAt.CheckValid(); err != nil {
		return nil, ErrInvalidInput
	}
	encoded, err := deterministicMarshal.Marshal(message)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return encoded, nil
}

func canonicalUnsignedCheckpoint(snapshot CheckpointSnapshot) ([]byte, error) {
	message := checkpointProto(snapshot, nil)
	if err := message.CreatedAt.CheckValid(); err != nil {
		return nil, ErrInvalidInput
	}
	encoded, err := deterministicMarshal.Marshal(message)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return encoded, nil
}

func checkpointProto(snapshot CheckpointSnapshot, signature []byte) *auditv1.AuditCheckpoint {
	return &auditv1.AuditCheckpoint{
		ChainId:             string(snapshot.ChainID),
		Sequence:            snapshot.Sequence,
		ChainHash:           snapshot.ChainHash.Bytes(),
		CheckpointSignature: bytes.Clone(signature),
		SigningKeyVersion:   snapshot.SigningKeyVersion,
		CreatedAt:           timestamppb.New(snapshot.CreatedAt),
	}
}
