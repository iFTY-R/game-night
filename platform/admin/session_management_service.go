package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

type SessionInfo struct {
	Session               Session
	Current               bool
	ActiveElevationScopes []ElevationScope
}

type ListAdminSessionsResult struct {
	Sessions []SessionInfo
}

type RevokeAdminSessionCommand struct {
	Session                Session
	SessionToken           string
	CSRFToken              string
	OperationID            idempotency.OperationID
	TargetSessionID        uuid.UUID
	ExpectedSessionVersion int64
	RequestID              string
}

type RevokeAdminSessionResult struct {
	SessionID uuid.UUID
	Revoked   bool
}

type PreviewRevokeOtherSessionsCommand struct {
	Session      Session
	SessionToken string
	CSRFToken    string
}

type PreviewRevokeOtherSessionsResult struct {
	PreviewVersion        string
	CurrentAdminVersion   int64
	CurrentSessionVersion int64
	OtherSessionCount     int64
	Sessions              []SessionInfo
}

type RevokeOtherSessionsCommand struct {
	Session                       Session
	SessionToken                  string
	CSRFToken                     string
	OperationID                   idempotency.OperationID
	PreviewVersion                string
	ExpectedAdminVersion          int64
	ExpectedCurrentSessionVersion int64
	RequestID                     string
}

type RevokeOtherSessionsResult struct {
	RevokedSessions int64
	Session         Session
}

