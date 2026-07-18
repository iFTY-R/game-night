package challenge

import (
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	challengeTokenVersion = "v1"
	proofMACBytes         = 32
)

// Credentials carries the independent HttpOnly cookie token and response-body proof required at completion.
type Credentials struct {
	CookieToken string
	BodyProof   string
}

// Issued contains persistence state and the two client credentials produced by a Begin operation.
type Issued[P security.HMACKeyPurpose] struct {
	Challenge   Challenge[P]
	Credentials Credentials
}

// AuthorizationKind separates the first state transition from a read-only exact-result replay.
type AuthorizationKind uint8

const (
	AuthorizeFirstUse AuthorizationKind = iota + 1
	AuthorizeExactReplay
)

// Authorization is an in-process capability. Its private fields prevent callers from manufacturing replay authority.
type Authorization struct {
	kind        AuthorizationKind
	resultID    uuid.UUID
	replayUntil time.Time
}

type firstUseCompletionKind uint8

const (
	// firstUseWithoutReplay is the terminal plan for operations that return no one-time material.
	firstUseWithoutReplay firstUseCompletionKind = iota + 1
	// firstUseWithReplay requires one persisted result before challenge consumption can commit.
	firstUseWithReplay
)

// FirstUseCompletion tells the service whether a successful first use has an exact result to replay.
// Its private state forces callers through the reviewed constructors below.
type FirstUseCompletion struct {
	kind        firstUseCompletionKind
	resultID    uuid.UUID
	replayUntil time.Time
}

// NoReplayCompletion terminates a successful operation that returns no one-time secret.
func NoReplayCompletion() FirstUseCompletion {
	return FirstUseCompletion{kind: firstUseWithoutReplay}
}

// NewReplayCompletion binds a successful operation to one committed result and replay deadline.
func NewReplayCompletion(resultID uuid.UUID, replayUntil time.Time) (FirstUseCompletion, error) {
	replayUntil = canonicalTime(replayUntil)
	if resultID == uuid.Nil || replayUntil.IsZero() {
		return FirstUseCompletion{}, ErrInvalidInput
	}
	return FirstUseCompletion{kind: firstUseWithReplay, resultID: resultID, replayUntil: replayUntil}, nil
}

// Kind reports whether the verified challenge permits first execution or exact replay.
func (authorization Authorization) Kind() AuthorizationKind {
	return authorization.kind
}

// ResultID returns the committed result only for exact replay authorizations.
func (authorization Authorization) ResultID() uuid.UUID {
	return authorization.resultID
}

// ReplayCapability can only be implemented by this package because its authorization method is package-private.
type ReplayCapability interface {
	authorizes(uuid.UUID, time.Time) bool
}

func (authorization Authorization) authorizes(resultID uuid.UUID, now time.Time) bool {
	return authorization.kind == AuthorizeExactReplay && resultID != uuid.Nil && authorization.resultID == resultID &&
		canonicalTime(now).Before(authorization.replayUntil)
}

// AuthorizesReplay checks that a verified, consumed challenge is bound to the exact result being accessed.
func AuthorizesReplay(capability ReplayCapability, resultID uuid.UUID, now time.Time) bool {
	// Accept only the concrete private-state value produced by Authorize, not external embedding wrappers.
	authorization, ok := capability.(Authorization)
	return ok && authorization.authorizes(resultID, now)
}

// Service issues and verifies challenges for exactly one compile-time HMAC purpose.
type Service[P security.HMACKeyPurpose] struct {
	keyring *security.HMACKeyring[P]
	clock   clock.Clock
}

// NewService binds one purpose-separated keyring and UTC clock to challenge operations.
func NewService[P security.HMACKeyPurpose](keyring *security.HMACKeyring[P], source clock.Clock) (*Service[P], error) {
	if keyring == nil || source == nil {
		return nil, ErrInvalidInput
	}
	return &Service[P]{keyring: keyring, clock: source}, nil
}

