package admin

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maximumAdminReasonBytes = 512
	defaultAuditPageSize    = 50
	maximumAuditPageSize    = 100
)

// IdentityServiceDependencies keeps PII, authentication, audit, and persistence authority explicit.
type IdentityServiceDependencies struct {
	Clock         clock.Clock
	UnitOfWork    UnitOfWork
	Sessions      *SessionService
	Authorizer    AdminAuthorizer
	Limiter       ratelimit.RateLimiter
	PII           *profile.PIIProtector
	RecoveryCodes *identity.RecoveryCodeService
	Results       *secretresult.Service
	Audit         *audit.Service
}

// IdentityService coordinates administrator profile and user-governance operations.
type IdentityService struct {
	clock         clock.Clock
	unitOfWork    UnitOfWork
	sessions      *SessionService
	authorizer    AdminAuthorizer
	limiter       ratelimit.RateLimiter
	pii           *profile.PIIProtector
	recoveryCodes *identity.RecoveryCodeService
	results       *secretresult.Service
	audit         *audit.Service
}

func NewIdentityService(deps IdentityServiceDependencies) (*IdentityService, error) {
	if deps.Clock == nil || deps.UnitOfWork == nil || deps.Sessions == nil || deps.Limiter == nil ||
		deps.PII == nil || deps.RecoveryCodes == nil || deps.Results == nil || deps.Audit == nil {
		return nil, ErrInvalidInput
	}
	return &IdentityService{
		clock: deps.Clock, unitOfWork: deps.UnitOfWork, sessions: deps.Sessions, authorizer: deps.Authorizer,
		limiter: deps.Limiter, pii: deps.PII, recoveryCodes: deps.RecoveryCodes, results: deps.Results, audit: deps.Audit,
	}, nil
}

// IdentityAuthorization carries transport proofs and the full session used by every privileged command.
type IdentityAuthorization struct {
	Session      Session
	SessionToken string
	CSRFToken    string
	RequestID    string
}

type GetUserCommand struct {
	Authorization IdentityAuthorization
	UserID        uuid.UUID
	Username      string
}

// GetUser resolves exactly one stable user identifier without exposing profile data.
func (service *IdentityService) GetUser(ctx context.Context, command GetUserCommand) (identity.User, error) {
	if (command.UserID == uuid.Nil) == (strings.TrimSpace(command.Username) == "") {
		return identity.User{}, ErrInvalidInput
	}
	if err := service.authorize(command.Authorization, PermissionGetUser); err != nil {
		return identity.User{}, err
	}
	var result identity.User
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		var err error
		if command.UserID != uuid.Nil {
			result, err = transaction.IdentityUsers().GetByID(ctx, command.UserID)
			return err
		}
		username, parseErr := identifier.ParseUsername(command.Username)
		if parseErr != nil {
			return ErrInvalidInput
		}
		result, err = transaction.IdentityUsers().GetByUsernameKey(ctx, username.Key())
		return err
	})
	return result, mapAdminIdentityError(err)
}

type GetRealNameCommand struct {
	Authorization IdentityAuthorization
	UserID        uuid.UUID
	Reason        string
}

// RealNameResult distinguishes an absent optional profile from an empty decrypted value.
type RealNameResult struct {
	UserID           uuid.UUID
	RealName         string
	ProfileVersion   uint64
	UpdatedAt        time.Time
	UpdatedByAdminID uuid.UUID
	Exists           bool
}

// GetRealName commits the disclosure audit before decrypting or returning plaintext.
func (service *IdentityService) GetRealName(ctx context.Context, command GetRealNameCommand) (RealNameResult, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || command.UserID == uuid.Nil {
		return RealNameResult{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionGetRealName); err != nil {
		return RealNameResult{}, err
	}
	if err = service.consumePIILimit(ctx, ratelimit.OperationRealNameRead, command.Authorization.Session, command.UserID.String()); err != nil {
		return RealNameResult{}, err
	}
	var stored profile.UserProfile
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		if _, err := transaction.IdentityUsers().GetByID(ctx, command.UserID); err != nil {
			return err
		}
		stored, err = transaction.Profiles().GetByID(ctx, command.UserID)
		if errors.Is(err, profile.ErrProfileNotFound) {
			return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), audit.ActionRealNameRead, "profile_absent", digestFields(reason, command.UserID.String()))
		}
		if err != nil {
			return err
		}
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), audit.ActionRealNameRead, "admin_requested", digestFields(reason, command.UserID.String(), strconv.FormatUint(stored.ProfileVersion(), 10)))
	})
	if err != nil {
		return RealNameResult{}, mapAdminIdentityError(err)
	}
	if stored.UserID() == uuid.Nil {
		return RealNameResult{UserID: command.UserID}, nil
	}
	realName, err := service.pii.DecryptRealName(command.UserID, stored.EncryptedRealName())
	if err != nil {
		return RealNameResult{}, mapAdminIdentityError(err)
	}
	snapshot := stored.Snapshot()
	return RealNameResult{UserID: command.UserID, RealName: realName, ProfileVersion: snapshot.ProfileVersion, UpdatedAt: snapshot.RealNameUpdatedAt, UpdatedByAdminID: snapshot.RealNameUpdatedBy, Exists: true}, nil
}

type UpdateRealNameCommand struct {
	Authorization IdentityAuthorization
	UserID        uuid.UUID
	RealName      string
	Reason        string
}

