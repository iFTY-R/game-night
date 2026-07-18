package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
)

const (
	// identityResultVersion fixes the encrypted bootstrap/onboarding envelope schema.
	identityResultVersion = 1
	// identitySecretTTL bounds response-loss replay of device and recovery plaintext.
	identitySecretTTL = 10 * time.Minute
)

// CredentialInstruction tells the transport whether an existing device Cookie remains usable.
type CredentialInstruction uint8

const (
	CredentialInstructionKeep CredentialInstruction = iota + 1
	CredentialInstructionClear
)

// OperationResult is the domain representation of the common RPC idempotency result metadata.
type OperationResult struct {
	OperationID     idempotency.OperationID
	ResultID        uuid.UUID
	SecretExpiresAt time.Time
	Replayed        bool
}

// BeginIdentityBootstrapCommand contains transport-validated Origin and client request-flow binding.
type BeginIdentityBootstrapCommand struct {
	CanonicalOrigin string
	RequestFlowID   challenge.RequestFlowID
	ClientIP        string
}

// BootstrapIdentityCommand either creates a new pending identity or inspects an existing device.
type BootstrapIdentityCommand struct {
	CanonicalOrigin      string
	RequestFlowID        challenge.RequestFlowID
	ChallengeCredentials challenge.Credentials
	OperationID          idempotency.OperationID
	ClientIP             string
	DeviceLabel          string
	DeviceToken          string
	CSRFToken            string
}

// BootstrapIdentityResult carries the authoritative user/device state and optional Cookie material.
type BootstrapIdentityResult struct {
	Operation             OperationResult
	User                  User
	Device                DeviceCredential
	DeviceSecrets         *DeviceCookieWrite
	CredentialInstruction CredentialInstruction
	AccountInstruction    AccountInstruction
}

// CompleteOnboardingCommand binds a username claim to current-device and CSRF authentication.
type CompleteOnboardingCommand struct {
	DeviceToken string
	CSRFToken   string
	ClientIP    string
	Username    string
	OperationID idempotency.OperationID
}

// CompleteOnboardingResult contains the one-time recovery code and active user state.
type CompleteOnboardingResult struct {
	Operation    OperationResult
	User         User
	RecoveryCode string
}

// GetCurrentIdentityCommand authenticates one device without depending on Redis availability.
type GetCurrentIdentityCommand struct {
	DeviceToken string
}

// GetCurrentIdentityResult returns authenticated state without rotating credentials on a non-replayable read.
type GetCurrentIdentityResult struct {
	User                  User
	Device                DeviceCredential
	CredentialInstruction CredentialInstruction
	AccountInstruction    AccountInstruction
}

// ChangeUsernameCommand requires current device, CSRF, and all three username limiter dimensions.
type ChangeUsernameCommand struct {
	DeviceToken string
	CSRFToken   string
	ClientIP    string
	Username    string
}

// ChangeUsernameResult returns the committed active user aggregate.
type ChangeUsernameResult struct {
	User User
}

// Service orchestrates identity aggregates while keeping transport and PostgreSQL types outside the domain.
type Service struct {
	challenges *ChallengeService
	devices    *DeviceService
	recovery   *RecoveryCodeService
	results    *secretresult.Service
	unitOfWork IdentityUnitOfWork
	limiter    ratelimit.RateLimiter
	usernames  identifier.UsernameValidator
	clock      clock.Clock
}

// NewService requires every security and persistence dependency so protected methods fail closed when wiring is absent.
func NewService(
	challenges *ChallengeService,
	devices *DeviceService,
	recovery *RecoveryCodeService,
	results *secretresult.Service,
	unitOfWork IdentityUnitOfWork,
	limiter ratelimit.RateLimiter,
	usernames identifier.UsernameValidator,
	source clock.Clock,
) (*Service, error) {
	if challenges == nil || devices == nil || recovery == nil || results == nil || unitOfWork == nil ||
		limiter == nil || source == nil {
		return nil, ErrInvalidIdentityRequest
	}
	return &Service{
		challenges: challenges, devices: devices, recovery: recovery, results: results,
		unitOfWork: unitOfWork, limiter: limiter, usernames: usernames, clock: source,
	}, nil
}

