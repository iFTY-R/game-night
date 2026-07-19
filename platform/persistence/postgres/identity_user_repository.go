package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	identityDomain "github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type identityUserQueries interface {
	CreateUser(context.Context, sqlcgen.CreateUserParams) (sqlcgen.User, error)
	GetUserByID(context.Context, sqlcgen.GetUserByIDParams) (sqlcgen.User, error)
	GetUserByUsernameKey(context.Context, sqlcgen.GetUserByUsernameKeyParams) (sqlcgen.User, error)
	GetUserForUpdate(context.Context, sqlcgen.GetUserForUpdateParams) (sqlcgen.User, error)
	CompleteOnboardingUserCAS(context.Context, sqlcgen.CompleteOnboardingUserCASParams) (sqlcgen.User, error)
	ChangeCurrentUsernameCAS(context.Context, sqlcgen.ChangeCurrentUsernameCASParams) (sqlcgen.User, error)
	SetCurrentUsernameCAS(context.Context, sqlcgen.SetCurrentUsernameCASParams) (sqlcgen.User, error)
	TransitionUserStatusCAS(context.Context, sqlcgen.TransitionUserStatusCASParams) (sqlcgen.User, error)
}

type identityUserRepository struct{ queries identityUserQueries }

func (repository *identityUserRepository) Insert(ctx context.Context, user identityDomain.User) (identityDomain.User, error) {
	snapshot := user.Snapshot()
	if snapshot.Status != identityDomain.UserStatusOnboarding {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.CreateUser(ctx, sqlcgen.CreateUserParams{
		UserID: uuidToPG(snapshot.ID), CreatedAt: timeToPG(snapshot.CreatedAt),
	})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityUserFromRow(row)
}

func (repository *identityUserRepository) GetByID(ctx context.Context, id uuid.UUID) (identityDomain.User, error) {
	if id == uuid.Nil {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.GetUserByID(ctx, sqlcgen.GetUserByIDParams{UserID: uuidToPG(id)})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrUserNotFound)
	}
	return identityUserFromRow(row)
}

// GetByUsernameKey resolves only the active claim that is still referenced by the owning user row.
func (repository *identityUserRepository) GetByUsernameKey(ctx context.Context, key string) (identityDomain.User, error) {
	if key == "" {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.GetUserByUsernameKey(ctx, sqlcgen.GetUserByUsernameKeyParams{UsernameKey: key})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrUserNotFound)
	}
	return identityUserFromRow(row)
}

func (repository *identityUserRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (identityDomain.User, error) {
	if id == uuid.Nil {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.GetUserForUpdate(ctx, sqlcgen.GetUserForUpdateParams{UserID: uuidToPG(id)})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrUserNotFound)
	}
	return identityUserFromRow(row)
}

