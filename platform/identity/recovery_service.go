package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// IdentitySecretOperation identifies the reviewed user-side envelope family for receipt confirmation.
type IdentitySecretOperation uint8

const (
	IdentitySecretOperationBootstrap IdentitySecretOperation = iota + 1
	IdentitySecretOperationOnboarding
	IdentitySecretOperationRecovery
	IdentitySecretOperationRecoveryCodeRotation
)

// BeginRecoveryChallengeCommand binds a recovery challenge to the accepted Origin and browser flow.
type BeginRecoveryChallengeCommand struct {
	CanonicalOrigin string
	RequestFlowID   challenge.RequestFlowID
}

// BeginRecoveryCommand validates a long-lived or administrator-assisted code without consuming it.
type BeginRecoveryCommand struct {
	CanonicalOrigin      string
	RequestFlowID        challenge.RequestFlowID
	ChallengeCredentials challenge.Credentials
	RecoveryCode         string
	ClientIP             string
}

// BeginRecoveryResult returns the short-lived grant used by CompleteRecovery.
type BeginRecoveryResult struct {
	RecoveryGrant string
	ExpiresAt     time.Time
}

// CompleteRecoveryCommand carries the exact semantics bound on the first winning completion.
type CompleteRecoveryCommand struct {
	CanonicalOrigin string
	RecoveryGrant   string
	OperationID     idempotency.OperationID
	DeviceLabel     string
	DevicePolicy    RecoveryDevicePolicy
	RequestID       string
}

// CompleteRecoveryResult contains only committed user/device state and the replayable one-time bundle.
type CompleteRecoveryResult struct {
	Operation     OperationResult
	User          User
	Device        DeviceCredential
	DeviceSecrets *DeviceCookieWrite
	RecoveryCode  string
}

// RotateRecoveryCodeCommand requires current device and CSRF authority over an explicit operation ID.
type RotateRecoveryCodeCommand struct {
	DeviceToken string
	CSRFToken   string
	OperationID idempotency.OperationID
	RequestID   string
}

// RotateRecoveryCodeResult returns the exact committed code for first execution or response-loss replay.
type RotateRecoveryCodeResult struct {
	Operation    OperationResult
	RecoveryCode string
}

// ConfirmSecretReceiptCommand reauthenticates the current device before erasing an exact result.
type ConfirmSecretReceiptCommand struct {
	DeviceToken string
	CSRFToken   string
	Operation   IdentitySecretOperation
	OperationID idempotency.OperationID
	ResultID    uuid.UUID
}

// ConfirmSecretReceiptResult reports the durable tombstone state without returning secret material.
type ConfirmSecretReceiptResult struct{ Confirmed bool }

// ListDevicesCommand selects a stable user-owned page; revoked rows are opt-in while expired rows remain visible.
type ListDevicesCommand struct {
	DeviceToken    string
	IncludeRevoked bool
	After          DevicePageCursor
	PageSize       uint32
}

// ListDevicesResult contains redacted device summaries and the next keyset cursor.
type ListDevicesResult struct {
	Devices    []DeviceSummary
	NextCursor DevicePageCursor
}

// RevokeDeviceCommand separates free-form audit detail from the closed persisted revocation reason.
type RevokeDeviceCommand struct {
	DeviceToken  string
	CSRFToken    string
	CredentialID uuid.UUID
	Reason       string
	RequestID    string
}

// RevokeDeviceResult tells the transport whether the authenticated Cookie must be cleared immediately.
type RevokeDeviceResult struct {
	CurrentDeviceRevoked  bool
	CredentialInstruction CredentialInstruction
}

// NewServiceWithRecovery enables Task 10 flows while preserving the narrower constructor for earlier milestones.
func NewServiceWithRecovery(
	challenges *ChallengeService,
	devices *DeviceService,
	recovery *RecoveryCodeService,
	recoveryAttempts *RecoveryAttemptService,
	results *secretresult.Service,
	unitOfWork IdentityUnitOfWork,
	limiter ratelimit.RateLimiter,
	usernames identifier.UsernameValidator,
	serviceClock clock.Clock,
	auditService *audit.Service,
	checkpointHealth *audit.CheckpointHealthPolicy,
) (*Service, error) {
	if recoveryAttempts == nil || auditService == nil || checkpointHealth == nil {
		return nil, ErrInvalidIdentityRequest
	}
	service, err := NewService(
		challenges, devices, recovery, results, unitOfWork, limiter, usernames, serviceClock,
	)
	if err != nil {
		return nil, err
	}
	service.recoveryAttempts = recoveryAttempts
	service.audit = auditService
	service.checkpointHealth = checkpointHealth
	return service, nil
}

// BeginRecoveryChallenge creates the anonymous five-minute proof required before any selector lookup.
func (service *Service) BeginRecoveryChallenge(
	ctx context.Context,
	command BeginRecoveryChallengeCommand,
) (IssuedChallenge, error) {
	if service == nil || ctx == nil {
		return IssuedChallenge{}, ErrInvalidIdentityRequest
	}
	issued, err := service.challenges.Issue(
		ChallengePurposeRecovery, command.CanonicalOrigin, command.RequestFlowID, challenge.DefaultMaxAttempts,
	)
	if err != nil {
		return IssuedChallenge{}, err
	}
	if err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	}); err != nil {
		return IssuedChallenge{}, err
	}
	return issued, nil
}

type recoverySourceKind uint8

const (
	recoverySourceCode recoverySourceKind = iota + 1
	recoverySourceAssisted
)

type resolvedRecoverySource struct {
	kind       recoverySourceKind
	user       User
	credential RecoveryCredential
	assisted   AssistedRecoveryGrant
	selector   identifier.Selector
}