// BeginIdentityBootstrap creates and persists a five-minute dual-proof challenge.
func (service *Service) BeginIdentityBootstrap(ctx context.Context, command BeginIdentityBootstrapCommand) (IssuedChallenge, error) {
	if service == nil || ctx == nil {
		return IssuedChallenge{}, ErrInvalidIdentityRequest
	}
	if err := service.consumeBootstrapLimit(ctx, command.ClientIP); err != nil {
		return IssuedChallenge{}, err
	}
	issued, err := service.challenges.Issue(
		ChallengePurposeBootstrap, command.CanonicalOrigin, command.RequestFlowID, challenge.DefaultMaxAttempts,
	)
	if err != nil {
		return IssuedChallenge{}, err
	}
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		return transaction.Challenges().Insert(ctx, issued.Challenge)
	})
	if err != nil {
		return IssuedChallenge{}, err
	}
	return issued, nil
}

// BootstrapIdentity consumes its challenge once; exact retries recover the same encrypted device credentials.
func (service *Service) BootstrapIdentity(ctx context.Context, command BootstrapIdentityCommand) (BootstrapIdentityResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() {
		return BootstrapIdentityResult{}, ErrInvalidIdentityRequest
	}
	if command.DeviceToken != "" {
		return service.bootstrapExistingIdentity(ctx, command)
	}
	label, err := normalizeDeviceLabel(command.DeviceLabel)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	digest := digestIdentityRequest("identity.bootstrap.v1", label)
	var response BootstrapIdentityResult
	var result secretresult.Result
	var binding secretresult.Binding
	var replayed bool
	authorization, err := service.challenges.AuthorizePersistent(
		ctx, service.unitOfWork, ChallengePurposeBootstrap, command.CanonicalOrigin, command.RequestFlowID,
		command.ChallengeCredentials, command.OperationID, digest,
		func(ctx context.Context, transaction ChallengeTransaction, record Challenge, authorization challenge.Authorization) (AuthorizedChallengeCompletion, error) {
			binding = identityResultBinding(
				secretresult.ScopeIdentityBootstrap, record.Snapshot().ID, command.OperationID, digest,
				secretresult.ResultTypeIdentityDeviceCredential,
			)
			if authorization.Kind() == challenge.AuthorizeExactReplay {
				stored, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
				if getErr != nil {
					return AuthorizedChallengeCompletion{}, getErr
				}
				if _, resolveErr := stored.Resolve(binding, service.clock.Now()); resolveErr != nil {
					return AuthorizedChallengeCompletion{}, resolveErr
				}
				result = stored
				replayed = true
				return NoReplayCompletion(), nil
			}

			userID, idErr := uuid.NewV7()
			if idErr != nil {
				return AuthorizedChallengeCompletion{}, ErrInvalidUserInput
			}
			user, newErr := NewOnboardingUser(userID, service.clock.Now())
			if newErr != nil {
				return AuthorizedChallengeCompletion{}, newErr
			}
			issuedDevice, issueErr := service.devices.Issue(userID, label)
			if issueErr != nil {
				return AuthorizedChallengeCompletion{}, issueErr
			}
			plaintext, marshalErr := encodeBootstrapEnvelope(issuedDevice)
			if marshalErr != nil {
				return AuthorizedChallengeCompletion{}, marshalErr
			}
			defer clear(plaintext)
			resultID, idErr := uuid.NewV7()
			if idErr != nil {
				return AuthorizedChallengeCompletion{}, ErrInvalidIdentityRequest
			}
			prepared, prepareErr := service.results.PrepareAvailable(resultID, binding, plaintext, identitySecretTTL)
			if prepareErr != nil {
				return AuthorizedChallengeCompletion{}, prepareErr
			}
			storedUser, insertErr := transaction.Users().Insert(ctx, user)
			if insertErr != nil {
				return AuthorizedChallengeCompletion{}, insertErr
			}
			storedDevice, insertErr := transaction.Devices().Insert(ctx, issuedDevice.Credential)
			if insertErr != nil {
				return AuthorizedChallengeCompletion{}, insertErr
			}
			storedResult, insertErr := transaction.SecretResults().InsertAvailable(ctx, prepared)
			if insertErr != nil {
				return AuthorizedChallengeCompletion{}, insertErr
			}
			result = storedResult
			cookieWrite, cookieErr := service.devices.cookieWrite(storedDevice, issuedDevice.secrets)
			if cookieErr != nil {
				return AuthorizedChallengeCompletion{}, cookieErr
			}
			response = BootstrapIdentityResult{
				User: storedUser, Device: storedDevice, DeviceSecrets: &cookieWrite,
				CredentialInstruction: CredentialInstructionKeep,
			}
			return NewReplayCompletion(storedResult)
		},
	)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	if replayed {
		return service.openBootstrapReplay(
			ctx, command.OperationID, binding, result, authorization, command.DeviceToken, command.CSRFToken,
		)
	}
	response.Operation = operationResult(command.OperationID, result, false)
	return response, nil
}