// UpdateRealName encrypts before persistence and returns plaintext only after the profile and audit commit.
func (service *IdentityService) UpdateRealName(ctx context.Context, command UpdateRealNameCommand) (RealNameResult, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || command.UserID == uuid.Nil {
		return RealNameResult{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionUpdateRealName); err != nil {
		return RealNameResult{}, err
	}
	if err = service.consumePIILimit(ctx, ratelimit.OperationRealNameUpdate, command.Authorization.Session, command.UserID.String()); err != nil {
		return RealNameResult{}, err
	}
	encrypted, err := service.pii.EncryptRealName(command.UserID, command.RealName)
	if err != nil {
		return RealNameResult{}, mapAdminIdentityError(err)
	}
	var stored profile.UserProfile
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		user, err := transaction.IdentityUsers().GetForUpdate(ctx, command.UserID)
		if err != nil || user.Snapshot().Status == identity.UserStatusDeleted || user.Snapshot().Status == identity.UserStatusOnboarding {
			if err != nil {
				return err
			}
			return identity.ErrUserStatus
		}
		current, getErr := transaction.Profiles().GetForUpdate(ctx, command.UserID)
		if errors.Is(getErr, profile.ErrProfileNotFound) {
			created, createErr := profile.NewUserProfile(command.UserID, encrypted, service.clock.Now(), command.Authorization.Session.Snapshot().AdminID)
			if createErr != nil {
				return createErr
			}
			stored, createErr = transaction.Profiles().Insert(ctx, created)
			if createErr != nil {
				return createErr
			}
		} else if getErr != nil {
			return getErr
		} else {
			next, updateErr := current.UpdateEncrypted(current.ProfileVersion(), encrypted, service.clock.Now(), command.Authorization.Session.Snapshot().AdminID)
			if updateErr != nil {
				return updateErr
			}
			stored, updateErr = transaction.Profiles().UpdateCAS(ctx, current, next)
			if updateErr != nil {
				return updateErr
			}
		}
		storedSnapshot := stored.Snapshot()
		ciphertextDigest := sha256.Sum256(storedSnapshot.RealNameCiphertext)
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), audit.ActionRealNameUpdated, "admin_requested", digestFields(
			reason, command.UserID.String(), strconv.FormatUint(storedSnapshot.ProfileVersion, 10),
			strconv.FormatUint(uint64(storedSnapshot.RealNameKeyVersion), 10), hex.EncodeToString(ciphertextDigest[:]),
		))
	})
	if err != nil {
		return RealNameResult{}, mapAdminIdentityError(err)
	}
	realName, err := service.pii.DecryptRealName(command.UserID, stored.EncryptedRealName())
	if err != nil {
		return RealNameResult{}, mapAdminIdentityError(err)
	}
	snapshot := stored.Snapshot()
	return RealNameResult{UserID: command.UserID, RealName: realName, ProfileVersion: snapshot.ProfileVersion, UpdatedAt: snapshot.RealNameUpdatedAt, UpdatedByAdminID: snapshot.RealNameUpdatedBy, Exists: true}, nil
}

type ProfileExportFilter struct {
	UserIDs  []uuid.UUID
	Statuses []identity.UserStatus
}

type CreateProfileExportCommand struct {
	Authorization IdentityAuthorization
	Filter        ProfileExportFilter
	Fields        []profile.Field
	Reason        string
}

// CreateProfileExport materializes encrypted rows in stable user-ID order inside one audited transaction.
func (service *IdentityService) CreateProfileExport(ctx context.Context, command CreateProfileExportCommand) (profile.ProfileExportContext, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || !validExportFilter(command.Filter) || !validProfileFields(command.Fields) {
		return profile.ProfileExportContext{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionExportProfile); err != nil {
		return profile.ProfileExportContext{}, err
	}
	filterDigest := profileFilterDigest(command.Filter, command.Fields)
	if err = service.consumePIILimit(ctx, ratelimit.OperationProfileExport, command.Authorization.Session, hex.EncodeToString(filterDigest)); err != nil {
		return profile.ProfileExportContext{}, err
	}
	var stored profile.ProfileExportContext
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		sources, err := transaction.ProfileExports().ListSources(ctx, command.Filter.UserIDs, command.Filter.Statuses)
		if err != nil {
			return err
		}
		exportID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		createdAt := service.clock.Now()
		created, err := profile.NewProfileExportContext(exportID, command.Authorization.Session.Snapshot().AdminID, filterDigest, command.Fields, profile.ProfileSchemaVersion, uint64(len(sources)), reason, createdAt, createdAt.Add(profile.DefaultExportTTL))
		if err != nil {
			return err
		}
		stored, err = transaction.ProfileExports().InsertContext(ctx, created)
		if err != nil {
			return err
		}
		for index, source := range sources {
			item, materializeErr := source.Materialize(exportID, uint64(index+1))
			if materializeErr != nil {
				return materializeErr
			}
			if _, materializeErr = transaction.ProfileExports().InsertItem(ctx, item); materializeErr != nil {
				return materializeErr
			}
		}
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetProfileExport, exportID.String(), audit.ActionProfileExportCreated, "admin_requested", digestFields(reason, hex.EncodeToString(filterDigest), strconv.Itoa(len(sources))))
	})
	return stored, mapAdminIdentityError(err)
}

type GetProfileExportPageCommand struct {
	Authorization IdentityAuthorization
	ExportID      uuid.UUID
	Cursor        string
	PageSize      int32
}

type ProfileExportRecord struct {
	UserID         uuid.UUID
	Username       string
	RealName       string
	ProfileVersion uint64
}

type ProfileExportPage struct {
	Records    []ProfileExportRecord
	NextCursor string
	Complete   bool
}

// GetProfileExportPage commits a page checkpoint audit before decrypting any materialized row.
func (service *IdentityService) GetProfileExportPage(ctx context.Context, command GetProfileExportPageCommand) (ProfileExportPage, error) {
	if command.ExportID == uuid.Nil {
		return ProfileExportPage{}, ErrInvalidInput
	}
	if command.PageSize == 0 {
		command.PageSize = 50
	}
	if command.PageSize < 0 || command.PageSize > profile.MaximumExportPageSize {
		return ProfileExportPage{}, ErrInvalidInput
	}
	afterOrdinal := uint64(0)
	if command.Cursor != "" {
		cursor, err := profile.DecodeExportCursor(command.Cursor)
		if err != nil || cursor.ExportID != command.ExportID {
			return ProfileExportPage{}, ErrInvalidInput
		}
		afterOrdinal = cursor.Ordinal
	}
	if err := service.authorize(command.Authorization, PermissionExportProfile); err != nil {
		return ProfileExportPage{}, err
	}
	if err := service.consumePIILimit(ctx, ratelimit.OperationProfileExport, command.Authorization.Session, command.ExportID.String()); err != nil {
		return ProfileExportPage{}, err
	}
	var items []profile.ProfileExportItem
	var exportContext profile.ProfileExportContext
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		var err error
		exportContext, err = transaction.ProfileExports().GetContextForUpdate(ctx, command.ExportID)
		if err != nil {
			return err
		}
		contextSnapshot := exportContext.Snapshot()
		if contextSnapshot.CreatedByAdminID != command.Authorization.Session.Snapshot().AdminID {
			return profile.ErrProfileExportClosed
		}
		if contextSnapshot.Status == profile.ExportStatusActive && !exportContext.IsReadableAt(service.clock.Now()) {
			return profile.ErrProfileExportExpired
		}
		if contextSnapshot.Status != profile.ExportStatusActive {
			return profile.ErrProfileExportClosed
		}
		if afterOrdinal > contextSnapshot.ItemCount {
			return profile.ErrProfileExportCursor
		}
		items, err = transaction.ProfileExports().ListItems(ctx, command.ExportID, int64(afterOrdinal), command.PageSize)
		if err != nil {
			return err
		}
		pageDigest := exportPageDigest(exportContext, afterOrdinal, items)
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetProfileExport, command.ExportID.String(), audit.ActionProfileExportPageRead, "page_checkpoint", pageDigest)
	})
	if err != nil {
		return ProfileExportPage{}, mapAdminIdentityError(err)
	}
	records := make([]ProfileExportRecord, 0, len(items))
	for _, item := range items {
		snapshot := item.Snapshot()
		record := ProfileExportRecord{UserID: snapshot.UserID, Username: snapshot.Username, ProfileVersion: snapshot.ProfileVersion}
		if encrypted := item.EncryptedRealName(); encrypted != nil {
			record.RealName, err = service.pii.DecryptRealName(snapshot.UserID, *encrypted)
			if err != nil {
				return ProfileExportPage{}, mapAdminIdentityError(err)
			}
		}
		records = append(records, record)
	}
	contextSnapshot := exportContext.Snapshot()
	lastOrdinal := afterOrdinal
	if len(items) > 0 {
		lastOrdinal = items[len(items)-1].Snapshot().Ordinal
	}
	complete := lastOrdinal >= contextSnapshot.ItemCount
	nextCursor := ""
	if !complete {
		nextCursor, err = profile.EncodeCursor(command.ExportID, lastOrdinal)
		if err != nil {
			return ProfileExportPage{}, mapAdminIdentityError(err)
		}
	}
	return ProfileExportPage{Records: records, NextCursor: nextCursor, Complete: complete}, nil
}