// BeginRecovery verifies one source under ordered limiter buckets and consumes only the anonymous challenge.
func (service *Service) BeginRecovery(ctx context.Context, command BeginRecoveryCommand) (BeginRecoveryResult, error) {
	if service == nil || service.recoveryAttempts == nil || ctx == nil || command.RecoveryCode == "" {
		return BeginRecoveryResult{}, ErrInvalidIdentityRequest
	}
	sourceKind, limiterSelector := recoveryCodeFamily(command.RecoveryCode)
	if err := service.consumeRecoveryLookupLimit(ctx, command.ClientIP, limiterSelector); err != nil {
		return BeginRecoveryResult{}, err
	}
	source, found, err := service.lookupRecoverySource(ctx, sourceKind, command.RecoveryCode)
	if err != nil {
		return BeginRecoveryResult{}, err
	}
	if found {
		if err := service.consumeRecoveryUserLimit(ctx, source.user.Snapshot().ID); err != nil {
			return BeginRecoveryResult{}, err
		}
	}
	verifyErr := service.verifyRecoverySource(ctx, sourceKind, found, source, command.RecoveryCode)
	if verifyErr != nil {
		if found && sourceKind == recoverySourceAssisted && errors.Is(verifyErr, ErrRecoveryInvalid) {
			if failureErr := service.recordAssistedRecoveryFailure(ctx, source.assisted); failureErr != nil {
				return BeginRecoveryResult{}, failureErr
			}
		}
		return BeginRecoveryResult{}, verifyErr
	}
	if source.user.Snapshot().Status != UserStatusActive {
		return BeginRecoveryResult{}, ErrRecoveryInvalid
	}
	challengeSelector, err := challenge.SelectorFromCredentials(command.ChallengeCredentials)
	if err != nil {
		return BeginRecoveryResult{}, ErrRecoveryInvalid
	}
	beginOperation, err := idempotency.ParseOperationID(challengeSelector.Value())
	if err != nil {
		return BeginRecoveryResult{}, ErrRecoveryInvalid
	}
	beginDigest := digestIdentityRequest(
		"identity.recovery.begin.v1", strconv.Itoa(int(sourceKind)), source.selector.Value(),
	)
	var issued IssuedRecoveryAttempt
	_, err = service.challenges.AuthorizeIdentityPersistent(
		ctx, service.unitOfWork, ChallengePurposeRecovery, command.CanonicalOrigin, command.RequestFlowID,
		command.ChallengeCredentials, beginOperation, beginDigest,
		func(
			ctx context.Context,
			transaction IdentityTransaction,
			record Challenge,
			authorization challenge.Authorization,
		) (AuthorizedChallengeCompletion, error) {
			if authorization.Kind() != challenge.AuthorizeFirstUse {
				return AuthorizedChallengeCompletion{}, ErrRecoveryInvalid
			}
			user, getErr := transaction.Users().GetForUpdate(ctx, source.user.Snapshot().ID)
			if getErr != nil || user.Snapshot().Status != UserStatusActive {
				return AuthorizedChallengeCompletion{}, ErrRecoveryInvalid
			}
			binding := RecoveryAttemptBinding{
				UserID: user.Snapshot().ID, ChallengeID: record.Snapshot().ID,
				Origin: record.Snapshot().Binding.Origin,
			}
			switch sourceKind {
			case recoverySourceCode:
				current, sourceErr := transaction.RecoveryCredentials().GetForUpdate(
					ctx, source.credential.Snapshot().ID, user.Snapshot().ID, source.credential.Snapshot().Version,
				)
				if sourceErr != nil || current.Snapshot().Status != RecoveryCredentialActive {
					return AuthorizedChallengeCompletion{}, ErrRecoveryInvalid
				}
				binding.RecoveryCredentialID = current.Snapshot().ID
				binding.RecoveryCredentialVersion = current.Snapshot().Version
			case recoverySourceAssisted:
				current, sourceErr := transaction.AssistedRecoveryGrants().GetForUpdate(
					ctx, source.assisted.Snapshot().ID, user.Snapshot().ID,
				)
				if sourceErr != nil || current.State(service.clock.Now()) != AssistedRecoveryGrantActive {
					return AuthorizedChallengeCompletion{}, ErrRecoveryInvalid
				}
				binding.AssistedGrantID = current.Snapshot().ID
			default:
				return AuthorizedChallengeCompletion{}, ErrRecoveryInvalid
			}
			issued, getErr = service.recoveryAttempts.Issue(binding)
			if getErr != nil {
				return AuthorizedChallengeCompletion{}, getErr
			}
			if _, getErr = transaction.RecoveryAttempts().Insert(ctx, issued.Attempt); getErr != nil {
				return AuthorizedChallengeCompletion{}, getErr
			}
			return NoReplayCompletion(), nil
		},
	)
	if err != nil {
		return BeginRecoveryResult{}, err
	}
	return BeginRecoveryResult{RecoveryGrant: issued.Grant, ExpiresAt: issued.ExpiresAt}, nil
}

func recoveryCodeFamily(encoded string) (recoverySourceKind, string) {
	if assistedRecoveryTokenFamily(encoded) {
		parsed, err := parseAssistedRecoveryCode(encoded)
		if err == nil {
			clear(parsed.secret)
			return recoverySourceAssisted, parsed.selector.Value()
		}
		return recoverySourceAssisted, "invalid"
	}
	parsed, err := parseRecoveryCode(encoded)
	if err == nil {
		clear(parsed.secret)
		return recoverySourceCode, parsed.selector.Value()
	}
	return recoverySourceCode, "invalid"
}

func (service *Service) lookupRecoverySource(
	ctx context.Context,
	kind recoverySourceKind,
	encoded string,
) (resolvedRecoverySource, bool, error) {
	var source resolvedRecoverySource
	var found bool
	err := service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		switch kind {
		case recoverySourceCode:
			selector, parseErr := service.recovery.SelectorFromCode(encoded)
			if parseErr != nil {
				return nil
			}
			source.selector = selector
			credential, getErr := transaction.RecoveryCredentials().GetBySelector(ctx, selector)
			if errors.Is(getErr, ErrRecoveryInvalid) {
				return nil
			}
			if getErr != nil {
				return getErr
			}
			user, getErr := transaction.Users().GetByID(ctx, credential.Snapshot().UserID)
			if getErr != nil {
				return getErr
			}
			source.kind, source.credential, source.user, found = kind, credential, user, true
		case recoverySourceAssisted:
			selector, parseErr := service.recovery.AssistedSelectorFromCode(encoded)
			if parseErr != nil {
				return nil
			}
			source.selector = selector
			grant, getErr := transaction.AssistedRecoveryGrants().GetBySelector(ctx, selector)
			if errors.Is(getErr, ErrRecoveryInvalid) {
				return nil
			}
			if getErr != nil {
				return getErr
			}
			user, getErr := transaction.Users().GetByID(ctx, grant.Snapshot().UserID)
			if getErr != nil {
				return getErr
			}
			source.kind, source.assisted, source.user, found = kind, grant, user, true
		default:
			return ErrRecoveryInvalid
		}
		return nil
	})
	return source, found, err
}