// CompleteOnboarding atomically claims a username, activates the user, creates recovery state, and stores its envelope.
func (service *Service) CompleteOnboarding(ctx context.Context, command CompleteOnboardingCommand) (CompleteOnboardingResult, error) {
	if service == nil || ctx == nil || !command.OperationID.Valid() || command.CSRFToken == "" {
		return CompleteOnboardingResult{}, ErrInvalidIdentityRequest
	}
	username, err := service.usernames.Parse(command.Username)
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	if err := service.consumeUsernameLimit(ctx, ratelimit.OperationUsernameClaim, command.ClientIP, credentialID, username.Key()); err != nil {
		return CompleteOnboardingResult{}, err
	}
	digest := digestIdentityRequest("identity.onboarding.v1", username.Display(), username.Key())

	user, binding, existing, existingPlaintext, err := service.resolveOnboardingResult(
		ctx, credentialID, command.DeviceToken, command.CSRFToken, command.OperationID, digest,
	)
	defer clear(existingPlaintext)
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	if existing.Snapshot().ID != uuid.Nil {
		return onboardingResponse(command.OperationID, existing, user, true, existingPlaintext)
	}

	issuedRecovery, err := service.recovery.Issue(ctx, user.Snapshot().ID, service.clock.Now())
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	plaintext, err := json.Marshal(onboardingResultEnvelope{RecoveryCode: issuedRecovery.Code})
	if err != nil {
		return CompleteOnboardingResult{}, ErrInvalidIdentityRequest
	}
	defer clear(plaintext)
	resultID, err := uuid.NewV7()
	if err != nil {
		return CompleteOnboardingResult{}, ErrInvalidIdentityRequest
	}
	prepared, err := service.results.PrepareAvailable(resultID, binding, plaintext, identitySecretTTL)
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	var committed secretresult.Result
	var committedUser User
	var committedPlaintext []byte
	var replayed bool
	defer clear(committedPlaintext)
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		currentUser, currentDevice, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		currentAuthorization, authErr := service.authenticateSensitiveDevice(currentDevice, command.DeviceToken, command.CSRFToken)
		if authErr != nil {
			return authErr
		}
		if currentUser.Snapshot().ID != user.Snapshot().ID {
			return ErrDeviceAuthentication
		}
		stored, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if getErr == nil {
			if _, resolveErr := stored.Resolve(binding, service.clock.Now()); resolveErr != nil {
				return resolveErr
			}
			capability, capabilityErr := service.devices.resultCapability(currentAuthorization,
				currentDevice, stored.Snapshot().ID, stored.Snapshot().SecretExpiresAt,
			)
			if capabilityErr != nil {
				return capabilityErr
			}
			opened, openErr := service.results.OpenAuthorizedResult(stored, binding, capability)
			if openErr != nil {
				return openErr
			}
			committed, committedUser, committedPlaintext, replayed = stored, currentUser, opened, true
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		nextUser, transitionErr := currentUser.CompleteOnboarding(username, service.clock.Now())
		if transitionErr != nil {
			return transitionErr
		}
		claim, claimErr := NewActiveUsernameClaim(username, currentUser.Snapshot().ID, service.clock.Now())
		if claimErr != nil {
			return claimErr
		}
		if _, claimErr = transaction.UsernameClaims().Claim(ctx, claim, service.clock.Now()); claimErr != nil {
			return claimErr
		}
		storedUser, transitionErr := transaction.Users().CompleteOnboardingCAS(ctx, currentUser, nextUser)
		if transitionErr != nil {
			return transitionErr
		}
		if _, insertErr := transaction.RecoveryCredentials().Insert(ctx, issuedRecovery.Credential); insertErr != nil {
			return insertErr
		}
		storedResult, insertErr := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if insertErr != nil {
			return insertErr
		}
		capability, capabilityErr := service.devices.resultCapability(currentAuthorization,
			currentDevice, storedResult.Snapshot().ID, storedResult.Snapshot().SecretExpiresAt,
		)
		if capabilityErr != nil {
			return capabilityErr
		}
		opened, openErr := service.results.OpenAuthorizedResult(storedResult, binding, capability)
		if openErr != nil {
			return openErr
		}
		committed, committedUser, committedPlaintext = storedResult, storedUser, opened
		return nil
	})
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	return onboardingResponse(command.OperationID, committed, committedUser, replayed, committedPlaintext)
}