// ExpireProfileExport is called by the cleanup worker after the context reaches its half-open expiry boundary.
func (service *IdentityService) ExpireProfileExport(ctx context.Context, exportID uuid.UUID, requestID string) (bool, error) {
	if exportID == uuid.Nil || strings.TrimSpace(requestID) == "" {
		return false, ErrInvalidInput
	}
	expired := false
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		current, err := transaction.ProfileExports().GetContextForUpdate(ctx, exportID)
		if err != nil {
			return err
		}
		if current.Status() != profile.ExportStatusActive {
			return nil
		}
		if _, err = current.Expire(service.clock.Now()); err != nil {
			return err
		}
		if _, err = transaction.ProfileExports().ExpireCAS(ctx, exportID, service.clock.Now()); err != nil {
			return err
		}
		expired = true
		actor, actorErr := audit.NewActor(audit.ActorSystem, "profile-export-expirer")
		if actorErr != nil {
			return actorErr
		}
		return service.appendAuditActor(ctx, transaction, actor, requestID, audit.TargetProfileExport, exportID.String(), audit.ActionProfileExportExpired, "expired", digestFields(exportID.String()))
	})
	return expired, mapAdminIdentityError(err)
}

// CompleteProfileExport closes a readable context and records the terminal audit state atomically.
func (service *IdentityService) CompleteProfileExport(ctx context.Context, authorization IdentityAuthorization, exportID uuid.UUID) (bool, error) {
	return service.closeProfileExport(ctx, authorization, exportID, "", profile.ExportStatusCompleted)
}

// AbortProfileExport closes a readable context with an operator reason.
func (service *IdentityService) AbortProfileExport(ctx context.Context, authorization IdentityAuthorization, exportID uuid.UUID, reason string) (bool, error) {
	normalized, err := normalizeAdminReason(reason)
	if err != nil {
		return false, ErrInvalidInput
	}
	return service.closeProfileExport(ctx, authorization, exportID, normalized, profile.ExportStatusAborted)
}

func (service *IdentityService) closeProfileExport(ctx context.Context, authorization IdentityAuthorization, exportID uuid.UUID, reason string, status profile.ExportStatus) (bool, error) {
	if exportID == uuid.Nil || status != profile.ExportStatusCompleted && status != profile.ExportStatusAborted {
		return false, ErrInvalidInput
	}
	if err := service.authorize(authorization, PermissionExportProfile); err != nil {
		return false, err
	}
	if err := service.consumePIILimit(ctx, ratelimit.OperationProfileExport, authorization.Session, exportID.String()); err != nil {
		return false, err
	}
	closed := false
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		current, err := transaction.ProfileExports().GetContextForUpdate(ctx, exportID)
		if err != nil {
			return err
		}
		if current.Snapshot().CreatedByAdminID != authorization.Session.Snapshot().AdminID {
			return ErrPermissionDenied
		}
		if current.Status() == status {
			closed = true
			return nil
		}
		if current.Status() != profile.ExportStatusActive {
			return profile.ErrProfileExportClosed
		}
		if !current.IsReadableAt(service.clock.Now()) {
			return profile.ErrProfileExportExpired
		}
		if status == profile.ExportStatusCompleted {
			if _, err = current.Complete(service.clock.Now()); err != nil {
				return err
			}
			if _, err = transaction.ProfileExports().CompleteCAS(ctx, exportID, authorization.Session.Snapshot().AdminID, service.clock.Now()); err != nil {
				return err
			}
			closed = true
			return service.appendAdminAudit(ctx, transaction, authorization, audit.TargetProfileExport, exportID.String(), audit.ActionProfileExportCompleted, "completed", digestFields(exportID.String()))
		}
		if _, err = current.Abort(service.clock.Now()); err != nil {
			return err
		}
		if _, err = transaction.ProfileExports().AbortCAS(ctx, exportID, authorization.Session.Snapshot().AdminID, service.clock.Now()); err != nil {
			return err
		}
		closed = true
		return service.appendAdminAudit(ctx, transaction, authorization, audit.TargetProfileExport, exportID.String(), audit.ActionProfileExportAborted, "aborted", digestFields(exportID.String(), reason))
	})
	if err != nil {
		return false, mapAdminIdentityError(err)
	}
	return closed, nil
}

type CreateAssistedRecoveryGrantCommand struct {
	Authorization      IdentityAuthorization
	UserID             uuid.UUID
	OperationID        idempotency.OperationID
	RevokeOtherDevices bool
	Reason             string
}

type AssistedRecoveryGrantResult struct {
	Operation OperationResult
	Code      string
	ExpiresAt time.Time
}