func (service *Service) verifyRecoverySource(
	ctx context.Context,
	kind recoverySourceKind,
	found bool,
	source resolvedRecoverySource,
	encoded string,
) error {
	switch kind {
	case recoverySourceCode:
		if !found {
			return service.recovery.VerifyOrDummy(ctx, nil, encoded)
		}
		return service.recovery.VerifyOrDummy(ctx, &source.credential, encoded)
	case recoverySourceAssisted:
		if !found {
			return service.recovery.VerifyAssistedOrDummy(ctx, nil, encoded, service.clock.Now())
		}
		return service.recovery.VerifyAssistedOrDummy(ctx, &source.assisted, encoded, service.clock.Now())
	default:
		return ErrRecoveryInvalid
	}
}

func (service *Service) recordAssistedRecoveryFailure(ctx context.Context, grant AssistedRecoveryGrant) error {
	return service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		if _, err := transaction.Users().GetForUpdate(ctx, grant.Snapshot().UserID); err != nil {
			return err
		}
		current, err := transaction.AssistedRecoveryGrants().GetForUpdate(
			ctx, grant.Snapshot().ID, grant.Snapshot().UserID,
		)
		if err != nil {
			return err
		}
		next, err := current.RecordFailure(service.clock.Now())
		if err != nil {
			return err
		}
		_, err = transaction.AssistedRecoveryGrants().RecordFailureCAS(ctx, current, next)
		return err
	})
}

func (service *Service) consumeRecoveryLookupLimit(ctx context.Context, clientIP, selector string) error {
	policy, err := ratelimit.PolicyFor(ratelimit.OperationIdentityRecoveryLookup)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	ipKey, err := identityBucketKey(ratelimit.DimensionIP, clientIP)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	selectorKey, err := identityBucketKey(ratelimit.DimensionRecoverySelector, selector)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	return policy.Consume(ctx, service.limiter, ipKey, selectorKey)
}

func (service *Service) consumeRecoveryUserLimit(ctx context.Context, userID uuid.UUID) error {
	policy, err := ratelimit.PolicyFor(ratelimit.OperationIdentityRecoveryResolved)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	userKey, err := identityBucketKey(ratelimit.DimensionUser, userID.String())
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	return policy.Consume(ctx, service.limiter, userKey)
}