// GetCurrentIdentity authenticates without Redis; token rotation stays on an idempotent challenge-backed path.
func (service *Service) GetCurrentIdentity(ctx context.Context, command GetCurrentIdentityCommand) (GetCurrentIdentityResult, error) {
	if service == nil || ctx == nil {
		return GetCurrentIdentityResult{}, ErrInvalidIdentityRequest
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return GetCurrentIdentityResult{}, err
	}
	var response GetCurrentIdentityResult
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		user, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		verification, verifyErr := service.devices.Verify(device, command.DeviceToken)
		if verifyErr != nil {
			return verifyErr
		}
		if instruction := verification.AccountInstruction(); instruction != AccountInstructionNone {
			response = GetCurrentIdentityResult{
				CredentialInstruction: CredentialInstructionClear, AccountInstruction: instruction,
			}
			return nil
		}
		authorization, authErr := service.devices.Authenticate(device, command.DeviceToken)
		if authErr != nil {
			return collapseUnavailableDevice(authErr)
		}
		if user.Snapshot().Status != UserStatusOnboarding && user.Snapshot().Status != UserStatusActive {
			return ErrUserStatus
		}
		current := device
		if authorization.SecretKind() == DeviceSecretCurrent && service.clock.Now().After(device.Snapshot().LastSeenAt) {
			touched, touchErr := device.Touch(authorization, service.clock.Now())
			if touchErr != nil {
				return touchErr
			}
			stored, persistErr := transaction.Devices().TouchCAS(ctx, device, touched)
			if persistErr != nil {
				return persistErr
			}
			current = stored
		}
		response.User = user
		response.Device = current
		response.CredentialInstruction = CredentialInstructionKeep
		return nil
	})
	return response, err
}