type assistedRecoveryEnvelope struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// CreateAssistedRecoveryGrant replaces old recovery authority and returns only a short-lived one-time grant.
func (service *IdentityService) CreateAssistedRecoveryGrant(ctx context.Context, command CreateAssistedRecoveryGrantCommand) (AssistedRecoveryGrantResult, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || command.UserID == uuid.Nil || !command.OperationID.Valid() {
		return AssistedRecoveryGrantResult{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionManageRecovery); err != nil {
		return AssistedRecoveryGrantResult{}, err
	}
	digest := digestAdminRequest("admin.assisted_recovery", command.UserID.String(), strconv.FormatBool(command.RevokeOtherDevices), reason)
	binding := adminResultBinding(secretresult.ScopeAdminAssistedRecoveryGrant, command.Authorization.Session.Snapshot().AdminID, command.OperationID, digest, secretresult.ResultTypeAdminAssistedRecoveryGrant)
	var result AssistedRecoveryGrantResult
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		existing, getErr := transaction.SecretResults().GetByOperationForUpdate(ctx, binding.Key)
		if getErr == nil {
			if _, resolveErr := existing.Resolve(binding, service.clock.Now()); resolveErr != nil {
				return resolveErr
			}
			grant, grantErr := service.sessions.ResultGrant(command.Authorization.Session, existing.Snapshot().ID, service.clock.Now())
			if grantErr != nil {
				return grantErr
			}
			plaintext, openErr := service.results.OpenAdminAuthorizedResult(existing, binding, grant)
			if openErr != nil {
				return openErr
			}
			defer clear(plaintext)
			var envelope assistedRecoveryEnvelope
			if decodeErr := decodeAdminEnvelope(plaintext, &envelope); decodeErr != nil || envelope.Code == "" {
				return ErrIntegrity
			}
			expiresAt, parseErr := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
			if parseErr != nil {
				return ErrIntegrity
			}
			result = AssistedRecoveryGrantResult{Operation: adminOperationResult(command.OperationID, existing, true), Code: envelope.Code, ExpiresAt: expiresAt}
			return nil
		}
		if !errors.Is(getErr, secretresult.ErrNotFound) {
			return getErr
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		user, err := transaction.IdentityUsers().GetForUpdate(ctx, command.UserID)
		if err != nil {
			return err
		}
		if user.Snapshot().Status != identity.UserStatusActive {
			return identity.ErrUserStatus
		}
		if current, recoveryErr := transaction.IdentityRecoveryCredentials().GetActiveForUserForUpdate(ctx, command.UserID); recoveryErr == nil {
			revoked, revokeErr := current.Revoke(identity.RecoveryRevokeAssisted, service.clock.Now())
			if revokeErr != nil {
				return revokeErr
			}
			if _, revokeErr = transaction.IdentityRecoveryCredentials().RevokeCAS(ctx, current, revoked); revokeErr != nil {
				return revokeErr
			}
		} else if !errors.Is(recoveryErr, identity.ErrRecoveryInvalid) {
			return recoveryErr
		}
		if _, err = transaction.AssistedRecoveryGrants().RevokeActiveForUser(ctx, command.UserID, uuid.Nil, service.clock.Now()); err != nil {
			return err
		}
		if command.RevokeOtherDevices {
			if err = service.revokeActiveDevices(ctx, transaction, command.UserID, identity.DeviceRevokeAdminRequested); err != nil {
				return err
			}
		}
		issued, err := service.recoveryCodes.IssueAssisted(ctx, command.UserID, command.Authorization.Session.Snapshot().AdminID, service.clock.Now())
		if err != nil {
			return err
		}
		storedGrant, err := transaction.AssistedRecoveryGrants().Create(ctx, issued.Grant)
		if err != nil {
			return err
		}
		envelopeBytes, err := json.Marshal(assistedRecoveryEnvelope{Code: issued.Code, ExpiresAt: storedGrant.Snapshot().ExpiresAt.Format(time.RFC3339Nano)})
		if err != nil {
			return ErrIntegrity
		}
		defer clear(envelopeBytes)
		resultID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		prepared, err := service.results.PrepareAvailable(resultID, binding, envelopeBytes, adminSecretResultTTL)
		if err != nil {
			return err
		}
		storedResult, err := transaction.SecretResults().InsertAvailable(ctx, prepared)
		if err != nil {
			return err
		}
		if err = service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), audit.ActionAssistedRecoveryCreated, "admin_requested", digestFields(reason, command.UserID.String(), strconv.FormatBool(command.RevokeOtherDevices))); err != nil {
			return err
		}
		result = AssistedRecoveryGrantResult{Operation: adminOperationResult(command.OperationID, storedResult, false), Code: issued.Code, ExpiresAt: storedGrant.Snapshot().ExpiresAt}
		return nil
	})
	return result, mapAdminIdentityError(err)
}

type GovernanceCommand struct {
	Authorization IdentityAuthorization
	UserID        uuid.UUID
	Reason        string
}

// ForceChangeUsername changes the current claim without the user cooldown but preserves the old 90-day reservation.
func (service *IdentityService) ForceChangeUsername(ctx context.Context, command GovernanceCommand, usernameValue string) (identity.User, error) {
	reason, err := normalizeAdminReason(command.Reason)
	username, parseErr := identifier.ParseUsername(usernameValue)
	if err != nil || parseErr != nil || command.UserID == uuid.Nil {
		return identity.User{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionForceUsername); err != nil {
		return identity.User{}, err
	}
	var stored identity.User
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		current, err := transaction.IdentityUsers().GetForUpdate(ctx, command.UserID)
		if err != nil {
			return err
		}
		plan, err := current.PlanForcedUsernameChange(username, service.clock.Now())
		if err != nil {
			return err
		}
		newClaim, err := identity.NewActiveUsernameClaim(username, command.UserID, plan.ChangedAt)
		if err != nil {
			return err
		}
		if _, err = transaction.IdentityUsernameClaims().Claim(ctx, newClaim, plan.ChangedAt); err != nil {
			return err
		}
		oldClaim, err := transaction.IdentityUsernameClaims().GetForUpdate(ctx, plan.PreviousUsernameKey)
		if err != nil {
			return err
		}
		reserved, err := oldClaim.Reserve(plan.ReservePreviousUntil, plan.ChangedAt)
		if err != nil {
			return err
		}
		if _, err = transaction.IdentityUsernameClaims().ReserveCAS(ctx, oldClaim, reserved); err != nil {
			return err
		}
		stored, err = transaction.IdentityUsers().ForceChangeUsernameCAS(ctx, current, plan.Next)
		if err != nil {
			return err
		}
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), audit.ActionUsernameForceChanged, "admin_requested", digestFields(reason, command.UserID.String(), username.Key()))
	})
	return stored, mapAdminIdentityError(err)
}