func (repository *identityUserRepository) CompleteOnboardingCAS(
	ctx context.Context,
	current, next identityDomain.User,
) (identityDomain.User, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.ID != after.ID || before.Status != identityDomain.UserStatusOnboarding || after.Status != identityDomain.UserStatusActive {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	username, err := identifier.ParseUsername(after.Username)
	if err != nil {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	planned, err := current.CompleteOnboarding(username, after.UsernameChangedAt)
	if err != nil {
		return identityDomain.User{}, err
	}
	if planned.Snapshot() != after {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.CompleteOnboardingUserCAS(ctx, sqlcgen.CompleteOnboardingUserCASParams{
		DisplayUsername: pgtype.Text{String: after.Username, Valid: true},
		UsernameKey:     pgtype.Text{String: after.CurrentUsernameKey, Valid: true},
		ChangedAt:       timeToPG(after.UsernameChangedAt), UserID: uuidToPG(before.ID),
		ExpectedUpdatedAt: timeToPG(before.UpdatedAt), ExpectedCreatedAt: timeToPG(before.CreatedAt),
	})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityUserFromRow(row)
}

func (repository *identityUserRepository) ChangeUsernameCAS(
	ctx context.Context,
	current, next identityDomain.User,
) (identityDomain.User, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.ID != after.ID || before.Status != identityDomain.UserStatusActive || after.Status != identityDomain.UserStatusActive {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	username, err := identifier.ParseUsername(after.Username)
	if err != nil {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	plan, err := current.PlanUsernameChange(username, after.UsernameChangedAt)
	if err != nil {
		return identityDomain.User{}, err
	}
	if plan.Next.Snapshot() != after {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.ChangeCurrentUsernameCAS(ctx, sqlcgen.ChangeCurrentUsernameCASParams{
		DisplayUsername: pgtype.Text{String: after.Username, Valid: true},
		UsernameKey:     pgtype.Text{String: after.CurrentUsernameKey, Valid: true},
		ChangedAt:       timeToPG(after.UsernameChangedAt), UserID: uuidToPG(before.ID),
		ExpectedDisplayUsername:   pgtype.Text{String: before.Username, Valid: true},
		ExpectedUsernameKey:       pgtype.Text{String: before.CurrentUsernameKey, Valid: true},
		ExpectedUsernameChangedAt: timeToPG(before.UsernameChangedAt), ExpectedUpdatedAt: timeToPG(before.UpdatedAt),
		CooldownCutoff: timeToPG(plan.ChangedAt.Add(-identityDomain.UsernameChangeCooldown)),
	})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityUserFromRow(row)
}

// ForceChangeUsernameCAS persists a reviewed administrator plan without applying the user cooldown.
func (repository *identityUserRepository) ForceChangeUsernameCAS(
	ctx context.Context,
	current, next identityDomain.User,
) (identityDomain.User, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.ID != after.ID || before.Status != after.Status {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	username, err := identifier.ParseUsername(after.Username)
	if err != nil {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	plan, err := current.PlanForcedUsernameChange(username, after.UsernameChangedAt)
	if err != nil || plan.Next.Snapshot() != after {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.SetCurrentUsernameCAS(ctx, sqlcgen.SetCurrentUsernameCASParams{
		NextStatus: string(after.Status), DisplayUsername: pgtype.Text{String: after.Username, Valid: true},
		UsernameKey: pgtype.Text{String: after.CurrentUsernameKey, Valid: true}, ChangedAt: timeToPG(after.UpdatedAt),
		UserID: uuidToPG(before.ID), ExpectedStatus: string(before.Status),
		ExpectedUsernameKey: pgtype.Text{String: before.CurrentUsernameKey, Valid: before.CurrentUsernameKey != ""},
		ExpectedUpdatedAt:   timeToPG(before.UpdatedAt),
	})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityUserFromRow(row)
}

// TransitionStatusCAS persists only transitions produced by the governance state machine.
func (repository *identityUserRepository) TransitionStatusCAS(
	ctx context.Context,
	current, next identityDomain.User,
) (identityDomain.User, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.ID != after.ID {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	planned, err := current.TransitionForGovernance(after.Status, after.UpdatedAt)
	if err != nil || planned.Snapshot() != after {
		return identityDomain.User{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.TransitionUserStatusCAS(ctx, sqlcgen.TransitionUserStatusCASParams{
		NextStatus: string(after.Status), ChangedAt: timeToPG(after.UpdatedAt), UserID: uuidToPG(before.ID),
		ExpectedStatus: string(before.Status), ExpectedUpdatedAt: timeToPG(before.UpdatedAt),
	})
	if err != nil {
		return identityDomain.User{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityUserFromRow(row)
}

type identityClaimQueries interface {
	ClaimUsername(context.Context, sqlcgen.ClaimUsernameParams) (sqlcgen.UsernameClaim, error)
	GetUsernameClaimForUpdate(context.Context, sqlcgen.GetUsernameClaimForUpdateParams) (sqlcgen.UsernameClaim, error)
	ReserveUsernameClaimCAS(context.Context, sqlcgen.ReserveUsernameClaimCASParams) (sqlcgen.UsernameClaim, error)
}

type identityClaimRepository struct{ queries identityClaimQueries }

func (repository *identityClaimRepository) Claim(
	ctx context.Context,
	claim identityDomain.UsernameClaim,
	claimedAt time.Time,
) (identityDomain.UsernameClaim, error) {
	snapshot := claim.Snapshot()
	if snapshot.Status != identityDomain.UsernameClaimActive {
		return identityDomain.UsernameClaim{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.ClaimUsername(ctx, sqlcgen.ClaimUsernameParams{
		UsernameKey: snapshot.UsernameKey, DisplayUsername: snapshot.DisplayUsername,
		OwnerUserID: uuidToPG(snapshot.OwnerUserID), ClaimedAt: timeToPG(claimedAt),
	})
	if err != nil {
		return identityDomain.UsernameClaim{}, mapIdentityQueryError(ctx, err, identityDomain.ErrUsernameUnavailable)
	}
	return identityClaimFromRow(row)
}

func (repository *identityClaimRepository) GetForUpdate(ctx context.Context, key string) (identityDomain.UsernameClaim, error) {
	if key == "" {
		return identityDomain.UsernameClaim{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.GetUsernameClaimForUpdate(ctx, sqlcgen.GetUsernameClaimForUpdateParams{UsernameKey: key})
	if err != nil {
		return identityDomain.UsernameClaim{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityIntegrity)
	}
	return identityClaimFromRow(row)
}

func (repository *identityClaimRepository) ReserveCAS(
	ctx context.Context,
	current, next identityDomain.UsernameClaim,
) (identityDomain.UsernameClaim, error) {
	before, after := current.Snapshot(), next.Snapshot()
	if before.UsernameKey != after.UsernameKey || before.OwnerUserID != after.OwnerUserID ||
		before.Status != identityDomain.UsernameClaimActive || after.Status != identityDomain.UsernameClaimReserved {
		return identityDomain.UsernameClaim{}, identityDomain.ErrInvalidUserInput
	}
	row, err := repository.queries.ReserveUsernameClaimCAS(ctx, sqlcgen.ReserveUsernameClaimCASParams{
		ReservedUntil: timeToPG(after.ReservedUntil), ChangedAt: timeToPG(after.UpdatedAt),
		UsernameKey: before.UsernameKey, OwnerUserID: uuidToPG(before.OwnerUserID),
	})
	if err != nil {
		return identityDomain.UsernameClaim{}, mapIdentityQueryError(ctx, err, identityDomain.ErrIdentityConcurrentTransition)
	}
	return identityClaimFromRow(row)
}

func mapIdentityQueryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return noRowsError
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505":
			if pgError.ConstraintName == "username_claims_pkey" {
				return identityDomain.ErrUsernameUnavailable
			}
			return identityDomain.ErrIdentityConcurrentTransition
		case "23503", "23514":
			return identityDomain.ErrIdentityIntegrity
		case "40001", "40P01":
			return identityDomain.ErrIdentityConcurrentTransition
		}
	}
	return identityDomain.ErrIdentityRepositoryUnavailable
}

var _ identityDomain.UserRepository = (*identityUserRepository)(nil)
var _ identityDomain.UsernameClaimRepository = (*identityClaimRepository)(nil)
