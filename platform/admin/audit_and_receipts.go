package admin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/idempotency"
)

func (service *Service) requireHealthyAudit(ctx context.Context, transaction Transaction) error {
	if service == nil || service.audit == nil || service.checkpointHealth == nil {
		return nil
	}
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

func (service *Service) appendAdminAudit(ctx context.Context, transaction Transaction, adminID uuid.UUID, requestID string, targetType audit.TargetType, targetID string, action audit.Action, reasonCode string, detailDigest []byte) (uuid.UUID, error) {
	if service == nil || service.audit == nil || service.checkpointHealth == nil || transaction == nil {
		eventID, err := uuid.NewV7()
		if err != nil {
			return uuid.UUID{}, err
		}
		return eventID, nil
	}
	if err := service.requireHealthyAudit(ctx, transaction); err != nil {
		return uuid.UUID{}, err
	}
	actor, err := audit.NewActor(audit.ActorAdmin, adminID.String())
	if err != nil {
		return uuid.UUID{}, err
	}
	head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
	if err != nil {
		return uuid.UUID{}, err
	}
	target, err := audit.NewTarget(targetType, strings.TrimSpace(targetID))
	if err != nil {
		return uuid.UUID{}, err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		requestID = "admin-auth:" + fmt.Sprintf("%d", action) + ":" + adminID.String()
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return uuid.UUID{}, err
	}
	event, err := service.audit.Prepare(head, audit.EventInput{
		EventID: eventID, RequestID: requestID, OccurredAt: service.clock.Now(),
		Actor: actor, Target: target, Action: action, ReasonCode: reasonCode, DetailDigest: detailDigest,
	})
	if err != nil {
		return uuid.UUID{}, err
	}
	next, err := transaction.Audit().AppendEvent(ctx, audit.AppendRequest{ExpectedHead: head, Event: event})
	if err != nil {
		return uuid.UUID{}, err
	}
	progress, err := transaction.AuditCheckpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
	if err != nil {
		return uuid.UUID{}, err
	}
	health, err := service.checkpointHealth.Evaluate(ctx, next.Sequence(), progress, service.clock.Now())
	if err != nil || !health.CheckpointDue() {
		return eventID, err
	}
	checkpoint, err := service.audit.PrepareCheckpoint(next, service.clock.Now())
	if err != nil {
		return uuid.UUID{}, err
	}
	if err = transaction.AuditCheckpoints().AppendPendingCheckpoint(ctx, checkpoint); err != nil {
		return uuid.UUID{}, err
	}
	return eventID, nil
}

func (service *Service) auditLoginFailure(ctx context.Context, adminID uuid.UUID, requestID, reason string) error {
	if service == nil || service.audit == nil || service.checkpointHealth == nil || adminID == uuid.Nil {
		return nil
	}
	return service.unitOfWork.Run(ctx, func(ctx context.Context, transaction Transaction) error {
		_, err := service.appendAdminAudit(ctx, transaction, adminID, requestID, audit.TargetAdmin, adminID.String(), audit.ActionAdminLoginFailed, reason, digestAdminRequest("admin.login.failed", reason).Bytes())
		return err
	})
}

func (service *Service) saveCommandReceipt(ctx context.Context, transaction Transaction, adminID uuid.UUID, operationID idempotency.OperationID, requestDigest idempotency.Digest, command, target string, resultAdminVersion, resultPasswordVersion, resultSessionVersion, resultEnrollmentVersion int64, auditEventID uuid.UUID) error {
	if transaction.CommandReceipts() == nil {
		return nil
	}
	if auditEventID == uuid.Nil {
		var err error
		auditEventID, err = uuid.NewV7()
		if err != nil {
			return err
		}
	}
	_, err := transaction.CommandReceipts().Save(ctx, CommandReceipt{
		AdminID: adminID, OperationID: operationID, RequestDigest: requestDigest,
		Command: command, TargetType: command, TargetID: target,
		ResultAdminVersion: resultAdminVersion, ResultPasswordVersion: resultPasswordVersion, ResultSessionVersion: resultSessionVersion,
		ResultEnrollmentVersion: resultEnrollmentVersion, AuditEventID: auditEventID, CreatedAt: service.clock.Now(),
	})
	return err
}

func (service *Service) replayPasswordChangeReceipt(ctx context.Context, transaction Transaction, adminID uuid.UUID, command ChangePasswordCommand) (*ChangePasswordResult, error) {
	if transaction.CommandReceipts() == nil {
		return nil, nil
	}
	receipt, err := transaction.CommandReceipts().Get(ctx, adminID, command.OperationID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	digest := digestAdminRequest("admin.change_password", command.Current, command.New, strconv.FormatInt(command.ExpectedPasswordVersion, 10))
	if receipt.RequestDigest != digest || receipt.Command != "change_admin_password" {
		return nil, ErrIdempotencyConflict
	}
	parts, err := decodeReceiptTarget(receipt.TargetID, 2)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseReceiptUUID(parts[0])
	if err != nil {
		return nil, err
	}
	revokedSessions, err := parseReceiptInt64(parts[1])
	if err != nil {
		return nil, err
	}
	session, err := transaction.Sessions().GetByIDForUpdate(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &ChangePasswordResult{Session: IssuedSession{Session: session}, RevokedSessions: revokedSessions}, nil
}

func (service *Service) replayDisableTotpReceipt(ctx context.Context, transaction Transaction, adminID uuid.UUID, command DisableTotpCommand) (*DisableTotpResult, error) {
	if transaction.CommandReceipts() == nil {
		return nil, nil
	}
	receipt, err := transaction.CommandReceipts().Get(ctx, adminID, command.OperationID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	digest := digestAdminRequest("admin.disable_totp", command.Reason, strconv.FormatInt(command.ExpectedEnrollmentVersion, 10))
	if receipt.RequestDigest != digest || receipt.Command != "disable_totp" {
		return nil, ErrIdempotencyConflict
	}
	parts, err := decodeReceiptTarget(receipt.TargetID, 3)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseReceiptUUID(parts[0])
	if err != nil {
		return nil, err
	}
	revokedSessions, err := parseReceiptInt64(parts[1])
	if err != nil {
		return nil, err
	}
	alreadyDisabled, err := parseReceiptBool(parts[2])
	if err != nil {
		return nil, err
	}
	session, err := transaction.Sessions().GetByIDForUpdate(ctx, sessionID)
	if err != nil {
		if alreadyDisabled && sessionID == command.Session.Snapshot().ID {
			return &DisableTotpResult{Session: IssuedSession{Session: command.Session}, RevokedSessions: revokedSessions, AlreadyDisabled: true}, nil
		}
		return nil, err
	}
	return &DisableTotpResult{Session: IssuedSession{Session: session}, RevokedSessions: revokedSessions, AlreadyDisabled: alreadyDisabled}, nil
}

func (service *Service) replaySingleSessionRevokeReceipt(ctx context.Context, transaction Transaction, adminID uuid.UUID, command RevokeAdminSessionCommand) (*RevokeAdminSessionResult, error) {
	if transaction.CommandReceipts() == nil {
		return nil, nil
	}
	receipt, err := transaction.CommandReceipts().Get(ctx, adminID, command.OperationID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	digest := digestAdminRequest("admin.revoke_session", command.TargetSessionID.String(), strconv.FormatInt(command.ExpectedSessionVersion, 10))
	if receipt.RequestDigest != digest || receipt.Command != "revoke_admin_session" {
		return nil, ErrIdempotencyConflict
	}
	parts, err := decodeReceiptTarget(receipt.TargetID, 2)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseReceiptUUID(parts[0])
	if err != nil {
		return nil, err
	}
	revoked, err := parseReceiptBool(parts[1])
	if err != nil {
		return nil, err
	}
	return &RevokeAdminSessionResult{SessionID: sessionID, Revoked: revoked}, nil
}

func (service *Service) replayBulkSessionRevokeReceipt(ctx context.Context, transaction Transaction, adminID uuid.UUID, command RevokeOtherSessionsCommand) (*RevokeOtherSessionsResult, error) {
	if transaction.CommandReceipts() == nil {
		return nil, nil
	}
	receipt, err := transaction.CommandReceipts().Get(ctx, adminID, command.OperationID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	digest := digestAdminRequest("admin.revoke_other_sessions", command.PreviewVersion, strconv.FormatInt(command.ExpectedAdminVersion, 10), strconv.FormatInt(command.ExpectedCurrentSessionVersion, 10))
	if receipt.RequestDigest != digest || receipt.Command != "revoke_other_admin_sessions" {
		return nil, ErrIdempotencyConflict
	}
	parts, err := decodeReceiptTarget(receipt.TargetID, 2)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseReceiptUUID(parts[0])
	if err != nil {
		return nil, err
	}
	revokedSessions, err := parseReceiptInt64(parts[1])
	if err != nil {
		return nil, err
	}
	session, err := transaction.Sessions().GetByIDForUpdate(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &RevokeOtherSessionsResult{RevokedSessions: revokedSessions, Session: session}, nil
}