// SuspendUser revokes active recovery and device authority in the same transaction as the status change.
func (service *IdentityService) SuspendUser(ctx context.Context, command GovernanceCommand) (identity.User, error) {
	return service.transitionUser(ctx, command, identity.UserStatusSuspended)
}

// UnsuspendUser restores account status only; revoked credentials remain revoked.
func (service *IdentityService) UnsuspendUser(ctx context.Context, command GovernanceCommand) (identity.User, error) {
	return service.transitionUser(ctx, command, identity.UserStatusActive)
}

// DeleteUser reserves the current name before clearing the deferred username foreign key.
func (service *IdentityService) DeleteUser(ctx context.Context, command GovernanceCommand) (identity.User, error) {
	return service.transitionUser(ctx, command, identity.UserStatusDeleted)
}

func (service *IdentityService) transitionUser(ctx context.Context, command GovernanceCommand, nextStatus identity.UserStatus) (identity.User, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || command.UserID == uuid.Nil {
		return identity.User{}, ErrInvalidInput
	}
	permission := PermissionSuspendUser
	if nextStatus == identity.UserStatusDeleted {
		permission = PermissionDeleteUser
	}
	if err = service.authorize(command.Authorization, permission); err != nil {
		return identity.User{}, err
	}
	var stored identity.User
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		current, err := transaction.IdentityUsers().GetForUpdate(ctx, command.UserID)
		if err != nil {
			return err
		}
		next, err := current.TransitionForGovernance(nextStatus, service.clock.Now())
		if err != nil {
			return err
		}
		if nextStatus == identity.UserStatusDeleted {
			oldClaim, claimErr := transaction.IdentityUsernameClaims().GetForUpdate(ctx, current.Snapshot().CurrentUsernameKey)
			if claimErr != nil {
				return claimErr
			}
			reserved, claimErr := oldClaim.Reserve(service.clock.Now().Add(identity.UsernameReservationTTL), service.clock.Now())
			if claimErr != nil {
				return claimErr
			}
			if _, claimErr = transaction.IdentityUsernameClaims().ReserveCAS(ctx, oldClaim, reserved); claimErr != nil {
				return claimErr
			}
		}
		if nextStatus == identity.UserStatusSuspended || nextStatus == identity.UserStatusDeleted {
			deviceReason := identity.DeviceRevokeAccountSuspended
			recoveryReason := identity.RecoveryRevokeAccountSuspended
			if nextStatus == identity.UserStatusDeleted {
				deviceReason, recoveryReason = identity.DeviceRevokeAccountDeleted, identity.RecoveryRevokeAccountDeleted
			}
			if err = service.revokeActiveDevices(ctx, transaction, command.UserID, deviceReason); err != nil {
				return err
			}
			if currentRecovery, recoveryErr := transaction.IdentityRecoveryCredentials().GetActiveForUserForUpdate(ctx, command.UserID); recoveryErr == nil {
				revoked, revokeErr := currentRecovery.Revoke(recoveryReason, service.clock.Now())
				if revokeErr != nil {
					return revokeErr
				}
				if _, revokeErr = transaction.IdentityRecoveryCredentials().RevokeCAS(ctx, currentRecovery, revoked); revokeErr != nil {
					return revokeErr
				}
			} else if !errors.Is(recoveryErr, identity.ErrRecoveryInvalid) {
				return recoveryErr
			}
			if _, err = transaction.AssistedRecoveryGrants().RevokeActiveForUser(ctx, command.UserID, uuid.Nil, service.clock.Now()); err != nil {
				return err
			}
		}
		stored, err = transaction.IdentityUsers().TransitionStatusCAS(ctx, current, next)
		if err != nil {
			return err
		}
		action, eventType, reasonCode := audit.ActionUserUnsuspended, outbox.EventTypeIdentityUserUnsuspended, "unsuspended"
		if nextStatus == identity.UserStatusSuspended {
			action, eventType, reasonCode = audit.ActionUserSuspended, outbox.EventTypeIdentityUserSuspended, "suspended"
		} else if nextStatus == identity.UserStatusDeleted {
			action, eventType, reasonCode = audit.ActionUserDeleted, outbox.EventTypeIdentityUserDeleted, "deleted"
		}
		if err = service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetUser, command.UserID.String(), action, reasonCode, digestFields(reason, command.UserID.String())); err != nil {
			return err
		}
		return insertUserStatusOutbox(ctx, transaction, eventType, current.Snapshot().Status, nextStatus, command.Authorization, command.UserID, service.clock.Now())
	})
	return stored, mapAdminIdentityError(err)
}

type RevokeUserDeviceCommand struct {
	Authorization IdentityAuthorization
	UserID        uuid.UUID
	CredentialID  uuid.UUID
	Reason        string
}

// RevokeUserDevice revokes only the credential owned by the requested user and emits audit/outbox facts.
func (service *IdentityService) RevokeUserDevice(ctx context.Context, command RevokeUserDeviceCommand) (bool, error) {
	reason, err := normalizeAdminReason(command.Reason)
	if err != nil || command.UserID == uuid.Nil || command.CredentialID == uuid.Nil {
		return false, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionRevokeDevice); err != nil {
		return false, err
	}
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		if err := service.requireHealthyAudit(ctx, transaction); err != nil {
			return err
		}
		if _, err := transaction.IdentityUsers().GetForUpdate(ctx, command.UserID); err != nil {
			return err
		}
		current, err := transaction.IdentityDevices().GetForUpdate(ctx, command.CredentialID)
		if err != nil || current.Snapshot().UserID != command.UserID {
			if err != nil {
				return err
			}
			return identity.ErrDeviceAuthentication
		}
		next, err := current.Revoke(identity.DeviceRevokeAdminRequested, service.clock.Now())
		if err != nil {
			return err
		}
		if _, err = transaction.IdentityDevices().RevokeCAS(ctx, current, next); err != nil {
			return err
		}
		if err = service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetDevice, command.CredentialID.String(), audit.ActionDeviceRevoked, "admin_requested", digestFields(reason, command.UserID.String(), command.CredentialID.String())); err != nil {
			return err
		}
		return insertDeviceRevokedOutbox(ctx, transaction, command.Authorization, command.UserID, command.CredentialID, identity.DeviceRevokeAdminRequested, service.clock.Now())
	})
	return err == nil, mapAdminIdentityError(err)
}