// ChangeUsername claims the new key and reserves the old key in one database transaction.
func (service *Service) ChangeUsername(ctx context.Context, command ChangeUsernameCommand) (ChangeUsernameResult, error) {
	if service == nil || ctx == nil || command.CSRFToken == "" {
		return ChangeUsernameResult{}, ErrInvalidIdentityRequest
	}
	username, err := service.usernames.Parse(command.Username)
	if err != nil {
		return ChangeUsernameResult{}, err
	}
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return ChangeUsernameResult{}, err
	}
	if err := service.consumeUsernameLimit(ctx, ratelimit.OperationUsernameClaim, command.ClientIP, credentialID, username.Key()); err != nil {
		return ChangeUsernameResult{}, err
	}
	var response ChangeUsernameResult
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		user, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		if _, authErr := service.authenticateSensitiveDevice(device, command.DeviceToken, command.CSRFToken); authErr != nil {
			return authErr
		}
		plan, planErr := user.PlanUsernameChange(username, service.clock.Now())
		if planErr != nil {
			return planErr
		}
		claim, claimErr := NewActiveUsernameClaim(username, user.Snapshot().ID, plan.ChangedAt)
		if claimErr != nil {
			return claimErr
		}
		if _, claimErr = transaction.UsernameClaims().Claim(ctx, claim, plan.ChangedAt); claimErr != nil {
			return claimErr
		}
		previousClaim, claimErr := transaction.UsernameClaims().GetForUpdate(ctx, plan.PreviousUsernameKey)
		if claimErr != nil {
			return claimErr
		}
		storedUser, transitionErr := transaction.Users().ChangeUsernameCAS(ctx, user, plan.Next)
		if transitionErr != nil {
			return transitionErr
		}
		reserved, reserveErr := previousClaim.Reserve(plan.ReservePreviousUntil, plan.ChangedAt)
		if reserveErr != nil {
			return reserveErr
		}
		if _, reserveErr = transaction.UsernameClaims().ReserveCAS(ctx, previousClaim, reserved); reserveErr != nil {
			return reserveErr
		}
		response.User = storedUser
		return nil
	})
	return response, err
}

func (service *Service) bootstrapExistingIdentity(ctx context.Context, command BootstrapIdentityCommand) (BootstrapIdentityResult, error) {
	credentialID, err := CredentialIDFromToken(command.DeviceToken)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	digest := digestIdentityRequest("identity.bootstrap.existing.v1", credentialID.String())
	var response BootstrapIdentityResult
	var result secretresult.Result
	var binding secretresult.Binding
	var replayed bool
	challengeAuthorization, err := service.challenges.AuthorizePersistent(
		ctx, service.unitOfWork, ChallengePurposeBootstrap, command.CanonicalOrigin, command.RequestFlowID,
		command.ChallengeCredentials, command.OperationID, digest,
		func(ctx context.Context, transaction ChallengeTransaction, record Challenge, authorization challenge.Authorization) (AuthorizedChallengeCompletion, error) {
			binding = identityResultBinding(
				secretresult.ScopeIdentityBootstrap, record.Snapshot().ID, command.OperationID, digest,
				secretresult.ResultTypeIdentityDeviceCredential,
			)
			if authorization.Kind() == challenge.AuthorizeExactReplay {
				stored, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
				if getErr != nil {
					return AuthorizedChallengeCompletion{}, getErr
				}
				if _, resolveErr := stored.Resolve(binding, service.clock.Now()); resolveErr != nil {
					return AuthorizedChallengeCompletion{}, resolveErr
				}
				result = stored
				replayed = true
				return NoReplayCompletion(), nil
			}
			user, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
			if getErr != nil {
				return AuthorizedChallengeCompletion{}, getErr
			}
			verification, verifyErr := service.devices.Verify(device, command.DeviceToken)
			if verifyErr != nil {
				return AuthorizedChallengeCompletion{}, verifyErr
			}
			if instruction := verification.AccountInstruction(); instruction != AccountInstructionNone {
				response = BootstrapIdentityResult{
					CredentialInstruction: CredentialInstructionClear, AccountInstruction: instruction,
				}
				return NoReplayCompletion(), nil
			}
			deviceAuthorization, authErr := service.devices.Authenticate(device, command.DeviceToken)
			if authErr != nil {
				return AuthorizedChallengeCompletion{}, collapseUnavailableDevice(authErr)
			}
			if user.Snapshot().Status != UserStatusOnboarding && user.Snapshot().Status != UserStatusActive {
				return AuthorizedChallengeCompletion{}, ErrUserStatus
			}
			current := device
			if deviceAuthorization.SecretKind() == DeviceSecretCurrent && device.RotationDue(service.clock.Now()) {
				rotated, rotateErr := service.devices.Rotate(device, deviceAuthorization, command.CSRFToken)
				if rotateErr != nil {
					return AuthorizedChallengeCompletion{}, rotateErr
				}
				stored, persistErr := transaction.Devices().RotateCAS(ctx, device, rotated.Credential)
				if persistErr != nil {
					return AuthorizedChallengeCompletion{}, persistErr
				}
				current = stored
				cookieWrite, cookieErr := service.devices.cookieWrite(stored, rotated.secrets)
				if cookieErr != nil {
					return AuthorizedChallengeCompletion{}, cookieErr
				}
				response.DeviceSecrets = &cookieWrite
				plaintext, marshalErr := encodeBootstrapEnvelope(rotated)
				if marshalErr != nil {
					return AuthorizedChallengeCompletion{}, marshalErr
				}
				defer clear(plaintext)
				resultID, idErr := uuid.NewV7()
				if idErr != nil {
					return AuthorizedChallengeCompletion{}, ErrInvalidIdentityRequest
				}
				prepared, prepareErr := service.results.PrepareAvailable(resultID, binding, plaintext, identitySecretTTL)
				if prepareErr != nil {
					return AuthorizedChallengeCompletion{}, prepareErr
				}
				storedResult, insertErr := transaction.SecretResults().InsertAvailable(ctx, prepared)
				if insertErr != nil {
					return AuthorizedChallengeCompletion{}, insertErr
				}
				result = storedResult
				response.User = user
				response.Device = current
				response.CredentialInstruction = CredentialInstructionKeep
				return NewReplayCompletion(storedResult)
			}
			response.User = user
			response.Device = current
			response.CredentialInstruction = CredentialInstructionKeep
			return NoReplayCompletion(), nil
		},
	)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	if replayed {
		return service.openBootstrapReplay(
			ctx, command.OperationID, binding, result, challengeAuthorization, command.DeviceToken, command.CSRFToken,
		)
	}
	if result.Snapshot().ID != uuid.Nil {
		response.Operation = operationResult(command.OperationID, result, false)
	} else {
		response.Operation = OperationResult{OperationID: command.OperationID}
	}
	return response, nil
}