// CompleteRecovery atomically consumes one source and returns only the exact committed recovery bundle on retry.
func (service *Service) CompleteRecovery(
	ctx context.Context,
	command CompleteRecoveryCommand,
) (CompleteRecoveryResult, error) {
	if service == nil || service.recoveryAttempts == nil || service.audit == nil || ctx == nil ||
		!command.OperationID.Valid() || !command.DevicePolicy.Valid() || command.RequestID == "" {
		return CompleteRecoveryResult{}, ErrInvalidIdentityRequest
	}
	label, err := normalizeDeviceLabel(command.DeviceLabel)
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	origin, err := challenge.DigestOrigin(command.CanonicalOrigin)
	if err != nil {
		return CompleteRecoveryResult{}, ErrRecoveryInvalid
	}
	digest := digestIdentityRequest(
		"identity.recovery.complete.v1", label, strconv.Itoa(int(command.DevicePolicy)),
	)
	selector, err := service.recoveryAttempts.SelectorFromGrant(command.RecoveryGrant)
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	preliminary, user, authorization, err := service.authorizeRecoveryAttempt(
		ctx, selector, command.RecoveryGrant, origin, digest,
	)
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	if user.Snapshot().Status != UserStatusActive {
		return CompleteRecoveryResult{}, ErrRecoveryInvalid
	}

	binding := identityResultBinding(
		secretresult.ScopeIdentityRecovery, user.Snapshot().ID, command.OperationID, digest,
		secretresult.ResultTypeIdentityRecoveryBundle,
	)
	var issuedDevice IssuedDeviceCredential
	var issuedRecovery IssuedRecoveryCredential
	var prepared secretresult.Result
	var preparedPlaintext []byte
	if preliminary.Snapshot().Status == RecoveryAttemptActive {
		issuedDevice, err = service.devices.Issue(user.Snapshot().ID, label)
		if err != nil {
			return CompleteRecoveryResult{}, err
		}
		issuedRecovery, err = service.recovery.Issue(ctx, user.Snapshot().ID, service.clock.Now())
		if err != nil {
			return CompleteRecoveryResult{}, err
		}
		preparedPlaintext, err = encodeRecoveryEnvelope(issuedDevice, issuedRecovery.Code)
		if err != nil {
			return CompleteRecoveryResult{}, err
		}
		defer clear(preparedPlaintext)
		resultID, idErr := uuid.NewV7()
		if idErr != nil {
			return CompleteRecoveryResult{}, ErrInvalidIdentityRequest
		}
		prepared, err = service.results.PrepareAvailable(resultID, binding, preparedPlaintext, identitySecretTTL)
		if err != nil {
			return CompleteRecoveryResult{}, err
		}
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return CompleteRecoveryResult{}, ErrInvalidIdentityRequest
	}
	var committedResult secretresult.Result
	var committedUser User
	var committedDevice DeviceCredential
	var committedPlaintext []byte
	var replayed bool
	defer clear(committedPlaintext)
	err = service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		currentUser, getErr := transaction.Users().GetForUpdate(ctx, user.Snapshot().ID)
		if getErr != nil || currentUser.Snapshot().Status != UserStatusActive {
			return ErrRecoveryInvalid
		}
		currentAttempt, getErr := transaction.RecoveryAttempts().GetForUpdate(ctx, selector)
		if getErr != nil {
			return getErr
		}
		currentAuthorization, authErr := service.recoveryAttempts.Authorize(
			currentAttempt, command.RecoveryGrant, origin, digest,
		)
		if authErr != nil {
			return authErr
		}
		if currentAttempt.Snapshot().Binding.UserID != currentUser.Snapshot().ID {
			return ErrRecoveryInvalid
		}
		if currentAttempt.Snapshot().Status == RecoveryAttemptConsumed {
			stored, resultErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
			if resultErr != nil || stored.Snapshot().ID != currentAttempt.Snapshot().ResultID {
				return ErrRecoveryInvalid
			}
			if _, resultErr = stored.Resolve(binding, service.clock.Now()); resultErr != nil {
				return resultErr
			}
			grant, grantErr := service.recoveryAttempts.ResultGrant(
				currentAttempt, currentAuthorization, stored.Snapshot().ID, stored.Snapshot().SecretExpiresAt,
			)
			if grantErr != nil {
				return grantErr
			}
			opened, openErr := service.results.OpenRecoveryAuthorizedResult(stored, binding, grant)
			if openErr != nil {
				return openErr
			}
			deviceID, envelopeErr := recoveryEnvelopeDeviceID(opened)
			if envelopeErr != nil {
				clear(opened)
				return envelopeErr
			}
			device, deviceErr := transaction.Devices().GetForUpdate(ctx, deviceID)
			if deviceErr != nil || device.Snapshot().UserID != currentUser.Snapshot().ID {
				clear(opened)
				return ErrIdentityIntegrity
			}
			committedResult, committedUser, committedDevice = stored, currentUser, device
			committedPlaintext, replayed = opened, true
			return nil
		}
		if !authorization.AllowsFirstUse(preliminary) || prepared.Snapshot().ID == uuid.Nil {
			return ErrRecoveryConcurrentTransition
		}
		if healthErr := service.requireHealthyAudit(ctx, transaction); healthErr != nil {
			return healthErr
		}

		now := service.clock.Now()
		sourceKind := recoverySourceCode
		var assistedCurrent AssistedRecoveryGrant
		bindingSnapshot := currentAttempt.Snapshot().Binding
		if bindingSnapshot.RecoveryCredentialID != uuid.Nil {
			currentCredential, sourceErr := transaction.RecoveryCredentials().GetForUpdate(
				ctx, bindingSnapshot.RecoveryCredentialID, currentUser.Snapshot().ID,
				bindingSnapshot.RecoveryCredentialVersion,
			)
			if sourceErr != nil || currentCredential.Snapshot().Status != RecoveryCredentialActive {
				return ErrRecoveryInvalid
			}
			consumedCredential, sourceErr := currentCredential.Consume(now)
			if sourceErr != nil {
				return sourceErr
			}
			if _, sourceErr = transaction.RecoveryCredentials().ConsumeCAS(ctx, currentCredential, consumedCredential); sourceErr != nil {
				return sourceErr
			}
		} else {
			sourceKind = recoverySourceAssisted
			assistedCurrent, getErr = transaction.AssistedRecoveryGrants().GetForUpdate(
				ctx, bindingSnapshot.AssistedGrantID, currentUser.Snapshot().ID,
			)
			if getErr != nil || assistedCurrent.State(now) != AssistedRecoveryGrantActive {
				return ErrRecoveryInvalid
			}
			// Admin grant creation should revoke ordinary recovery; this closes stale rows defensively in the same transaction.
			activeCredential, activeErr := transaction.RecoveryCredentials().GetActiveForUserForUpdate(ctx, currentUser.Snapshot().ID)
			if activeErr == nil {
				revokedCredential, revokeErr := activeCredential.Revoke(RecoveryRevokeAssisted, now)
				if revokeErr != nil {
					return revokeErr
				}
				if _, revokeErr = transaction.RecoveryCredentials().RevokeCAS(ctx, activeCredential, revokedCredential); revokeErr != nil {
					return revokeErr
				}
			} else if !errors.Is(activeErr, ErrRecoveryInvalid) {
				return activeErr
			}
		}

		storedDevice, insertErr := transaction.Devices().Insert(ctx, issuedDevice.Credential)
		if insertErr != nil {
			return insertErr
		}
		if _, insertErr = transaction.RecoveryCredentials().Insert(ctx, issuedRecovery.Credential); insertErr != nil {
			return insertErr
		}
		var revokedDevices []DeviceSummary
		if command.DevicePolicy == RecoveryDevicePolicyRevokeOtherDevices {
			revokedDevices, insertErr = transaction.Devices().RevokeOtherActiveForRecovery(
				ctx, currentUser.Snapshot().ID, storedDevice.Snapshot().CredentialID, now,
			)
			if insertErr != nil {
				return insertErr
			}
		}
		exceptAssistedID := uuid.Nil
		if sourceKind == recoverySourceAssisted {
			exceptAssistedID = assistedCurrent.Snapshot().ID
		}
		revokedAssisted, insertErr := transaction.AssistedRecoveryGrants().RevokeActiveForUser(
			ctx, currentUser.Snapshot().ID, exceptAssistedID, now,
		)
		if insertErr != nil {
			return insertErr
		}
		storedResult, insertErr := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if insertErr != nil {
			return insertErr
		}
		if sourceKind == recoverySourceAssisted {
			consumedGrant, consumeErr := assistedCurrent.Consume(storedResult.Snapshot().ID, now)
			if consumeErr != nil {
				return consumeErr
			}
			if _, consumeErr = transaction.AssistedRecoveryGrants().ConsumeCAS(ctx, assistedCurrent, consumedGrant); consumeErr != nil {
				return consumeErr
			}
		}
		consumedAttempt, consumeErr := currentAttempt.Consume(
			currentAuthorization, storedResult.Snapshot().ID, digest, now,
		)
		if consumeErr != nil {
			return consumeErr
		}
		storedAttempt, consumeErr := transaction.RecoveryAttempts().ConsumeCAS(ctx, currentAttempt, consumedAttempt)
		if consumeErr != nil {
			return consumeErr
		}
		if consumeErr = service.appendRecoveryEvents(
			ctx, transaction, eventID, command.RequestID, now, currentUser.Snapshot().ID,
			storedDevice.Snapshot().CredentialID, issuedRecovery.Credential.Snapshot().ID,
			storedResult.Snapshot().ID, sourceKind, command.DevicePolicy, revokedDevices, revokedAssisted,
		); consumeErr != nil {
			return consumeErr
		}
		consumedAuthorization, consumeErr := service.recoveryAttempts.Authorize(
			storedAttempt, command.RecoveryGrant, origin, digest,
		)
		if consumeErr != nil {
			return consumeErr
		}
		grant, consumeErr := service.recoveryAttempts.ResultGrant(
			storedAttempt, consumedAuthorization, storedResult.Snapshot().ID, storedResult.Snapshot().SecretExpiresAt,
		)
		if consumeErr != nil {
			return consumeErr
		}
		opened, consumeErr := service.results.OpenRecoveryAuthorizedResult(storedResult, binding, grant)
		if consumeErr != nil {
			return consumeErr
		}
		committedResult, committedUser, committedDevice, committedPlaintext = storedResult, currentUser, storedDevice, opened
		return nil
	})
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	return service.recoveryResponse(
		command.OperationID, committedResult, committedUser, committedDevice, committedPlaintext, replayed,
	)
}