// Issue creates a five-minute challenge with a 16-byte selector and independent 32-byte cookie secret.
func (service *Service[P]) Issue(binding Binding, maxAttempts uint32) (Issued[P], error) {
	if service == nil || service.keyring == nil || service.clock == nil || binding.Validate() != nil ||
		maxAttempts == 0 || maxAttempts > MaximumAttempts {
		return Issued[P]{}, ErrInvalidInput
	}
	createdAt := canonicalTime(service.clock.Now())
	if createdAt.IsZero() {
		return Issued[P]{}, ErrInvalidInput
	}

	selectorEntropy, err := security.RandomBytes(SelectorBytes)
	if err != nil {
		return Issued[P]{}, ErrInvalidInput
	}
	selector, err := identifier.NewSelector(selectorEntropy)
	clear(selectorEntropy)
	if err != nil {
		return Issued[P]{}, ErrInvalidInput
	}
	secret, err := security.RandomBytes(SecretBytes)
	if err != nil {
		return Issued[P]{}, ErrInvalidInput
	}
	defer clear(secret)

	secretMAC, err := service.keyring.Sum(secret)
	if err != nil {
		return Issued[P]{}, ErrInvalidInput
	}
	snapshot := Snapshot[P]{
		ID: uuid.New(), Selector: selector, SecretMAC: secretMAC, Binding: binding,
		MaxAttempts: maxAttempts, CreatedAt: createdAt, ExpiresAt: createdAt.Add(TTL),
	}
	issuedChallenge, err := Restore(snapshot)
	if err != nil {
		return Issued[P]{}, err
	}
	cookieToken, err := security.FormatToken(challengeTokenVersion, selector.Value(), secret)
	if err != nil {
		return Issued[P]{}, ErrInvalidInput
	}
	claims, err := canonicalClaims(snapshot)
	if err != nil {
		return Issued[P]{}, err
	}
	proofMAC, err := service.keyring.Sum(claims)
	if err != nil || proofMAC.KeyVersion != secretMAC.KeyVersion {
		return Issued[P]{}, ErrInvalidInput
	}
	defer clear(proofMAC.Value)
	bodyProof, err := formatProof(proofMAC)
	if err != nil {
		return Issued[P]{}, err
	}
	return Issued[P]{
		Challenge:   issuedChallenge,
		Credentials: Credentials{CookieToken: cookieToken, BodyProof: bodyProof},
	}, nil
}

// Verify requires both credentials and every expected request binding before state transition logic runs.
func (service *Service[P]) Verify(record Challenge[P], expected Binding, credentials Credentials) error {
	if service == nil || service.keyring == nil || expected.Validate() != nil || expected != record.snapshot.Binding {
		return ErrAuthentication
	}
	state := record.State(service.clock.Now())
	if state != StateActive && state != StateConsumed {
		return ErrUnavailable
	}

	selector, cookieSecret, err := parseCookieToken(credentials.CookieToken)
	if err != nil {
		return ErrAuthentication
	}
	defer clear(cookieSecret)
	if !security.ConstantTimeEqual([]byte(selector.Value()), []byte(record.snapshot.Selector.Value())) {
		return ErrAuthentication
	}
	secretMatched, err := service.keyring.Verify(cookieSecret, record.snapshot.SecretMAC)
	if err != nil || !secretMatched {
		return ErrAuthentication
	}

	proofMAC, err := parseProof[P](credentials.BodyProof)
	if err != nil {
		return ErrAuthentication
	}
	defer clear(proofMAC.Value)
	if proofMAC.KeyVersion != record.snapshot.SecretMAC.KeyVersion {
		return ErrAuthentication
	}
	claims, err := canonicalClaims(record.snapshot)
	if err != nil {
		return ErrAuthentication
	}
	proofMatched, err := service.keyring.Verify(claims, proofMAC)
	if err != nil || !proofMatched {
		return ErrAuthentication
	}
	return nil
}