type ListAuditEventsCommand struct {
	Authorization IdentityAuthorization
	ActorAdminID  uuid.UUID
	TargetUserID  uuid.UUID
	Actions       []audit.Action
	StartedAt     time.Time
	EndedAt       time.Time
	PageSize      uint32
	PageToken     string
}

type AuditEventsPage struct {
	Events        []audit.SignedEvent
	NextPageToken string
}

// ListAuditEvents reads and verifies signed records, then appends a separate read audit after the snapshot query.
func (service *IdentityService) ListAuditEvents(ctx context.Context, command ListAuditEventsCommand) (AuditEventsPage, error) {
	if command.PageSize == 0 {
		command.PageSize = defaultAuditPageSize
	}
	if command.PageSize > maximumAuditPageSize || !validAuditFilters(command) {
		return AuditEventsPage{}, ErrInvalidInput
	}
	filterDigest := auditFilterDigest(command)
	afterSequence, err := decodeAuditPageToken(command.PageToken, filterDigest)
	if err != nil {
		return AuditEventsPage{}, ErrInvalidInput
	}
	if err = service.authorize(command.Authorization, PermissionReadAudit); err != nil {
		return AuditEventsPage{}, err
	}
	var scanned []audit.SignedEvent
	var result []audit.SignedEvent
	var lastProcessedSequence uint64
	var hasMore bool
	err = service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		if err := service.requireCurrentSession(ctx, transaction, command.Authorization.Session); err != nil {
			return err
		}
		request, err := audit.NewListRequest(audit.ChainAdmin, afterSequence, minUint32(command.PageSize*4, audit.MaximumPageSize))
		if err != nil {
			return err
		}
		scanned, err = transaction.Audit().List(ctx, request)
		if err != nil {
			return err
		}
		result = make([]audit.SignedEvent, 0, command.PageSize)
		for index, event := range scanned {
			lastProcessedSequence = event.Snapshot().Event.Sequence
			if auditEventMatches(event, command) {
				result = append(result, event)
				if uint32(len(result)) == command.PageSize {
					hasMore = index < len(scanned)-1 || uint32(len(scanned)) == minUint32(command.PageSize*4, audit.MaximumPageSize)
					break
				}
			}
		}
		if uint32(len(result)) < command.PageSize && uint32(len(scanned)) == minUint32(command.PageSize*4, audit.MaximumPageSize) {
			hasMore = true
		}
		return service.appendAdminAudit(ctx, transaction, command.Authorization, audit.TargetSystem, "audit-events", audit.ActionAuditEventsRead, "admin_requested", digestFields(
			strconv.FormatUint(afterSequence, 10), strconv.FormatUint(uint64(command.PageSize), 10), hex.EncodeToString(filterDigest),
		))
	})
	if err != nil {
		return AuditEventsPage{}, mapAdminIdentityError(err)
	}
	nextToken := ""
	if hasMore && lastProcessedSequence > 0 {
		nextToken = encodeAuditPageToken(lastProcessedSequence, filterDigest)
	}
	return AuditEventsPage{Events: result, NextPageToken: nextToken}, nil
}

func (service *IdentityService) authorize(authorization IdentityAuthorization, permission Permission) error {
	if service == nil || service.sessions == nil || service.clock == nil {
		return ErrAuthentication
	}
	if err := service.sessions.Authenticate(authorization.Session, authorization.SessionToken, authorization.CSRFToken, service.clock.Now()); err != nil {
		return err
	}
	return service.authorizer.Authorize(authorization.Session, permission, service.clock)
}

func (service *IdentityService) requireCurrentSession(ctx context.Context, transaction Transaction, session Session) error {
	account, err := transaction.Accounts().GetForUpdate(ctx)
	if err != nil {
		return err
	}
	if !sessionMatchesAccount(session, account) || account.Snapshot().Status != AccountStatusActive {
		return ErrAuthentication
	}
	return nil
}

func (service *IdentityService) consumePIILimit(ctx context.Context, operation ratelimit.Operation, session Session, target string) error {
	policy, err := ratelimit.PolicyFor(operation)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	sessionValue, err := ratelimit.NewBucketValue(session.Snapshot().ID.String())
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	targetValue, err := ratelimit.NewBucketValue(target)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	sessionKey, err := ratelimit.NewBucketKey(ratelimit.DimensionAdminSession, sessionValue)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	targetKey, err := ratelimit.NewBucketKey(ratelimit.DimensionTargetUser, targetValue)
	if err != nil {
		return ratelimit.ErrUnavailable
	}
	return policy.Consume(ctx, service.limiter, sessionKey, targetKey)
}

func (service *IdentityService) requireHealthyAudit(ctx context.Context, transaction Transaction) error {
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	progress, err := transaction.AuditCheckpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	health, err := audit.EvaluateCheckpointHealth(audit.CheckpointHealthInput{
		HeadSequence: head.Sequence(), AcknowledgedSequence: progress.AcknowledgedSequence,
		UncheckpointedSince: progress.UncheckpointedSince, Now: service.clock.Now(), Production: false, SinkReady: true,
	})
	if err != nil {
		return err
	}
	if !health.AllowsSensitiveWrites() {
		return audit.ErrSensitiveWriteBlocked
	}
	return nil
}

func (service *IdentityService) appendAdminAudit(ctx context.Context, transaction Transaction, authorization IdentityAuthorization, targetType audit.TargetType, targetID string, action audit.Action, reasonCode string, detailDigest []byte) error {
	actor, err := audit.NewActor(audit.ActorAdmin, authorization.Session.Snapshot().AdminID.String())
	if err != nil {
		return err
	}
	return service.appendAuditActor(ctx, transaction, actor, authorization.RequestID, targetType, targetID, action, reasonCode, detailDigest)
}

func (service *IdentityService) appendAuditActor(ctx context.Context, transaction Transaction, actor audit.Actor, requestID string, targetType audit.TargetType, targetID string, action audit.Action, reasonCode string, detailDigest []byte) error {
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return err
	}
	target, err := audit.NewTarget(targetType, targetID)
	if err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ErrInvalidInput
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	event, err := service.audit.Prepare(head, audit.EventInput{EventID: eventID, RequestID: requestID, OccurredAt: service.clock.Now(), Actor: actor, Target: target, Action: action, ReasonCode: reasonCode, DetailDigest: detailDigest})
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
	health, err := audit.EvaluateCheckpointHealth(audit.CheckpointHealthInput{HeadSequence: next.Sequence(), AcknowledgedSequence: progress.AcknowledgedSequence, UncheckpointedSince: progress.UncheckpointedSince, Now: service.clock.Now(), Production: false, SinkReady: true})
	if err != nil || !health.CheckpointDue() {
		return err
	}
	checkpoint, err := service.audit.PrepareCheckpoint(next, service.clock.Now())
	if err != nil {
		return err
	}
	return transaction.AuditCheckpoints().AppendPendingCheckpoint(ctx, checkpoint)
}

