package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errInvalidCleanupReport = errors.New("invalid worker cleanup report")

// CleanupReport contains only bounded row counts returned by the database-time cleanup function.
type CleanupReport struct {
	ClosedRooms       int64 `json:"closed_rooms"`
	ExpiredResults    int64 `json:"expired_results"`
	DeletedResults    int64 `json:"deleted_results"`
	ExpiredChallenges int64 `json:"expired_challenges"`
	DeletedChallenges int64 `json:"deleted_challenges"`
	ExpiredSessions   int64 `json:"expired_sessions"`
	DeletedSessions   int64 `json:"deleted_sessions"`
	ExpiredTOTP       int64 `json:"expired_totp"`
	DeletedTOTP       int64 `json:"deleted_totp"`
	ExpiredExports    int64 `json:"expired_exports"`
	DeletedExports    int64 `json:"deleted_exports"`
	ExpiredAttempts   int64 `json:"expired_attempts"`
	DeletedAttempts   int64 `json:"deleted_attempts"`
	ExpiredGrants     int64 `json:"expired_grants"`
	DeletedGrants     int64 `json:"deleted_grants"`
	DeletedClaims     int64 `json:"deleted_claims"`
	DeletedOnboarding int64 `json:"deleted_onboarding"`
}

// ExpiryCleanup calls the one transactional maintenance function with the worker role's narrow execute grant.
type ExpiryCleanup struct {
	queries         *sqlcgen.Queries
	roomIdleSeconds int64
}

// NewExpiryCleanup binds the worker-only maintenance port and validated room idle policy to a pool.
func NewExpiryCleanup(pool *pgxpool.Pool, roomIdleTimeout time.Duration) *ExpiryCleanup {
	if pool == nil || roomIdleTimeout < time.Minute || roomIdleTimeout > 24*time.Hour {
		return nil
	}
	return &ExpiryCleanup{queries: sqlcgen.New(pool), roomIdleSeconds: int64(roomIdleTimeout / time.Second)}
}

// RunOnce executes an idempotent database-time pass and hides count payloads from the worker loop.
func (cleanup *ExpiryCleanup) RunOnce(ctx context.Context) error {
	_, err := cleanup.RunReport(ctx)
	return err
}

// RunReport executes a pass and returns bounded operational counters for direct callers and tests.
func (cleanup *ExpiryCleanup) RunReport(ctx context.Context) (CleanupReport, error) {
	if cleanup == nil || cleanup.queries == nil {
		return CleanupReport{}, errInvalidCleanupReport
	}
	closedRooms, err := cleanup.queries.CloseExpiredPartyRooms(ctx, sqlcgen.CloseExpiredPartyRoomsParams{
		RoomIdleSeconds: cleanup.roomIdleSeconds,
	})
	if err != nil {
		return CleanupReport{}, err
	}
	payload, err := cleanup.queries.RunExpiryCleanup(ctx)
	if err != nil {
		return CleanupReport{}, err
	}
	var report CleanupReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return CleanupReport{}, errInvalidCleanupReport
	}
	report.ClosedRooms = closedRooms
	if !report.valid() {
		return CleanupReport{}, errInvalidCleanupReport
	}
	return report, nil
}

func (report CleanupReport) valid() bool {
	return report.ClosedRooms >= 0 && report.ExpiredResults >= 0 && report.DeletedResults >= 0 && report.ExpiredChallenges >= 0 &&
		report.DeletedChallenges >= 0 && report.ExpiredSessions >= 0 && report.DeletedSessions >= 0 &&
		report.ExpiredTOTP >= 0 && report.DeletedTOTP >= 0 && report.ExpiredExports >= 0 &&
		report.DeletedExports >= 0 && report.ExpiredAttempts >= 0 && report.DeletedAttempts >= 0 &&
		report.ExpiredGrants >= 0 && report.DeletedGrants >= 0 && report.DeletedClaims >= 0 &&
		report.DeletedOnboarding >= 0
}