// Authorize verifies both credentials, then permits either first execution or the exact committed result replay.
func (service *Service[P]) Authorize(
	record Challenge[P],
	expected Binding,
	credentials Credentials,
	operationID idempotency.OperationID,
	requestDigest idempotency.Digest,
) (Authorization, error) {
	if !operationID.Valid() {
		return Authorization{}, ErrInvalidInput
	}
	if err := service.Verify(record, expected, credentials); err != nil {
		return Authorization{}, err
	}

	now := canonicalTime(service.clock.Now())
	switch record.State(now) {
	case StateActive:
		return Authorization{kind: AuthorizeFirstUse}, nil
	case StateConsumed:
		replay := record.snapshot.Replay
		if replay == nil || replay.OperationID != operationID || !now.Before(replay.ReplayUntil) {
			return Authorization{}, ErrUnavailable
		}
		if replay.RequestDigest != requestDigest {
			return Authorization{}, idempotency.ErrConflict
		}
		return Authorization{
			kind: AuthorizeExactReplay, resultID: replay.ResultID, replayUntil: replay.ReplayUntil,
		}, nil
	default:
		return Authorization{}, ErrUnavailable
	}
}

// CompleteFirstUse creates the only transition accepted after authorized business work succeeds.
// Operation and digest come from the authenticated request, so callbacks cannot substitute replay bindings.
func (service *Service[P]) CompleteFirstUse(
	record Challenge[P],
	operationID idempotency.OperationID,
	requestDigest idempotency.Digest,
	completion FirstUseCompletion,
) (Challenge[P], error) {
	if service == nil || service.clock == nil || !operationID.Valid() {
		return Challenge[P]{}, ErrInvalidInput
	}
	now := service.clock.Now()
	switch completion.kind {
	case firstUseWithoutReplay:
		return record.ConsumeWithoutReplay(now)
	case firstUseWithReplay:
		return record.Consume(now, ReplayAuthorization{
			OperationID: operationID, RequestDigest: requestDigest,
			ResultID: completion.resultID, ReplayUntil: completion.replayUntil,
		})
	default:
		return Challenge[P]{}, ErrInvalidInput
	}
}

// SelectorFromCredentials performs bounded cookie parsing for the repository lookup that precedes authorization.
func SelectorFromCredentials(credentials Credentials) (identifier.Selector, error) {
	selector, secret, err := parseCookieToken(credentials.CookieToken)
	clear(secret)
	return selector, err
}

func parseCookieToken(encoded string) (identifier.Selector, []byte, error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: challengeTokenVersion, MinSecretBytes: SecretBytes, MaxSecretBytes: SecretBytes,
	})
	if err != nil {
		return identifier.Selector{}, nil, ErrAuthentication
	}
	selector, err := identifier.ParseSelector(parsed.Selector)
	if err != nil || selector.ByteLength() != SelectorBytes {
		clear(parsed.Secret)
		return identifier.Selector{}, nil, ErrAuthentication
	}
	return selector, parsed.Secret, nil
}

func formatProof[P security.HMACKeyPurpose](proof security.MAC[P]) (string, error) {
	if proof.KeyVersion == 0 || len(proof.Value) != proofMACBytes {
		return "", ErrInvalidInput
	}
	encoded, err := security.FormatToken(challengeTokenVersion, strconv.FormatUint(uint64(proof.KeyVersion), 10), proof.Value)
	if err != nil {
		return "", ErrInvalidInput
	}
	return encoded, nil
}

func parseProof[P security.HMACKeyPurpose](encoded string) (security.MAC[P], error) {
	parsed, err := security.ParseToken(encoded, security.TokenPolicy{
		Version: challengeTokenVersion, MinSecretBytes: proofMACBytes, MaxSecretBytes: proofMACBytes,
	})
	if err != nil {
		return security.MAC[P]{}, ErrAuthentication
	}
	version, err := strconv.ParseUint(parsed.Selector, 10, 32)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != parsed.Selector {
		clear(parsed.Secret)
		return security.MAC[P]{}, ErrAuthentication
	}
	return security.MAC[P]{KeyVersion: uint32(version), Value: parsed.Secret}, nil
}

// IsAuthenticationFailure lets transports collapse all submitted credential mismatches without inspecting details.
func IsAuthenticationFailure(err error) bool {
	return errors.Is(err, ErrAuthentication)
}