func (service *IdentityService) revokeActiveDevices(ctx context.Context, transaction Transaction, userID uuid.UUID, reason identity.DeviceRevokeReason) error {
	cursor := identity.DevicePageCursor{}
	for {
		request, err := identity.NewDeviceListRequest(userID, false, cursor, identity.MaximumDevicePageSize, service.clock.Now())
		if err != nil {
			return err
		}
		devices, err := transaction.IdentityDevices().List(ctx, request)
		if err != nil {
			return err
		}
		for _, summary := range devices {
			if summary.Status != identity.DeviceStateActive {
				continue
			}
			current, getErr := transaction.IdentityDevices().GetForUpdate(ctx, summary.CredentialID)
			if getErr != nil {
				return getErr
			}
			revoked, revokeErr := current.Revoke(reason, service.clock.Now())
			if revokeErr != nil {
				return revokeErr
			}
			if _, revokeErr = transaction.IdentityDevices().RevokeCAS(ctx, current, revoked); revokeErr != nil {
				return revokeErr
			}
		}
		if len(devices) < int(identity.MaximumDevicePageSize) {
			return nil
		}
		cursor = identity.NextDeviceCursor(devices)
	}
}

func insertUserStatusOutbox(ctx context.Context, transaction Transaction, eventType outbox.EventType, previous, current identity.UserStatus, authorization IdentityAuthorization, userID uuid.UUID, at time.Time) error {
	eventID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	payload, err := deterministicAdminIdentityEvent(&identityv1.IdentityUserStatusChangedEvent{
		SchemaVersion: 1, EventId: eventID.String(), RequestId: authorization.RequestID,
		OccurredAt: timestamppb.New(at), UserId: userID.String(), PreviousStatus: userStatusWire(previous),
		CurrentStatus: userStatusWire(current), ActorAdminId: authorization.Session.Snapshot().AdminID.String(),
	})
	if err != nil {
		return err
	}
	event, err := outbox.NewEvent(eventID, eventType, outbox.AggregateTypeIdentityUser, userID, payload, at, at)
	if err != nil {
		return err
	}
	_, err = transaction.OutboxEvents().Insert(ctx, event)
	return err
}

func insertDeviceRevokedOutbox(ctx context.Context, transaction Transaction, authorization IdentityAuthorization, userID, credentialID uuid.UUID, reason identity.DeviceRevokeReason, at time.Time) error {
	eventID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	payload, err := deterministicAdminIdentityEvent(&identityv1.IdentityDeviceRevokedEvent{
		SchemaVersion: 1, EventId: eventID.String(), RequestId: authorization.RequestID,
		OccurredAt: timestamppb.New(at), UserId: userID.String(), DeviceCredentialId: credentialID.String(),
		Reason: deviceRevocationReasonWire(reason),
	})
	if err != nil {
		return err
	}
	event, err := outbox.NewEvent(eventID, outbox.EventTypeIdentityDeviceRevoked, outbox.AggregateTypeIdentityDevice, credentialID, payload, at, at)
	if err != nil {
		return err
	}
	_, err = transaction.OutboxEvents().Insert(ctx, event)
	return err
}

func deterministicAdminIdentityEvent(message proto.Message) ([]byte, error) {
	if message == nil {
		return nil, ErrIntegrity
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil || len(payload) == 0 {
		return nil, ErrIntegrity
	}
	return payload, nil
}

func deviceRevocationReasonWire(reason identity.DeviceRevokeReason) identityv1.IdentityDeviceRevocationReason {
	switch reason {
	case identity.DeviceRevokeUserRequested:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_USER_REQUESTED
	case identity.DeviceRevokeAdminRequested:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_ADMIN_REQUESTED
	case identity.DeviceRevokeRecovery:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_RECOVERY
	case identity.DeviceRevokeOnboardingExpiry:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_ONBOARDING_EXPIRED
	case identity.DeviceRevokeAccountSuspended:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_ACCOUNT_SUSPENDED
	case identity.DeviceRevokeAccountDeleted:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_ACCOUNT_DELETED
	default:
		return identityv1.IdentityDeviceRevocationReason_IDENTITY_DEVICE_REVOCATION_REASON_UNSPECIFIED
	}
}

func normalizeAdminReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maximumAdminReasonBytes || !utf8.ValidString(value) {
		return "", ErrInvalidInput
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalidInput
		}
	}
	return value, nil
}

func validProfileFields(fields []profile.Field) bool {
	if len(fields) != 1 || fields[0] != profile.FieldRealName {
		return false
	}
	return true
}

func validExportFilter(filter ProfileExportFilter) bool {
	seenUsers := make(map[uuid.UUID]struct{}, len(filter.UserIDs))
	for _, userID := range filter.UserIDs {
		if userID == uuid.Nil {
			return false
		}
		if _, exists := seenUsers[userID]; exists {
			return false
		}
		seenUsers[userID] = struct{}{}
	}
	seenStatuses := make(map[identity.UserStatus]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return false
		}
		if _, exists := seenStatuses[status]; exists {
			return false
		}
		seenStatuses[status] = struct{}{}
	}
	return len(filter.UserIDs) > 0 || len(filter.Statuses) > 0
}

func profileFilterDigest(filter ProfileExportFilter, fields []profile.Field) []byte {
	userIDs := append([]uuid.UUID(nil), filter.UserIDs...)
	statuses := append([]identity.UserStatus(nil), filter.Statuses...)
	fieldSet := append([]profile.Field(nil), fields...)
	sort.Slice(userIDs, func(left, right int) bool { return userIDs[left].String() < userIDs[right].String() })
	sort.Slice(statuses, func(left, right int) bool { return statuses[left] < statuses[right] })
	sort.Slice(fieldSet, func(left, right int) bool { return fieldSet[left] < fieldSet[right] })
	hash := sha256.New()
	appendDigestField(hash, "profile-export-filter-v1")
	for _, userID := range userIDs {
		appendDigestField(hash, userID.String())
	}
	for _, status := range statuses {
		appendDigestField(hash, string(status))
	}
	for _, field := range fieldSet {
		appendDigestField(hash, string(field))
	}
	return hash.Sum(nil)
}