func (service *Service) authorizeRecoveryAttempt(
	ctx context.Context,
	selector identifier.Selector,
	encoded string,
	origin challenge.OriginDigest,
	digest idempotency.Digest,
) (RecoveryAttempt, User, RecoveryAttemptAuthorization, error) {
	var attempt RecoveryAttempt
	var user User
	err := service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		var getErr error
		attempt, getErr = transaction.RecoveryAttempts().GetBySelector(ctx, selector)
		if getErr != nil {
			return getErr
		}
		user, getErr = transaction.Users().GetByID(ctx, attempt.Snapshot().Binding.UserID)
		return getErr
	})
	if err != nil {
		return RecoveryAttempt{}, User{}, RecoveryAttemptAuthorization{}, err
	}
	authorization, err := service.recoveryAttempts.Authorize(attempt, encoded, origin, digest)
	if err == nil {
		return attempt, user, authorization, nil
	}
	if errors.Is(err, ErrRecoveryInvalid) && attempt.Snapshot().Status == RecoveryAttemptActive {
		failureErr := service.recordRecoveryAttemptFailure(ctx, selector)
		if failureErr != nil {
			return RecoveryAttempt{}, User{}, RecoveryAttemptAuthorization{}, failureErr
		}
	}
	return RecoveryAttempt{}, User{}, RecoveryAttemptAuthorization{}, err
}

func (service *Service) recordRecoveryAttemptFailure(ctx context.Context, selector identifier.Selector) error {
	return service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		observed, err := transaction.RecoveryAttempts().GetBySelector(ctx, selector)
		if err != nil {
			return err
		}
		ownerID := observed.Snapshot().Binding.UserID
		// Recovery writes always lock user before attempt/source rows to avoid crossing CompleteRecovery's lock order.
		if _, err = transaction.Users().GetForUpdate(ctx, ownerID); err != nil {
			return err
		}
		attempt, err := transaction.RecoveryAttempts().GetForUpdate(ctx, selector)
		if err != nil {
			return err
		}
		if attempt.Snapshot().Binding.UserID != ownerID {
			return ErrIdentityIntegrity
		}
		next, err := attempt.RecordFailure(service.clock.Now())
		if err != nil {
			return err
		}
		_, err = transaction.RecoveryAttempts().RecordFailureCAS(ctx, attempt, next)
		return err
	})
}

type recoveryResultEnvelope struct {
	DeviceToken  string `json:"device_token"`
	CSRFToken    string `json:"csrf_token"`
	CredentialID string `json:"credential_id"`
	Generation   uint64 `json:"generation"`
	RecoveryCode string `json:"recovery_code"`
}

func encodeRecoveryEnvelope(issued IssuedDeviceCredential, recoveryCode string) ([]byte, error) {
	snapshot := issued.Credential.Snapshot()
	return json.Marshal(recoveryResultEnvelope{
		DeviceToken: issued.secrets.token, CSRFToken: issued.secrets.csrfToken,
		CredentialID: snapshot.CredentialID.String(), Generation: issued.secrets.generation,
		RecoveryCode: recoveryCode,
	})
}

func decodeRecoveryEnvelope(plaintext []byte) (recoveryResultEnvelope, error) {
	var envelope recoveryResultEnvelope
	if err := decodeIdentityEnvelope(plaintext, &envelope); err != nil || envelope.DeviceToken == "" ||
		envelope.CSRFToken == "" || envelope.CredentialID == "" || envelope.Generation == 0 || envelope.RecoveryCode == "" {
		return recoveryResultEnvelope{}, ErrIdentityIntegrity
	}
	return envelope, nil
}

func recoveryEnvelopeDeviceID(plaintext []byte) (uuid.UUID, error) {
	envelope, err := decodeRecoveryEnvelope(plaintext)
	if err != nil {
		return uuid.Nil, err
	}
	credentialID, err := uuid.Parse(envelope.CredentialID)
	if err != nil || credentialID == uuid.Nil || credentialID.String() != envelope.CredentialID {
		return uuid.Nil, ErrIdentityIntegrity
	}
	return credentialID, nil
}

func (service *Service) recoveryResponse(
	operationID idempotency.OperationID,
	result secretresult.Result,
	user User,
	device DeviceCredential,
	plaintext []byte,
	replayed bool,
) (CompleteRecoveryResult, error) {
	envelope, err := decodeRecoveryEnvelope(plaintext)
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	credentialID, err := uuid.Parse(envelope.CredentialID)
	if err != nil || credentialID != device.Snapshot().CredentialID || envelope.Generation != device.Snapshot().Generation {
		return CompleteRecoveryResult{}, ErrIdentityIntegrity
	}
	write, err := service.devices.cookieWrite(device, issuedDeviceSecrets{
		token: envelope.DeviceToken, csrfToken: envelope.CSRFToken, generation: envelope.Generation,
	})
	if err != nil {
		return CompleteRecoveryResult{}, err
	}
	return CompleteRecoveryResult{
		Operation: operationResult(operationID, result, replayed), User: user, Device: device,
		DeviceSecrets: &write, RecoveryCode: envelope.RecoveryCode,
	}, nil
}