// ListAdminSessions enumerates every currently active session and the live scopes bound to each one.
func (service *Service) ListAdminSessions(ctx context.Context, command PreviewRevokeOtherSessionsCommand) (ListAdminSessionsResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull {
		return ListAdminSessionsResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return ListAdminSessionsResult{}, err
	}
	var result ListAdminSessionsResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		sessions, err := transaction.Sessions().ListActiveForAdmin(ctx, account.Snapshot().ID, service.clock.Now())
		if err != nil {
			return err
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID)
		if err != nil {
			return err
		}
		elevationsBySession, err := service.liveElevationsBySession(ctx, transaction, account.Snapshot().ID, sessions)
		if err != nil {
			return err
		}
		items := make([]SessionInfo, 0, len(sessions))
		for _, session := range sessions {
			elevationScopes, scopeErr := service.activeElevationScopes(session, enrollmentVersionOf(enrollment), elevationsBySession[session.Snapshot().ID])
			if scopeErr != nil {
				return scopeErr
			}
			items = append(items, SessionInfo{
				Session:               session,
				Current:               session.Snapshot().ID == command.Session.Snapshot().ID,
				ActiveElevationScopes: elevationScopes,
			})
		}
		sort.Slice(items, func(left, right int) bool {
			return items[left].Session.Snapshot().CreatedAt.After(items[right].Session.Snapshot().CreatedAt)
		})
		result = ListAdminSessionsResult{Sessions: items}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// PreviewRevokeOtherAdminSessions freezes the exact set that a later elevated revoke call must match.
func (service *Service) PreviewRevokeOtherAdminSessions(ctx context.Context, command PreviewRevokeOtherSessionsCommand) (PreviewRevokeOtherSessionsResult, error) {
	listed, err := service.ListAdminSessions(ctx, command)
	if err != nil {
		return PreviewRevokeOtherSessionsResult{}, err
	}
	others := make([]SessionInfo, 0, len(listed.Sessions))
	for _, item := range listed.Sessions {
		if item.Current {
			continue
		}
		others = append(others, item)
	}
	result := PreviewRevokeOtherSessionsResult{
		CurrentAdminVersion:   command.Session.Snapshot().AdminVersion,
		CurrentSessionVersion: command.Session.Snapshot().SessionVersion,
		OtherSessionCount:     int64(len(others)),
		Sessions:              others,
	}
	result.PreviewVersion = service.revokeOtherPreviewVersion(command.Session, others)
	return result, nil
}

// RevokeAdminSession revokes one non-current active session by exact ID and version.
func (service *Service) RevokeAdminSession(ctx context.Context, command RevokeAdminSessionCommand) (RevokeAdminSessionResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() || command.TargetSessionID == uuid.Nil || command.ExpectedSessionVersion <= 0 {
		return RevokeAdminSessionResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return RevokeAdminSessionResult{}, err
	}
	result := RevokeAdminSessionResult{SessionID: command.TargetSessionID}
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		replayed, replayErr := service.replaySingleSessionRevokeReceipt(ctx, transaction, account.Snapshot().ID, command)
		if replayErr != nil {
			return replayErr
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		if command.TargetSessionID == command.Session.Snapshot().ID {
			return ErrPermissionDenied
		}
		target, err := transaction.Sessions().GetByIDForUpdate(ctx, command.TargetSessionID)
		if err != nil {
			return err
		}
		if target.Snapshot().AdminID != account.Snapshot().ID || target.Snapshot().SessionVersion != command.ExpectedSessionVersion {
			return ErrConcurrentTransition
		}
		revoked, err := transaction.Sessions().RevokeCAS(ctx, target, "manual_revoke", service.clock.Now())
		if err != nil {
			return err
		}
		auditEventID, err := service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, command.TargetSessionID.String(), audit.ActionAdminSessionsRevoked, "single_session_revoked", digestAdminRequest("admin.session.revoke", command.TargetSessionID.String(), strconv.FormatInt(command.ExpectedSessionVersion, 10)).Bytes())
		if err != nil {
			return err
		}
		result.Revoked = !revoked.Snapshot().RevokedAt.IsZero()
		if err = service.saveCommandReceipt(ctx, transaction, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.revoke_session", command.TargetSessionID.String(), strconv.FormatInt(command.ExpectedSessionVersion, 10)), "revoke_admin_session", encodeReceiptTarget(command.TargetSessionID.String(), strconv.FormatBool(result.Revoked)), account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, command.Session.Snapshot().SessionVersion, 0, auditEventID); err != nil {
			return err
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// RevokeOtherAdminSessions requires a fresh preview and elevation grant before revoking every other active session.
func (service *Service) RevokeOtherAdminSessions(ctx context.Context, command RevokeOtherSessionsCommand) (RevokeOtherSessionsResult, error) {
	if command.Session.Snapshot().Kind != SessionKindFull || !command.OperationID.Valid() {
		return RevokeOtherSessionsResult{}, ErrPermissionDenied
	}
	if err := service.sessions.Authenticate(command.Session, command.SessionToken, command.CSRFToken, service.clock.Now()); err != nil {
		return RevokeOtherSessionsResult{}, err
	}
	var result RevokeOtherSessionsResult
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		account, err := transaction.Accounts().GetForUpdate(ctx)
		if err != nil {
			return err
		}
		if !sessionMatchesAccount(command.Session, account) || account.Snapshot().Status != AccountStatusActive {
			return ErrAuthentication
		}
		if account.Snapshot().AdminVersion != command.ExpectedAdminVersion || command.Session.Snapshot().SessionVersion != command.ExpectedCurrentSessionVersion {
			return ErrConcurrentTransition
		}
		replayed, replayErr := service.replayBulkSessionRevokeReceipt(ctx, transaction, account.Snapshot().ID, command)
		if replayErr != nil {
			return replayErr
		}
		if replayed != nil {
			result = *replayed
			return nil
		}
		enrollment, err := service.loadActiveEnrollment(ctx, transaction, account.Snapshot().ID)
		if err != nil {
			return err
		}
		if err = service.requireElevation(ctx, transaction, command.Session, enrollmentVersionOf(enrollment), ElevationScopeSecurityRevokeSessions, command.RequestID); err != nil {
			return err
		}
		sessions, err := transaction.Sessions().ListActiveForAdmin(ctx, account.Snapshot().ID, service.clock.Now())
		if err != nil {
			return err
		}
		elevationsBySession, err := service.liveElevationsBySession(ctx, transaction, account.Snapshot().ID, sessions)
		if err != nil {
			return err
		}
		previewItems := make([]SessionInfo, 0, len(sessions))
		for _, session := range sessions {
			if session.Snapshot().ID == command.Session.Snapshot().ID {
				continue
			}
			scopes, scopeErr := service.activeElevationScopes(session, enrollmentVersionOf(enrollment), elevationsBySession[session.Snapshot().ID])
			if scopeErr != nil {
				return scopeErr
			}
			previewItems = append(previewItems, SessionInfo{Session: session, ActiveElevationScopes: scopes})
		}
		if command.PreviewVersion != service.revokeOtherPreviewVersion(command.Session, previewItems) {
			return ErrConcurrentTransition
		}
		revoked, err := transaction.Sessions().RevokeOtherActiveCAS(
			ctx,
			account.Snapshot().ID,
			command.Session.Snapshot().ID,
			account.Snapshot().AdminVersion,
			command.Session.Snapshot().SessionVersion,
			"bulk_revoke",
			service.clock.Now(),
		)
		if err != nil {
			return err
		}
		auditEventID, err := service.appendAdminAudit(ctx, transaction, account.Snapshot().ID, command.RequestID, audit.TargetAdmin, account.Snapshot().ID.String(), audit.ActionAdminSessionsRevoked, "other_sessions_revoked", digestAdminRequest("admin.sessions.revoke_other", command.PreviewVersion, strconv.FormatInt(int64(len(revoked)), 10)).Bytes())
		if err != nil {
			return err
		}
		result = RevokeOtherSessionsResult{RevokedSessions: int64(len(revoked)), Session: command.Session}
		if err = service.saveCommandReceipt(ctx, transaction, account.Snapshot().ID, command.OperationID, digestAdminRequest("admin.revoke_other_sessions", command.PreviewVersion, strconv.FormatInt(command.ExpectedAdminVersion, 10), strconv.FormatInt(command.ExpectedCurrentSessionVersion, 10)), "revoke_other_admin_sessions", encodeReceiptTarget(command.Session.Snapshot().ID.String(), strconv.FormatInt(result.RevokedSessions, 10)), account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, command.Session.Snapshot().SessionVersion, enrollmentVersionOf(enrollment), auditEventID); err != nil {
			return err
		}
		return nil
	})
	return result, mapAdminUoWError(err)
}

// LogoutAdmin revokes exactly one authenticated session.
func (service *Service) LogoutAdmin(ctx context.Context, session Session, token, csrfToken string) error {
	if err := service.sessions.Authenticate(session, token, csrfToken, service.clock.Now()); err != nil {
		return err
	}
	err := service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		_, err := transaction.Sessions().RevokeCAS(ctx, session, "logout", service.clock.Now())
		return err
	})
	return mapAdminUoWError(err)
}