func (service *Service) openBootstrapReplay(
	ctx context.Context,
	operationID idempotency.OperationID,
	binding secretresult.Binding,
	result secretresult.Result,
	authorization challenge.Authorization,
	submittedDeviceToken, submittedCSRFToken string,
) (BootstrapIdentityResult, error) {
	snapshot := result.Snapshot()
	plaintext, err := service.results.Open(ctx, identityResultUnitOfWork{service.unitOfWork}, snapshot.ID, binding, authorization)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	defer clear(plaintext)
	envelope, err := decodeBootstrapEnvelope(plaintext)
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	credentialID, err := uuid.Parse(envelope.CredentialID)
	if err != nil || credentialID.String() != envelope.CredentialID {
		return BootstrapIdentityResult{}, ErrIdentityIntegrity
	}
	var response BootstrapIdentityResult
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		user, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		// Generation one is issued under challenge authority; rotations must reauthenticate this retry's device proof.
		if envelope.Generation > 1 {
			submittedAuthorization, authErr := service.devices.Authenticate(device, submittedDeviceToken)
			if authErr != nil {
				return collapseUnavailableDevice(authErr)
			}
			if csrfErr := service.devices.VerifyCSRF(device, submittedCSRFToken); csrfErr != nil {
				return csrfErr
			}
			if !submittedAuthorization.authorizesRotationReplay(device, envelope.Generation) {
				return ErrDeviceAuthentication
			}
		}
		cookieWrite, cookieErr := service.devices.cookieWrite(device, issuedDeviceSecrets{
			token: envelope.DeviceToken, csrfToken: envelope.CSRFToken, generation: envelope.Generation,
		})
		if cookieErr != nil {
			return cookieErr
		}
		response.User = user
		response.Device = device
		response.DeviceSecrets = &cookieWrite
		response.CredentialInstruction = CredentialInstructionKeep
		return nil
	})
	if err != nil {
		return BootstrapIdentityResult{}, err
	}
	response.Operation = operationResult(operationID, result, true)
	return response, nil
}