func (service *Service) appendRecoveryEvents(
	ctx context.Context,
	transaction IdentityTransaction,
	eventID uuid.UUID,
	requestID string,
	occurredAt time.Time,
	userID, deviceID, recoveryCredentialID, resultID uuid.UUID,
	source recoverySourceKind,
	policy RecoveryDevicePolicy,
	revokedDevices []DeviceSummary,
	revokedAssisted []uuid.UUID,
) error {
	sort.Slice(revokedDevices, func(left, right int) bool {
		return revokedDevices[left].CredentialID.String() < revokedDevices[right].CredentialID.String()
	})
	revokedIDs := make([]string, len(revokedDevices))
	for index, device := range revokedDevices {
		revokedIDs[index] = device.CredentialID.String()
	}
	protoSource := identityv1.IdentityRecoverySource_IDENTITY_RECOVERY_SOURCE_RECOVERY_CODE
	reasonCode := "recovery_code"
	if source == recoverySourceAssisted {
		protoSource = identityv1.IdentityRecoverySource_IDENTITY_RECOVERY_SOURCE_ADMIN_ASSISTED_GRANT
		reasonCode = "assisted_recovery"
	}
	protoPolicy := identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_KEEP_OTHER_DEVICES
	if policy == RecoveryDevicePolicyRevokeOtherDevices {
		protoPolicy = identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_REVOKE_OTHER_DEVICES
	}
	payload, err := deterministicIdentityEvent(&identityv1.IdentityRecoveryCompletedEvent{
		SchemaVersion: 1, EventId: eventID.String(), RequestId: requestID,
		OccurredAt: timestamppb.New(occurredAt), UserId: userID.String(),
		NewDeviceCredentialId: deviceID.String(), NewRecoveryCredentialId: recoveryCredentialID.String(),
		ResultId: resultID.String(), Source: protoSource, DevicePolicy: protoPolicy,
		RevokedDeviceCredentialIds: revokedIDs, RevokedAssistedGrantCount: uint32(len(revokedAssisted)),
	})
	if err != nil {
		return err
	}
	detailDigest := sha256.Sum256(payload)
	if err = service.appendAuditEvent(ctx, transaction, audit.EventInput{
		EventID: eventID, RequestID: requestID, OccurredAt: occurredAt,
		Actor: mustAuditActor(audit.ActorUser, userID), Target: mustAuditTarget(audit.TargetUser, userID),
		Action: audit.ActionIdentityRecovered, ReasonCode: reasonCode, DetailDigest: detailDigest[:],
	}); err != nil {
		return err
	}
	event, err := outbox.NewEvent(
		eventID, outbox.EventTypeIdentityRecoveryCompleted, outbox.AggregateTypeIdentityUser,
		userID, payload, occurredAt, occurredAt,
	)
	if err != nil {
		return err
	}
	if _, err = transaction.OutboxEvents().Insert(ctx, event); err != nil {
		return err
	}
	for _, device := range revokedDevices {
		deviceEventID := uuid.NewSHA1(eventID, []byte(device.CredentialID.String()))
		devicePayload, payloadErr := deterministicIdentityEvent(&identityv1.IdentityDeviceRevokedEvent{
			SchemaVersion: 1, EventId: deviceEventID.String(), CauseEventId: eventID.String(),
			RequestId: requestID, OccurredAt: timestamppb.New(occurredAt), UserId: userID.String(),
			DeviceCredentialId: device.CredentialID.String(),
			Reason:             identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_RECOVERY,
		})
		if payloadErr != nil {
			return payloadErr
		}
		deviceEvent, eventErr := outbox.NewEvent(
			deviceEventID, outbox.EventTypeIdentityDeviceRevoked, outbox.AggregateTypeIdentityDevice,
			device.CredentialID, devicePayload, occurredAt, occurredAt,
		)
		if eventErr != nil {
			return eventErr
		}
		if _, eventErr = transaction.OutboxEvents().Insert(ctx, deviceEvent); eventErr != nil {
			return eventErr
		}
	}
	return nil
}

func (service *Service) appendAuditEvent(
	ctx context.Context,
	transaction IdentityTransaction,
	input audit.EventInput,
) error {
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	event, err := service.audit.Prepare(head, input)
	if err != nil {
		return err
	}
	next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: event})
	if err != nil {
		return err
	}
	progress, err := transaction.AuditCheckpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	health, err := service.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, input.OccurredAt)
	if err != nil || !health.CheckpointDue() {
		return err
	}
	checkpoint, err := service.audit.PrepareCheckpoint(next, input.OccurredAt)
	if err != nil {
		return err
	}
	return transaction.AuditCheckpoints().AppendPendingCheckpoint(ctx, checkpoint)
}

func (service *Service) requireHealthyAudit(ctx context.Context, transaction IdentityTransaction) error {
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	progress, err := transaction.AuditCheckpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	health, err := service.checkpointHealth.Evaluate(ctx, head.Sequence(), progress, service.clock.Now())
	if err != nil {
		return err
	}
	if !health.AllowsSensitiveWrites() {
		return audit.ErrSensitiveWriteBlocked
	}
	return nil
}

