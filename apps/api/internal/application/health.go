package application

import (
	"context"
	"errors"

	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/security"
)

var errDependencyUnavailable = errors.New("API dependency unavailable")

// checkpointChecker reads the durable chain head and checkpoint progress in one transaction.
type checkpointChecker struct {
	unitOfWork audit.UnitOfWork
	policy     *audit.CheckpointHealthPolicy
	clock      clock.Clock
}

func (checker checkpointChecker) Check(ctx context.Context) error {
	if checker.unitOfWork == nil || checker.policy == nil || checker.clock == nil || ctx == nil {
		return errDependencyUnavailable
	}
	// Refresh a potentially remote Object Lock probe before reserving a PostgreSQL transaction/connection.
	checker.policy.ProbeSink(ctx)
	return checker.unitOfWork.Run(ctx, func(ctx context.Context, transaction audit.Transaction) error {
		head, err := transaction.Audit().ReadHead(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		progress, err := transaction.Checkpoints().ReadCheckpointProgress(ctx, audit.ChainAdmin)
		if err != nil {
			return err
		}
		health, err := checker.policy.Evaluate(ctx, head.Sequence(), progress, checker.clock.Now())
		if err != nil {
			return err
		}
		if !health.Ready() {
			return audit.ErrSensitiveWriteBlocked
		}
		return nil
	})
}

// keyringChecker proves every independently typed keyring retained a usable active version after startup loading.
type keyringChecker struct{ keyrings security.Keyrings }

func (checker keyringChecker) Check(context.Context) error {
	keys := checker.keyrings
	if keys.PII == nil || keys.PII.ActiveVersion() == 0 || keys.TOTP == nil || keys.TOTP.ActiveVersion() == 0 ||
		keys.ResultEnvelope == nil || keys.ResultEnvelope.ActiveVersion() == 0 ||
		keys.Device == nil || keys.Device.ActiveVersion() == 0 || keys.RateLimit == nil || keys.RateLimit.ActiveVersion() == 0 ||
		keys.UserChallenge == nil || keys.UserChallenge.ActiveVersion() == 0 ||
		keys.AdminChallenge == nil || keys.AdminChallenge.ActiveVersion() == 0 ||
		keys.AdminSession == nil || keys.AdminSession.ActiveVersion() == 0 ||
		keys.Audit == nil || keys.Audit.ActiveVersion() == 0 {
		return errDependencyUnavailable
	}
	return nil
}