func (service *Service) liveElevationsBySession(ctx context.Context, transaction Transaction, adminID uuid.UUID, sessions []Session) (map[uuid.UUID][]Elevation, error) {
	repository := transaction.Elevations()
	grouped := make(map[uuid.UUID][]Elevation, len(sessions))
	if repository == nil || len(sessions) == 0 {
		return grouped, nil
	}
	sessionIDs := make([]uuid.UUID, 0, len(sessions))
	for _, session := range sessions {
		sessionIDs = append(sessionIDs, session.Snapshot().ID)
	}
	elevations, err := repository.ListLiveForSessions(ctx, adminID, sessionIDs, service.clock.Now())
	if err != nil {
		return nil, err
	}
	for _, elevation := range elevations {
		snapshot := elevation.Snapshot()
		grouped[snapshot.SessionID] = append(grouped[snapshot.SessionID], elevation)
	}
	return grouped, nil
}

func (service *Service) activeElevationScopes(session Session, enrollmentVersion int64, elevations []Elevation) ([]ElevationScope, error) {
	if session.Snapshot().Kind != SessionKindFull {
		return nil, nil
	}
	scopes := make([]ElevationScope, 0, len(elevations))
	for _, elevation := range elevations {
		scope := elevation.Snapshot().Scope
		if validateErr := elevation.Validate(session, enrollmentVersion, scope, service.clock.Now()); validateErr != nil {
			if errors.Is(validateErr, ErrElevationDenied) || errors.Is(validateErr, ErrElevationExpired) {
				continue
			}
			return nil, validateErr
		}
		scopes = append(scopes, scope)
	}
	sort.Slice(scopes, func(left, right int) bool { return scopes[left] < scopes[right] })
	return scopes, nil
}

func (service *Service) revokeOtherPreviewVersion(current Session, sessions []SessionInfo) string {
	hash := sha256.New()
	appendAdminDigestField(hash, "admin.revoke_other_preview.v1")
	appendAdminDigestField(hash, current.Snapshot().ID.String())
	appendAdminDigestField(hash, strconv.FormatInt(current.Snapshot().AdminVersion, 10))
	appendAdminDigestField(hash, strconv.FormatInt(current.Snapshot().SessionVersion, 10))
	items := append([]SessionInfo(nil), sessions...)
	sort.Slice(items, func(left, right int) bool {
		return items[left].Session.Snapshot().ID.String() < items[right].Session.Snapshot().ID.String()
	})
	for _, item := range items {
		appendAdminDigestField(hash, item.Session.Snapshot().ID.String())
		appendAdminDigestField(hash, strconv.FormatInt(item.Session.Snapshot().SessionVersion, 10))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