func deterministicIdentityEvent(message proto.Message) ([]byte, error) {
	if message == nil {
		return nil, ErrIdentityIntegrity
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil || len(encoded) == 0 {
		return nil, ErrIdentityIntegrity
	}
	return encoded, nil
}

func mustAuditActor(kind audit.ActorType, id uuid.UUID) audit.Actor {
	actor, err := audit.NewActor(kind, id.String())
	if err != nil {
		panic("validated identity UUID must create an audit actor")
	}
	return actor
}

func mustAuditTarget(kind audit.TargetType, id uuid.UUID) audit.Target {
	target, err := audit.NewTarget(kind, id.String())
	if err != nil {
		panic("validated identity UUID must create an audit target")
	}
	return target
}

// RotateRecoveryCode replaces the active long-lived code and replays only the exact committed envelope.
func (service *Service) RotateRecoveryCode(
	ctx context.Context,
	command RotateRecoveryCodeCommand,
) (RotateRecoveryCodeResult, error) {
	if service == nil || service.audit == nil || ctx == nil || !command.OperationID.Valid() ||
		command.CSRFToken == "" || command.RequestID == "" {
		return RotateRecoveryCodeResult{}, ErrInvalidIdentityRequest
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	var user User
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		currentUser, currentDevice, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		if _, authErr := service.authenticateSensitiveDevice(currentDevice, command.DeviceToken, command.CSRFToken); authErr != nil {
			return authErr
		}
		if currentUser.Snapshot().Status != UserStatusActive {
			return ErrUserStatus
		}
		user = currentUser
		return nil
	})
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	digest := digestIdentityRequest("identity.recovery_code_rotation.v1")
	binding := identityResultBinding(
		secretresult.ScopeIdentityRecoveryCodeRotation, user.Snapshot().ID, command.OperationID, digest,
		secretresult.ResultTypeIdentityRecoveryCode,
	)
	issued, err := service.recovery.Issue(ctx, user.Snapshot().ID, service.clock.Now())
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	plaintext, err := json.Marshal(onboardingResultEnvelope{RecoveryCode: issued.Code})
	if err != nil {
		return RotateRecoveryCodeResult{}, ErrIdentityIntegrity
	}
	defer clear(plaintext)
	resultID, err := uuid.NewV7()
	if err != nil {
		return RotateRecoveryCodeResult{}, ErrInvalidIdentityRequest
	}
	prepared, err := service.results.PrepareAvailable(resultID, binding, plaintext, identitySecretTTL)
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return RotateRecoveryCodeResult{}, ErrInvalidIdentityRequest
	}
	var committed secretresult.Result
	var opened []byte
	var replayed bool
	defer clear(opened)
	err = service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		currentUser, currentDevice, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil || currentUser.Snapshot().ID != user.Snapshot().ID || currentUser.Snapshot().Status != UserStatusActive {
			return ErrDeviceAuthentication
		}
		authorization, authErr := service.authenticateSensitiveDevice(currentDevice, command.DeviceToken, command.CSRFToken)
		if authErr != nil {
			return authErr
		}
		if healthErr := service.requireHealthyAudit(ctx, transaction); healthErr != nil {
			return healthErr
		}
		stored, resultErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if resultErr == nil {
			if _, resultErr = stored.Resolve(binding, service.clock.Now()); resultErr != nil {
				return resultErr
			}
			grant, grantErr := service.devices.resultCapability(
				authorization, currentDevice, stored.Snapshot().ID, stored.Snapshot().SecretExpiresAt,
			)
			if grantErr != nil {
				return grantErr
			}
			opened, resultErr = service.results.OpenAuthorizedResult(stored, binding, grant)
			committed, replayed = stored, true
			return resultErr
		}
		if !errors.Is(resultErr, secretresult.ErrNotFound) {
			return resultErr
		}
		currentRecovery, sourceErr := transaction.RecoveryCredentials().GetActiveForUserForUpdate(ctx, currentUser.Snapshot().ID)
		if sourceErr != nil {
			return sourceErr
		}
		revokedRecovery, sourceErr := currentRecovery.Revoke(RecoveryRevokeRotated, service.clock.Now())
		if sourceErr != nil {
			return sourceErr
		}
		if _, sourceErr = transaction.RecoveryCredentials().RevokeCAS(ctx, currentRecovery, revokedRecovery); sourceErr != nil {
			return sourceErr
		}
		if _, sourceErr = transaction.RecoveryCredentials().Insert(ctx, issued.Credential); sourceErr != nil {
			return sourceErr
		}
		stored, sourceErr = transaction.SecretResults().InsertAvailable(ctx, prepared)
		if sourceErr != nil {
			return sourceErr
		}
		detailDigest := digestIdentityRequest("identity.recovery_code_rotation.audit.v1")
		if sourceErr = service.appendAuditEvent(ctx, transaction, audit.EventInput{
			EventID: eventID, RequestID: command.RequestID, OccurredAt: service.clock.Now(),
			Actor:  mustAuditActor(audit.ActorUser, currentUser.Snapshot().ID),
			Target: mustAuditTarget(audit.TargetUser, currentUser.Snapshot().ID),
			Action: audit.ActionRecoveryCodeRotated, ReasonCode: "user_requested", DetailDigest: detailDigest[:],
		}); sourceErr != nil {
			return sourceErr
		}
		grant, sourceErr := service.devices.resultCapability(
			authorization, currentDevice, stored.Snapshot().ID, stored.Snapshot().SecretExpiresAt,
		)
		if sourceErr != nil {
			return sourceErr
		}
		opened, sourceErr = service.results.OpenAuthorizedResult(stored, binding, grant)
		committed = stored
		return sourceErr
	})
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	envelope, err := decodeOnboardingEnvelope(opened)
	if err != nil {
		return RotateRecoveryCodeResult{}, err
	}
	return RotateRecoveryCodeResult{
		Operation: operationResult(command.OperationID, committed, replayed), RecoveryCode: envelope.RecoveryCode,
	}, nil
}

// ConfirmSecretReceipt erases an exact user-owned envelope after current-device and CSRF reauthentication.
func (service *Service) ConfirmSecretReceipt(
	ctx context.Context,
	command ConfirmSecretReceiptCommand,
) (ConfirmSecretReceiptResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() || command.ResultID == uuid.Nil || command.CSRFToken == "" {
		return ConfirmSecretReceiptResult{}, ErrInvalidIdentityRequest
	}
	scope, resultType, err := identitySecretOperationBinding(command.Operation)
	if err != nil {
		return ConfirmSecretReceiptResult{}, err
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return ConfirmSecretReceiptResult{}, err
	}
	var confirmed secretresult.Result
	err = service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		user, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return ErrDeviceAuthentication
		}
		userStatus := user.Snapshot().Status
		// A new user must be able to erase the bootstrap credential envelope before onboarding completes.
		if command.Operation == IdentitySecretOperationBootstrap {
			if userStatus != UserStatusOnboarding && userStatus != UserStatusActive {
				return ErrDeviceAuthentication
			}
		} else if userStatus != UserStatusActive {
			return ErrDeviceAuthentication
		}
		authorization, authErr := service.authenticateSensitiveDevice(device, command.DeviceToken, command.CSRFToken)
		if authErr != nil {
			return authErr
		}
		key := secretresult.Key{Scope: scope, ActorID: user.Snapshot().ID, OperationID: command.OperationID}
		result, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, key)
		if getErr != nil {
			return getErr
		}
		binding := result.Snapshot().Binding
		if result.Snapshot().ID != command.ResultID || binding.ResultType != resultType || binding.ResultVersion != identityResultVersion {
			return secretresult.ErrReplayUnauthorized
		}
		grant, grantErr := service.devices.resultCapability(
			authorization, device, result.Snapshot().ID, result.Snapshot().SecretExpiresAt,
		)
		if grantErr != nil {
			return grantErr
		}
		confirmed, grantErr = service.results.ConfirmDeviceAuthorizedResult(
			ctx, transaction.SecretResults(), result, binding, grant,
		)
		return grantErr
	})
	if err != nil {
		return ConfirmSecretReceiptResult{}, err
	}
	return ConfirmSecretReceiptResult{Confirmed: confirmed.Snapshot().Status == secretresult.StatusConfirmed}, nil
}

