package audit

import (
	"bytes"
	"crypto/sha256"

	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// eventSignatureDomain prevents an event signature from being replayed as another signed artifact type.
	eventSignatureDomain = "game-night/audit/event/v1\x00"
	// checkpointSignatureDomain separates WORM anchors from event signatures even when canonical bytes collide.
	checkpointSignatureDomain = "game-night/audit/checkpoint/v1\x00"
)

// SigningKeyring is intentionally satisfied by security.AuditKeyring, not symmetric keyrings from other domains.
type SigningKeyring interface {
	ActiveVersion() uint32
	Sign([]byte) (security.AuditSignature, error)
	Verify([]byte, security.AuditSignature) bool
}

// Service constructs and verifies canonical events and checkpoints with one purpose-specific keyring.
type Service struct{ keys SigningKeyring }

// NewService requires explicit active signing authority plus historical signature verification.
func NewService(keys SigningKeyring) (*Service, error) {
	if keys == nil {
		return nil, ErrInvalidInput
	}
	return &Service{keys: keys}, nil
}

// Prepare binds caller facts to the exact expected head before hashing and signing.
func (service *Service) Prepare(head Head, input EventInput) (SignedEvent, error) {
	if !head.ChainID().Valid() || input.validate() != nil {
		return SignedEvent{}, ErrInvalidInput
	}
	occurredAt := canonicalTime(input.OccurredAt)
	if occurredAt.Before(head.UpdatedAt()) {
		return SignedEvent{}, ErrInvalidInput
	}
	keyVersion := service.keys.ActiveVersion()
	if keyVersion == 0 {
		return SignedEvent{}, ErrIntegrity
	}
	event, err := RestoreEvent(EventSnapshot{
		SchemaVersion: SchemaVersion, ChainID: head.ChainID(), EventID: input.EventID,
		Sequence: head.Sequence() + 1, PreviousHash: head.Hash(), RequestID: input.RequestID,
		OccurredAt: occurredAt, Actor: input.Actor, Target: input.Target, Action: input.Action,
		ReasonCode: input.ReasonCode, DetailDigest: input.DetailDigest, SigningKeyVersion: keyVersion,
	})
	if err != nil {
		return SignedEvent{}, err
	}
	canonical, err := canonicalEvent(event.Snapshot())
	if err != nil {
		return SignedEvent{}, err
	}
	signature, err := service.keys.Sign(eventSigningPayload(canonical))
	if err != nil || signature.KeyVersion != keyVersion || len(signature.Value) != SignatureSize {
		return SignedEvent{}, ErrIntegrity
	}
	hash := sha256.Sum256(canonical)
	return RestoreSignedEvent(SignedEventSnapshot{Event: event.Snapshot(), CanonicalEvent: canonical,
		EventHash: Hash(hash), Signature: signature.Value})
}

// Verify recomputes canonical bytes and the SHA-256 event hash before historical-key signature verification.
func (service *Service) Verify(event SignedEvent) error {
	snapshot := event.Snapshot()
	canonical, err := canonicalEvent(snapshot.Event)
	if err != nil || !bytes.Equal(canonical, snapshot.CanonicalEvent) {
		return ErrIntegrity
	}
	hash := sha256.Sum256(canonical)
	if Hash(hash) != snapshot.EventHash || !service.keys.Verify(eventSigningPayload(canonical), security.AuditSignature{
		KeyVersion: snapshot.Event.SigningKeyVersion,
		Value:      snapshot.Signature,
	}) {
		return ErrIntegrity
	}
	return nil
}

// VerifyChain verifies cryptography and every sequence/previous-hash link from the supplied trusted head.
func (service *Service) VerifyChain(head Head, events []SignedEvent) (Head, error) {
	current := head
	for _, event := range events {
		if err := service.Verify(event); err != nil {
			return Head{}, err
		}
		snapshot := event.Snapshot()
		if snapshot.Event.ChainID != current.ChainID() || snapshot.Event.Sequence != current.Sequence()+1 ||
			snapshot.Event.PreviousHash != current.Hash() || snapshot.Event.OccurredAt.Before(current.UpdatedAt()) {
			return Head{}, ErrChainDiscontinuity
		}
		next, err := event.NextHead()
		if err != nil {
			return Head{}, ErrIntegrity
		}
		current = next
	}
	return current, nil
}

func eventSigningPayload(canonical []byte) []byte {
	return append([]byte(eventSignatureDomain), canonical...)
}

func checkpointSigningPayload(canonical []byte) []byte {
	return append([]byte(checkpointSignatureDomain), canonical...)
}