func (service *Service) resolveOnboardingResult(
	ctx context.Context,
	credentialID uuid.UUID,
	deviceToken, csrfToken string,
	operationID idempotency.OperationID,
	digest idempotency.Digest,
) (User, secretresult.Binding, secretresult.Result, []byte, error) {
	var user User
	var binding secretresult.Binding
	var result secretresult.Result
	var plaintext []byte
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		currentUser, device, getErr := transaction.Devices().GetIdentityForUpdate(ctx, credentialID)
		if getErr != nil {
			return getErr
		}
		currentAuthorization, authErr := service.devices.Authenticate(device, deviceToken)
		if authErr != nil {
			return collapseUnavailableDevice(authErr)
		}
		if csrfErr := service.devices.VerifyCSRF(device, csrfToken); csrfErr != nil {
			return csrfErr
		}
		user = currentUser
		binding = identityResultBinding(
			secretresult.ScopeIdentityOnboarding, currentUser.Snapshot().ID, operationID, digest,
			secretresult.ResultTypeIdentityRecoveryCode,
		)
		stored, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if errors.Is(getErr, secretresult.ErrNotFound) {
			if !currentAuthorization.AllowsSensitiveMutation(device) {
				return ErrDeviceAuthentication
			}
			return nil
		}
		if getErr != nil {
			return getErr
		}
		if _, resolveErr := stored.Resolve(binding, service.clock.Now()); resolveErr != nil {
			return resolveErr
		}
		result = stored
		capability, capabilityErr := service.devices.resultContinuationCapability(
			currentAuthorization, device, stored.Snapshot().ID,
			stored.Snapshot().CompletedAt, stored.Snapshot().SecretExpiresAt,
		)
		if capabilityErr != nil {
			return capabilityErr
		}
		plaintext, getErr = service.results.OpenAuthorizedResult(stored, binding, capability)
		return getErr
	})
	if err != nil {
		clear(plaintext)
	}
	return user, binding, result, plaintext, err
}

func onboardingResponse(
	operationID idempotency.OperationID,
	result secretresult.Result,
	user User,
	replayed bool,
	plaintext []byte,
) (CompleteOnboardingResult, error) {
	envelope, err := decodeOnboardingEnvelope(plaintext)
	if err != nil {
		return CompleteOnboardingResult{}, err
	}
	return CompleteOnboardingResult{
		Operation: operationResult(operationID, result, replayed), User: user, RecoveryCode: envelope.RecoveryCode,
	}, nil
}

func (service *Service) authenticateSensitiveDevice(
	device DeviceCredential,
	deviceToken, csrfToken string,
) (DeviceAuthorization, error) {
	authorization, err := service.devices.Authenticate(device, deviceToken)
	if err != nil {
		return DeviceAuthorization{}, collapseUnavailableDevice(err)
	}
	if !authorization.AllowsSensitiveMutation(device) {
		return DeviceAuthorization{}, ErrDeviceAuthentication
	}
	if err := service.devices.VerifyCSRF(device, csrfToken); err != nil {
		return DeviceAuthorization{}, err
	}
	return authorization, nil
}

func (service *Service) consumeBootstrapLimit(ctx context.Context, clientIP string) error {
	policy, err := ratelimit.PolicyFor(ratelimit.OperationIdentityBootstrap)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	ipKey, err := identityBucketKey(ratelimit.DimensionIP, clientIP)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	return policy.Consume(ctx, service.limiter, ipKey)
}

func (service *Service) consumeUsernameLimit(
	ctx context.Context,
	operation ratelimit.Operation,
	clientIP string,
	credentialID uuid.UUID,
	usernameKey string,
) error {
	policy, err := ratelimit.PolicyFor(operation)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	ipKey, err := identityBucketKey(ratelimit.DimensionIP, clientIP)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	deviceKey, err := identityBucketKey(ratelimit.DimensionDevice, credentialID.String())
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	usernameBucket, err := identityBucketKey(ratelimit.DimensionUsername, usernameKey)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	return policy.Consume(ctx, service.limiter, ipKey, deviceKey, usernameBucket)
}