func identitySecretOperationBinding(operation IdentitySecretOperation) (secretresult.Scope, secretresult.ResultType, error) {
	switch operation {
	case IdentitySecretOperationBootstrap:
		return secretresult.ScopeIdentityBootstrap, secretresult.ResultTypeIdentityDeviceCredential, nil
	case IdentitySecretOperationOnboarding:
		return secretresult.ScopeIdentityOnboarding, secretresult.ResultTypeIdentityRecoveryCode, nil
	case IdentitySecretOperationRecovery:
		return secretresult.ScopeIdentityRecovery, secretresult.ResultTypeIdentityRecoveryBundle, nil
	case IdentitySecretOperationRecoveryCodeRotation:
		return secretresult.ScopeIdentityRecoveryCodeRotation, secretresult.ResultTypeIdentityRecoveryCode, nil
	default:
		return "", "", ErrInvalidIdentityRequest
	}
}

// ListDevices authenticates a read without Redis or CSRF and returns only redacted summaries.
func (service *Service) ListDevices(ctx context.Context, command ListDevicesCommand) (ListDevicesResult, error) {
	if service == nil || ctx == nil {
		return ListDevicesResult{}, ErrInvalidIdentityRequest
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return ListDevicesResult{}, err
	}
	pageSize := command.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	var summaries []DeviceSummary
	err = service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		user, current, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil || user.Snapshot().Status != UserStatusActive {
			return ErrDeviceAuthentication
		}
		if _, getErr = service.devices.Authenticate(current, command.DeviceToken); getErr != nil {
			return getErr
		}
		request, requestErr := NewDeviceListRequest(
			user.Snapshot().ID, command.IncludeRevoked, command.After, pageSize, service.clock.Now(),
		)
		if requestErr != nil {
			return requestErr
		}
		summaries, getErr = transaction.Devices().List(ctx, request)
		for index := range summaries {
			if summaries[index].UserID() != user.Snapshot().ID {
				return ErrIdentityIntegrity
			}
			summaries[index] = summaries[index].MarkCurrent(credentialID)
		}
		return getErr
	})
	if err != nil {
		return ListDevicesResult{}, err
	}
	return ListDevicesResult{Devices: summaries, NextCursor: NextDeviceCursor(summaries)}, nil
}

// RevokeDevice revokes one owned active credential and emits audit/outbox atomically with generation invalidation.
func (service *Service) RevokeDevice(ctx context.Context, command RevokeDeviceCommand) (RevokeDeviceResult, error) {
	reason := strings.TrimSpace(command.Reason)
	if service == nil || service.audit == nil || ctx == nil || command.CSRFToken == "" || command.CredentialID == uuid.Nil ||
		command.RequestID == "" || reason == "" || reason != command.Reason || len(reason) > 256 {
		return RevokeDeviceResult{}, ErrInvalidIdentityRequest
	}
	currentID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return RevokeDeviceResult{}, err
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return RevokeDeviceResult{}, ErrInvalidIdentityRequest
	}
	var currentRevoked bool
	err = service.unitOfWork.RunIdentity(ctx, func(ctx context.Context, transaction IdentityTransaction) error {
		user, current, getErr := transaction.Devices().GetIdentityForUpdate(ctx, currentID)
		if getErr != nil || user.Snapshot().Status != UserStatusActive {
			return ErrDeviceAuthentication
		}
		if _, getErr = service.authenticateSensitiveDevice(current, command.DeviceToken, command.CSRFToken); getErr != nil {
			return getErr
		}
		if healthErr := service.requireHealthyAudit(ctx, transaction); healthErr != nil {
			return healthErr
		}
		target := current
		if command.CredentialID != currentID {
			target, getErr = transaction.Devices().GetForUpdate(ctx, command.CredentialID)
			if getErr != nil {
				return getErr
			}
		}
		if target.Snapshot().UserID != user.Snapshot().ID {
			return ErrDeviceAuthentication
		}
		if target.State(service.clock.Now()) == DeviceStateRevoked {
			currentRevoked = target.Snapshot().CredentialID == currentID
			return nil
		}
		next, revokeErr := target.Revoke(DeviceRevokeUserRequested, service.clock.Now())
		if revokeErr != nil {
			return revokeErr
		}
		stored, revokeErr := transaction.Devices().RevokeCAS(ctx, target, next)
		if revokeErr != nil {
			return revokeErr
		}
		payload, revokeErr := deterministicIdentityEvent(&identityv1.IdentityDeviceRevokedEvent{
			SchemaVersion: 1, EventId: eventID.String(), RequestId: command.RequestID,
			OccurredAt: timestamppb.New(service.clock.Now()), UserId: user.Snapshot().ID.String(),
			DeviceCredentialId: stored.Snapshot().CredentialID.String(),
			Reason:             identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_USER_REQUESTED,
		})
		if revokeErr != nil {
			return revokeErr
		}
		detailDigest := sha256.Sum256(append(payload, []byte(reason)...))
		if revokeErr = service.appendAuditEvent(ctx, transaction, audit.EventInput{
			EventID: eventID, RequestID: command.RequestID, OccurredAt: service.clock.Now(),
			Actor:  mustAuditActor(audit.ActorUser, user.Snapshot().ID),
			Target: mustAuditTarget(audit.TargetDevice, stored.Snapshot().CredentialID),
			Action: audit.ActionDeviceRevoked, ReasonCode: "user_requested", DetailDigest: detailDigest[:],
		}); revokeErr != nil {
			return revokeErr
		}
		outboxEvent, revokeErr := outbox.NewEvent(
			eventID, outbox.EventTypeIdentityDeviceRevoked, outbox.AggregateTypeIdentityDevice,
			stored.Snapshot().CredentialID, payload, service.clock.Now(), service.clock.Now(),
		)
		if revokeErr != nil {
			return revokeErr
		}
		if _, revokeErr = transaction.OutboxEvents().Insert(ctx, outboxEvent); revokeErr != nil {
			return revokeErr
		}
		currentRevoked = stored.Snapshot().CredentialID == currentID
		return nil
	})
	if err != nil {
		return RevokeDeviceResult{}, err
	}
	instruction := CredentialInstructionKeep
	if currentRevoked {
		instruction = CredentialInstructionClear
	}
	return RevokeDeviceResult{CurrentDeviceRevoked: currentRevoked, CredentialInstruction: instruction}, nil
}