func exportPageDigest(exportContext profile.ProfileExportContext, afterOrdinal uint64, items []profile.ProfileExportItem) []byte {
	snapshot := exportContext.Snapshot()
	hash := sha256.New()
	appendDigestField(hash, "profile-export-page-v1")
	appendDigestField(hash, snapshot.ExportID.String())
	appendDigestField(hash, hex.EncodeToString(snapshot.FilterDigest))
	appendDigestField(hash, strconv.FormatUint(afterOrdinal, 10))
	appendDigestField(hash, strconv.FormatUint(uint64(snapshot.SchemaVersion), 10))
	appendDigestField(hash, strconv.Itoa(len(items)))
	for _, item := range items {
		itemSnapshot := item.Snapshot()
		appendDigestField(hash, itemSnapshot.UserID.String())
		appendDigestField(hash, strconv.FormatUint(itemSnapshot.Ordinal, 10))
		appendDigestField(hash, strconv.FormatUint(itemSnapshot.ProfileVersion, 10))
	}
	return hash.Sum(nil)
}

type digestFieldWriter interface{ Write([]byte) (int, error) }

func appendDigestField(writer digestFieldWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

func digestFields(values ...string) []byte {
	hash := sha256.New()
	for _, value := range values {
		appendDigestField(hash, value)
	}
	return hash.Sum(nil)
}

func validAuditFilters(command ListAuditEventsCommand) bool {
	if !command.StartedAt.IsZero() && !command.EndedAt.IsZero() && command.EndedAt.Before(command.StartedAt) {
		return false
	}
	seen := make(map[audit.Action]struct{}, len(command.Actions))
	for _, action := range command.Actions {
		if !action.Valid() {
			return false
		}
		if _, exists := seen[action]; exists {
			return false
		}
		seen[action] = struct{}{}
	}
	return true
}

func auditEventMatches(event audit.SignedEvent, command ListAuditEventsCommand) bool {
	snapshot := event.Snapshot().Event
	// Read-audit events are omitted by default; an explicit action filter can still inspect them.
	if snapshot.Action == audit.ActionAuditEventsRead && !auditActionRequested(command.Actions, audit.ActionAuditEventsRead) {
		return false
	}
	if command.ActorAdminID != uuid.Nil && (snapshot.Actor.Type() != audit.ActorAdmin || snapshot.Actor.ID() != command.ActorAdminID.String()) {
		return false
	}
	if command.TargetUserID != uuid.Nil && (snapshot.Target.Type() != audit.TargetUser || snapshot.Target.ID() != command.TargetUserID.String()) {
		return false
	}
	if !command.StartedAt.IsZero() && snapshot.OccurredAt.Before(command.StartedAt) {
		return false
	}
	if !command.EndedAt.IsZero() && !snapshot.OccurredAt.Before(command.EndedAt) {
		return false
	}
	if len(command.Actions) > 0 {
		for _, action := range command.Actions {
			if snapshot.Action == action {
				return true
			}
		}
		return false
	}
	return true
}

func auditActionRequested(actions []audit.Action, target audit.Action) bool {
	for _, action := range actions {
		if action == target {
			return true
		}
	}
	return false
}

func auditFilterDigest(command ListAuditEventsCommand) []byte {
	actions := append([]audit.Action(nil), command.Actions...)
	sort.Slice(actions, func(left, right int) bool { return actions[left] < actions[right] })
	hash := sha256.New()
	appendDigestField(hash, "admin-audit-filter-v1")
	appendDigestField(hash, command.ActorAdminID.String())
	appendDigestField(hash, command.TargetUserID.String())
	appendDigestField(hash, command.StartedAt.UTC().Format(time.RFC3339Nano))
	appendDigestField(hash, command.EndedAt.UTC().Format(time.RFC3339Nano))
	for _, action := range actions {
		appendDigestField(hash, strconv.FormatInt(int64(action), 10))
	}
	return hash.Sum(nil)
}

func encodeAuditPageToken(sequence uint64, filterDigest []byte) string {
	return "v1." + strconv.FormatUint(sequence, 10) + "." + hex.EncodeToString(filterDigest)
}

func decodeAuditPageToken(value string, filterDigest []byte) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "v1" || parts[2] != hex.EncodeToString(filterDigest) {
		return 0, ErrInvalidInput
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || strconv.FormatUint(sequence, 10) != parts[1] {
		return 0, ErrInvalidInput
	}
	return sequence, nil
}

func minUint32(left, right uint32) uint32 {
	if left < right {
		return left
	}
	return right
}

func mapAdminIdentityError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrInvalidInput), errors.Is(err, ErrAuthentication), errors.Is(err, ErrPermissionDenied),
		errors.Is(err, ratelimit.ErrRejected), errors.Is(err, ratelimit.ErrUnavailable),
		errors.Is(err, identity.ErrUserNotFound), errors.Is(err, identity.ErrUserStatus),
		errors.Is(err, identity.ErrUsernameUnavailable), errors.Is(err, identity.ErrIdentityConcurrentTransition),
		errors.Is(err, identity.ErrDeviceAuthentication), errors.Is(err, identity.ErrDeviceConcurrentTransition),
		errors.Is(err, identity.ErrRecoveryInvalid), errors.Is(err, identity.ErrRecoveryConcurrentTransition),
		errors.Is(err, profile.ErrProfileNotFound), errors.Is(err, profile.ErrInvalidProfileInput),
		errors.Is(err, profile.ErrProfileConcurrentTransition), errors.Is(err, profile.ErrProfileExportClosed),
		errors.Is(err, profile.ErrProfileExportExpired), errors.Is(err, profile.ErrProfileExportCursor),
		errors.Is(err, profile.ErrPIIAuthentication), errors.Is(err, profile.ErrPIIKeyUnavailable),
		errors.Is(err, profile.ErrProfileRepositoryUnavailable), errors.Is(err, audit.ErrSensitiveWriteBlocked),
		errors.Is(err, audit.ErrHeadConflict), errors.Is(err, audit.ErrRepositoryUnavailable),
		errors.Is(err, secretresult.ErrIdempotencyConflict),
		errors.Is(err, secretresult.ErrSecretNoLongerAvailable):
		return err
	default:
		return fmt.Errorf("%w: admin identity operation", ErrRepositoryUnavailable)
	}
}