func identityBucketKey(dimension ratelimit.Dimension, raw string) (ratelimit.BucketKey, error) {
	value, err := ratelimit.NewBucketValue(raw)
	if err != nil {
		return ratelimit.BucketKey{}, err
	}
	return ratelimit.NewBucketKey(dimension, value)
}

func identityResultBinding(
	scope secretresult.Scope,
	actorID uuid.UUID,
	operationID idempotency.OperationID,
	digest idempotency.Digest,
	resultType secretresult.ResultType,
) secretresult.Binding {
	return secretresult.Binding{
		Key:           secretresult.Key{Scope: scope, ActorID: actorID, OperationID: operationID},
		RequestDigest: digest, ResultType: resultType, ResultVersion: identityResultVersion,
	}
}

func operationResult(operationID idempotency.OperationID, result secretresult.Result, replayed bool) OperationResult {
	snapshot := result.Snapshot()
	return OperationResult{
		OperationID: operationID, ResultID: snapshot.ID, SecretExpiresAt: snapshot.SecretExpiresAt, Replayed: replayed,
	}
}

func digestIdentityRequest(domain string, fields ...string) idempotency.Digest {
	hash := sha256.New()
	appendDigestField(hash, domain)
	for _, field := range fields {
		appendDigestField(hash, field)
	}
	var digest idempotency.Digest
	copy(digest[:], hash.Sum(nil))
	return digest
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func appendDigestField(writer digestWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

type bootstrapResultEnvelope struct {
	DeviceToken  string `json:"device_token"`
	CSRFToken    string `json:"csrf_token"`
	CredentialID string `json:"credential_id"`
	Generation   uint64 `json:"generation"`
}

type onboardingResultEnvelope struct {
	RecoveryCode string `json:"recovery_code"`
}

func encodeBootstrapEnvelope(issued IssuedDeviceCredential) ([]byte, error) {
	snapshot := issued.Credential.Snapshot()
	return json.Marshal(bootstrapResultEnvelope{
		DeviceToken: issued.secrets.token, CSRFToken: issued.secrets.csrfToken,
		CredentialID: snapshot.CredentialID.String(), Generation: issued.secrets.generation,
	})
}

func decodeBootstrapEnvelope(plaintext []byte) (bootstrapResultEnvelope, error) {
	var envelope bootstrapResultEnvelope
	if err := decodeIdentityEnvelope(plaintext, &envelope); err != nil || envelope.DeviceToken == "" ||
		envelope.CSRFToken == "" || envelope.CredentialID == "" || envelope.Generation == 0 {
		return bootstrapResultEnvelope{}, ErrIdentityIntegrity
	}
	return envelope, nil
}

func decodeOnboardingEnvelope(plaintext []byte) (onboardingResultEnvelope, error) {
	var envelope onboardingResultEnvelope
	if err := decodeIdentityEnvelope(plaintext, &envelope); err != nil || envelope.RecoveryCode == "" {
		return onboardingResultEnvelope{}, ErrIdentityIntegrity
	}
	return envelope, nil
}

func decodeIdentityEnvelope(plaintext []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrIdentityIntegrity
	}
	return nil
}

func collapseUnavailableDevice(err error) error {
	if errors.Is(err, ErrDeviceUnavailable) {
		return ErrDeviceAuthentication
	}
	return err
}

type identityResultUnitOfWork struct {
	identity IdentityUnitOfWork
}

func (unitOfWork identityResultUnitOfWork) Run(ctx context.Context, work secretresult.TransactionWork) error {
	return unitOfWork.identity.Run(ctx, func(ctx context.Context, transaction ChallengeTransaction) error {
		return work(ctx, transaction.SecretResults())
	})
}
