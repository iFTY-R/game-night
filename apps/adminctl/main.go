package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	adminctlconfig "github.com/iFTY-R/game-night/apps/adminctl/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/secretfile"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/persistence/postgres"
	"github.com/iFTY-R/game-night/platform/security"
)

var (
	errLoadConfiguration = errors.New("load adminctl configuration")
	errResetFailed       = errors.New("administrator reset failed")
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), output io.Writer) error {
	config, command, err := adminctlconfig.Parse(args, lookup, io.Discard)
	if err != nil || command != "reset" {
		return errLoadConfiguration
	}
	secret, mounted, err := secretfile.Read(config.SecretFile)
	if err != nil || !mounted {
		return errResetFailed
	}
	// Strings cannot be explicitly zeroed; release the only local reference immediately after hashing.
	defer func() { secret = "" }()
	keyring, err := security.LoadAuditKeyring(config.AuditKeyring, time.Now().UTC())
	if err != nil {
		return errResetFailed
	}
	auditService, err := audit.NewService(keyring)
	if err != nil {
		return errResetFailed
	}
	argon2Service, err := security.NewArgon2Service(security.DefaultArgon2Params(), 1, 0)
	if err != nil {
		return errResetFailed
	}
	defer argon2Service.Close()
	password, err := admin.HashPassword(ctx, argon2Service, admin.DefaultPasswordPolicy(), "admin", secret)
	if err != nil {
		return errResetFailed
	}
	if config.DryRun {
		_, _ = io.WriteString(output, "admin reset dry-run valid\n")
		return nil
	}
	pool, err := postgres.OpenPool(ctx, postgres.PoolConfig{
		DatabaseURL: config.DatabaseURL, Schema: config.Schema, MinConnections: 1, MaxConnections: 2,
		MaxConnectionAge: time.Hour, MaxConnectionIdle: time.Minute, HealthCheckPeriod: time.Minute,
	})
	if err != nil {
		return errResetFailed
	}
	defer pool.Close()
	unitOfWork := postgres.NewAuditOutboxUnitOfWork(pool, auditService)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var resetResult postgres.AdminResetResult
	var expectedEvent audit.SignedEvent
	var checkpoint audit.Checkpoint
	err = unitOfWork.RunAdminReset(ctx, func(ctx context.Context, repository audit.Repository, reset func(context.Context, postgres.AdminResetRequest) (postgres.AdminResetResult, error)) error {
		head, err := repository.ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		actor, err := audit.NewActor(audit.ActorSystem, "adminctl")
		if err != nil {
			return err
		}
		target, err := audit.NewTarget(audit.TargetAdmin, "admin")
		if err != nil {
			return err
		}
		detailDigest := sha256.Sum256([]byte("game-night/adminctl/reset/v1"))
		event, err := auditService.Prepare(head, audit.EventInput{
			EventID: uuid.New(), RequestID: "adminctl.reset", OccurredAt: now, Actor: actor, Target: target,
			Action: audit.ActionAdminOfflineReset, ReasonCode: "offline_reset", DetailDigest: detailDigest[:],
		})
		if err != nil {
			return err
		}
		nextHead, err := event.NextHead()
		if err != nil {
			return err
		}
		checkpoint, err = auditService.PrepareCheckpoint(nextHead, now)
		if err != nil || auditService.VerifyCheckpoint(checkpoint) != nil {
			return audit.ErrIntegrity
		}
		eventSnapshot := event.Snapshot()
		checkpointSnapshot := checkpoint.Snapshot()
		resetResult, err = reset(ctx, postgres.AdminResetRequest{
			ExpectedPreviousHash: head.Hash().Bytes(), EventID: eventSnapshot.Event.EventID,
			CanonicalEvent: eventSnapshot.CanonicalEvent, Signature: eventSnapshot.Signature,
			SigningKeyVersion: int32(eventSnapshot.Event.SigningKeyVersion), CreatedAt: eventSnapshot.Event.OccurredAt,
			PasswordHash: password.Hash, PasswordAlgorithm: password.Algorithm, PasswordParameters: password.Parameters,
			CheckpointEventID: postgres.CheckpointEventID(checkpointSnapshot), CheckpointPayload: checkpointSnapshot.CanonicalPayload(),
		})
		expectedEvent = event
		return err
	})
	if err != nil {
		return errResetFailed
	}
	expectedSnapshot := expectedEvent.Snapshot()
	if resetResult.AppendedSequence != int64(expectedSnapshot.Event.Sequence) || string(resetResult.AppendedHash) != string(expectedSnapshot.EventHash.Bytes()) {
		return errResetFailed
	}
	if parsed, err := audit.ParseCheckpoint(checkpoint.Snapshot().CanonicalPayload()); err != nil || auditService.VerifyCheckpoint(parsed) != nil {
		return errResetFailed
	}
	_, _ = io.WriteString(output, "admin reset committed\n")
	return nil
}
