package postgres

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// adminTagColorPattern matches the wire contract's canonical uppercase RGB representation.
var adminTagColorPattern = regexp.MustCompile(`^#[0-9A-F]{6}$`)

// adminErrorMessageKeyPattern limits durable failure details to stable localization keys rather than free-form diagnostics.
var adminErrorMessageKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

// AdminUserRepository keeps PII-free queries and annotation writes behind one PostgreSQL boundary.
type AdminUserRepository struct {
	queries *sqlcgen.Queries
	runner  *TransactionRunner
}

// NewAdminUserRepository binds user-center queries and transactional annotation replacement to one pool.
func NewAdminUserRepository(pool *pgxpool.Pool) *AdminUserRepository {
	return &AdminUserRepository{queries: sqlcgen.New(pool), runner: NewTransactionRunner(pool)}
}

// CreateTag advances the catalog and inserts the definition in one statement so uniqueness failure rolls both back.
func (repository *AdminUserRepository) CreateTag(ctx context.Context, command adminuser.CreateTagCommand) (adminuser.TagMutation, error) {
	name, normalized, ok := validTagDefinition(command.Name, command.Color)
	if repository == nil || repository.queries == nil || ctx == nil || !ok || command.TagID == uuid.Nil || command.ActorAdminID == uuid.Nil ||
		command.ExpectedCatalogVersion == 0 || command.CreatedAt.IsZero() || !validAdminReason(command.Reason) {
		return adminuser.TagMutation{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.CreateAdminUserTag(ctx, sqlcgen.CreateAdminUserTagParams{
		CreatedAt: timeToPG(command.CreatedAt), ExpectedCatalogVersion: int64(command.ExpectedCatalogVersion),
		TagID: uuidToPG(command.TagID), Name: name, NormalizedName: normalized, Color: command.Color,
		ActorAdminID: uuidToPG(command.ActorAdminID), Reason: strings.TrimSpace(command.Reason),
	})
	if err != nil {
		return adminuser.TagMutation{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	tag, err := adminUserTagFromValues(
		row.TagID, row.Name, row.NormalizedName, row.Color, row.Version, row.CreatedByAdminID, row.UpdatedByAdminID,
		row.Reason, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil || row.CatalogVersion <= 0 {
		return adminuser.TagMutation{}, adminuser.ErrIntegrity
	}
	return adminuser.TagMutation{Tag: tag, CatalogVersion: uint64(row.CatalogVersion)}, nil
}

// UpdateTag writes only the exact expected tag version and advances the catalog in the same statement.
func (repository *AdminUserRepository) UpdateTag(ctx context.Context, command adminuser.UpdateTagCommand) (adminuser.Tag, error) {
	name, normalized, ok := validTagDefinition(command.Name, command.Color)
	if repository == nil || repository.queries == nil || ctx == nil || !ok || command.TagID == uuid.Nil || command.ActorAdminID == uuid.Nil ||
		command.ExpectedVersion == 0 || command.UpdatedAt.IsZero() || !validAdminReason(command.Reason) {
		return adminuser.Tag{}, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.UpdateAdminUserTagCAS(ctx, sqlcgen.UpdateAdminUserTagCASParams{
		Name: name, NormalizedName: normalized, Color: command.Color, ActorAdminID: uuidToPG(command.ActorAdminID),
		Reason: strings.TrimSpace(command.Reason), UpdatedAt: timeToPG(command.UpdatedAt), TagID: uuidToPG(command.TagID),
		ExpectedVersion: int64(command.ExpectedVersion),
	})
	if err != nil {
		return adminuser.Tag{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminUserTagFromValues(
		row.TagID, row.Name, row.NormalizedName, row.Color, row.Version, row.CreatedByAdminID, row.UpdatedByAdminID,
		row.Reason, row.CreatedAt, row.UpdatedAt,
	)
}

// DeleteTag removes the exact unused version; assigned tags must be detached through versioned user mutations first.
func (repository *AdminUserRepository) DeleteTag(ctx context.Context, command adminuser.DeleteTagCommand) (uint64, error) {
	if repository == nil || repository.queries == nil || ctx == nil || command.TagID == uuid.Nil || command.ExpectedVersion == 0 || command.DeletedAt.IsZero() {
		return 0, adminuser.ErrInvalidInput
	}
	row, err := repository.queries.DeleteAdminUserTagCAS(ctx, sqlcgen.DeleteAdminUserTagCASParams{
		TagID: uuidToPG(command.TagID), ExpectedVersion: int64(command.ExpectedVersion), DeletedAt: timeToPG(command.DeletedAt),
	})
	if err != nil {
		// An assignment appearing after the caller's review invalidates the deletion instead of silently changing a user.
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			return 0, adminuser.ErrConflict
		}
		return 0, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	if row.CatalogVersion <= 0 {
		return 0, adminuser.ErrIntegrity
	}
	return uint64(row.CatalogVersion), nil
}

// ListTags returns a bounded deterministic keyset page and the catalog version sampled by the adjacent read.
func (repository *AdminUserRepository) ListTags(ctx context.Context, query adminuser.TagPageQuery) (adminuser.TagPage, error) {
	if repository == nil || repository.runner == nil || ctx == nil || query.PageSize == 0 || query.PageSize > 200 {
		return adminuser.TagPage{}, adminuser.ErrInvalidInput
	}
	_, normalizedPrefix, ok := normalizeAdminTagName(query.NamePrefix)
	_, normalizedAfter, afterOK := normalizeAdminTagName(query.AfterNormalizedName)
	if !ok || !afterOK || (query.AfterTagID == uuid.Nil) != (query.AfterNormalizedName == "") ||
		(query.AfterTagID != uuid.Nil && (normalizedAfter == "" || normalizedAfter != query.AfterNormalizedName)) {
		return adminuser.TagPage{}, adminuser.ErrInvalidInput
	}
	var rows []sqlcgen.AdminUserTag
	var catalog sqlcgen.AdminUserTagCatalog
	err := repository.runner.RunWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, queries QueryHandle) error {
		var err error
		catalog, err = queries.GetAdminUserTagCatalog(ctx)
		if err != nil {
			return err
		}
		rows, err = queries.ListAdminUserTags(ctx, sqlcgen.ListAdminUserTagsParams{
			NamePrefix: normalizedPrefix, AfterTagID: optionalUUID(query.AfterTagID),
			AfterNormalizedName: normalizedAfter, PageSize: int32(query.PageSize),
		})
		return err
	})
	if err != nil {
		return adminuser.TagPage{}, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	if catalog.CatalogVersion <= 0 {
		return adminuser.TagPage{}, adminuser.ErrIntegrity
	}
	result := make([]adminuser.Tag, 0, len(rows))
	for _, row := range rows {
		tag, mapErr := adminUserTagFromRow(row)
		if mapErr != nil {
			return adminuser.TagPage{}, mapErr
		}
		result = append(result, tag)
	}
	return adminuser.TagPage{Tags: result, CatalogVersion: uint64(catalog.CatalogVersion)}, nil
}

// SetTags replaces all links and advances the user version inside one transaction.
func (repository *AdminUserRepository) SetTags(ctx context.Context, command adminuser.SetTagsCommand) (uint64, error) {
	if repository == nil || repository.runner == nil || ctx == nil || command.UserID == uuid.Nil || command.ActorAdminID == uuid.Nil ||
		command.ExpectedVersion == 0 || command.ChangedAt.IsZero() || !validAdminReason(command.Reason) || len(command.TagIDs) > 32 {
		return 0, adminuser.ErrInvalidInput
	}
	tagIDs, ok := uniqueUUIDs(command.TagIDs)
	if !ok {
		return 0, adminuser.ErrInvalidInput
	}
	var nextVersion int64
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := queries.LockAdminUserForTagUpdate(ctx, sqlcgen.LockAdminUserForTagUpdateParams{UserID: uuidToPG(command.UserID)})
		if err != nil {
			return err
		}
		if locked.AccountVersion != int64(command.ExpectedVersion) {
			return adminuser.ErrConflict
		}
		if _, err = queries.DeleteAdminUserTagLinks(ctx, sqlcgen.DeleteAdminUserTagLinksParams{UserID: uuidToPG(command.UserID)}); err != nil {
			return err
		}
		if len(tagIDs) > 0 {
			if _, err = queries.InsertAdminUserTagLinks(ctx, sqlcgen.InsertAdminUserTagLinksParams{
				UserID: uuidToPG(command.UserID), ActorAdminID: uuidToPG(command.ActorAdminID), Reason: strings.TrimSpace(command.Reason),
				ChangedAt: timeToPG(command.ChangedAt), TagIds: tagIDs,
			}); err != nil {
				return err
			}
		}
		nextVersion, err = queries.IncrementAdminUserVersionCAS(ctx, sqlcgen.IncrementAdminUserVersionCASParams{
			UpdatedAt: timeToPG(command.ChangedAt), UserID: uuidToPG(command.UserID), ExpectedVersion: int64(command.ExpectedVersion),
		})
		return err
	})
	if err != nil {
		return 0, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	if nextVersion <= int64(command.ExpectedVersion) {
		return 0, adminuser.ErrIntegrity
	}
	return uint64(nextVersion), nil
}

// AppendNote inserts evidence once and advances the user CAS version; the database trigger rejects later UPDATE and DELETE attempts.
func (repository *AdminUserRepository) AppendNote(ctx context.Context, command adminuser.AppendNoteCommand) (adminuser.Note, error) {
	if repository == nil || repository.runner == nil || ctx == nil || command.NoteID == uuid.Nil || command.UserID == uuid.Nil ||
		command.AuthorAdminID == uuid.Nil || command.CreatedAt.IsZero() || !validAdminReason(command.Reason) ||
		!validAdminNoteBody(command.Body) || command.ExpectedVersion == 0 {
		return adminuser.Note{}, adminuser.ErrInvalidInput
	}
	var row sqlcgen.AdminUserNote
	err := repository.runner.Run(ctx, func(ctx context.Context, queries QueryHandle) error {
		locked, err := queries.LockAdminUserForTagUpdate(ctx, sqlcgen.LockAdminUserForTagUpdateParams{UserID: uuidToPG(command.UserID)})
		if err != nil {
			return err
		}
		if locked.AccountVersion != int64(command.ExpectedVersion) {
			return adminuser.ErrConflict
		}
		row, err = queries.AppendAdminUserNote(ctx, sqlcgen.AppendAdminUserNoteParams{
			NoteID: uuidToPG(command.NoteID), UserID: uuidToPG(command.UserID), AuthorAdminID: uuidToPG(command.AuthorAdminID),
			Body: command.Body, Reason: strings.TrimSpace(command.Reason), CreatedAt: timeToPG(command.CreatedAt),
		})
		if err != nil {
			return err
		}
		_, err = queries.IncrementAdminUserVersionCAS(ctx, sqlcgen.IncrementAdminUserVersionCASParams{
			UpdatedAt: timeToPG(command.CreatedAt), UserID: uuidToPG(command.UserID), ExpectedVersion: int64(command.ExpectedVersion),
		})
		return err
	})
	if err != nil {
		return adminuser.Note{}, mapAdminUserQueryError(ctx, err, adminuser.ErrConflict)
	}
	return adminUserNoteFromRow(row)
}

// ListNotes reads the immutable note timeline with the same tuple used by its descending index.
func (repository *AdminUserRepository) ListNotes(ctx context.Context, query adminuser.NotePageQuery) ([]adminuser.Note, error) {
	if repository == nil || repository.queries == nil || ctx == nil || query.UserID == uuid.Nil || query.PageSize == 0 || query.PageSize > 200 ||
		(query.AfterCreatedAt.IsZero() != (query.AfterNoteID == uuid.Nil)) {
		return nil, adminuser.ErrInvalidInput
	}
	rows, err := repository.queries.ListAdminUserNotes(ctx, sqlcgen.ListAdminUserNotesParams{
		UserID: uuidToPG(query.UserID), AfterCreatedAt: adminOptionalTimeToPG(query.AfterCreatedAt),
		AfterNoteID: optionalUUID(query.AfterNoteID), PageSize: int32(query.PageSize),
	})
	if err != nil {
		return nil, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	result := make([]adminuser.Note, 0, len(rows))
	for _, row := range rows {
		note, mapErr := adminUserNoteFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		result = append(result, note)
	}
	return result, nil
}

// ListUsers performs a PII-free sampled keyset read and loads page tags from the same database snapshot.
func (repository *AdminUserRepository) ListUsers(ctx context.Context, query adminuser.UserListQuery) ([]adminuser.UserRecord, error) {
	if repository == nil || repository.runner == nil || ctx == nil || query.PageSize == 0 || query.PageSize > 200 || query.SampledAt.IsZero() {
		return nil, adminuser.ErrInvalidInput
	}
	sortField, direction := query.SortField, query.Direction
	if sortField == "" {
		sortField = adminuser.UserSortCreatedAt
	}
	if direction == "" {
		direction = adminuser.SortDescending
	}
	usernamePrefix, err := identifier.NormalizeUsernamePrefix(query.UsernamePrefix)
	if err != nil || !validUserListRange(query.CreatedFrom, query.CreatedTo) || !validUserListRange(query.LastActivityFrom, query.LastActivityTo) ||
		!validUserListPosition(sortField, direction, query.SampledAt, query.After) {
		return nil, adminuser.ErrInvalidInput
	}
	statuses := append([]string(nil), query.Statuses...)
	seenStatuses := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if status != "onboarding" && status != "active" && status != "suspended" && status != "deleted" {
			return nil, adminuser.ErrInvalidInput
		}
		if _, duplicate := seenStatuses[status]; duplicate {
			return nil, adminuser.ErrInvalidInput
		}
		seenStatuses[status] = struct{}{}
	}
	tagIDs, ok := uniqueUUIDs(query.TagIDs)
	if !ok || len(tagIDs) > 32 {
		return nil, adminuser.ErrInvalidInput
	}
	params := sqlcgen.ListAdminUsersParams{
		UserID: optionalUUID(query.UserID), Statuses: statuses,
		UsernamePrefix: usernamePrefix, TagIds: tagIDs,
		CreatedFrom: adminOptionalTimeToPG(query.CreatedFrom), CreatedTo: adminOptionalTimeToPG(query.CreatedTo),
		LastActivityFrom: adminOptionalTimeToPG(query.LastActivityFrom), LastActivityTo: adminOptionalTimeToPG(query.LastActivityTo),
		AfterUserID: optionalUUID(query.After.UserID), SortField: string(sortField), SortDirection: string(direction),
		AfterSortTime: adminOptionalTimeToPG(query.After.SortTime), AfterSortText: cursorSortText(query.After, sortField),
		PageSize: int32(query.PageSize), SampledAt: timeToPG(query.SampledAt),
	}
	var rows []sqlcgen.ListAdminUsersRow
	var tagRows []sqlcgen.ListAdminUserTagsForUsersRow
	err = repository.runner.RunWithOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, queries QueryHandle) error {
		var err error
		rows, err = queries.ListAdminUsers(ctx, params)
		if err != nil || len(rows) == 0 {
			return err
		}
		userIDs := make([]pgtype.UUID, 0, len(rows))
		for _, row := range rows {
			userIDs = append(userIDs, row.UserID)
		}
		tagRows, err = queries.ListAdminUserTagsForUsers(ctx, sqlcgen.ListAdminUserTagsForUsersParams{UserIds: userIDs})
		return err
	})
	if err != nil {
		return nil, mapAdminUserQueryError(ctx, err, adminuser.ErrRepositoryUnavailable)
	}
	result := make([]adminuser.UserRecord, 0, len(rows))
	resultIndex := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil || row.AccountVersion <= 0 || !row.CreatedAt.Valid || !row.UpdatedAt.Valid ||
			!row.LastActivityAt.Valid || row.LastActivityAt.Time.Before(row.CreatedAt.Time) || row.LastActivityAt.Time.After(query.SampledAt) ||
			row.UsernameSortKey != row.CurrentUsernameKey.String {
			return nil, adminuser.ErrIntegrity
		}
		resultIndex[row.UserID.Bytes] = len(result)
		result = append(result, adminuser.UserRecord{
			ID: row.UserID.Bytes, Status: row.Status, Username: row.Username.String, CurrentUsernameKey: row.CurrentUsernameKey.String,
			Tags: make([]adminuser.Tag, 0), Version: uint64(row.AccountVersion), CreatedAt: canonicalPostgresTime(row.CreatedAt),
			UpdatedAt: canonicalPostgresTime(row.UpdatedAt), LastActivityAt: canonicalPostgresTime(row.LastActivityAt),
		})
	}
	for _, row := range tagRows {
		if !row.UserID.Valid || row.UserID.Bytes == uuid.Nil {
			return nil, adminuser.ErrIntegrity
		}
		index, exists := resultIndex[row.UserID.Bytes]
		if !exists {
			return nil, adminuser.ErrIntegrity
		}
		tag, mapErr := adminUserTagFromValues(
			row.TagID, row.Name, row.NormalizedName, row.Color, row.Version, row.CreatedByAdminID, row.UpdatedByAdminID,
			row.Reason, row.CreatedAt, row.UpdatedAt,
		)
		if mapErr != nil {
			return nil, mapErr
		}
		result[index].Tags = append(result[index].Tags, tag)
	}
	return result, nil
}

func validUserListRange(from, to time.Time) bool {
	return from.IsZero() || to.IsZero() || !from.After(to)
}

func validUserListPosition(
	sortField adminuser.UserSortField,
	direction adminuser.SortDirection,
	sampledAt time.Time,
	position adminuser.UserListPosition,
) bool {
	if sortField != adminuser.UserSortCreatedAt && sortField != adminuser.UserSortLastActivityAt &&
		sortField != adminuser.UserSortUsername && sortField != adminuser.UserSortUserID {
		return false
	}
	if direction != adminuser.SortAscending && direction != adminuser.SortDescending {
		return false
	}
	if position.UserID == uuid.Nil {
		return position.SortTime.IsZero() && position.SortText == ""
	}
	switch sortField {
	case adminuser.UserSortCreatedAt, adminuser.UserSortLastActivityAt:
		return !position.SortTime.IsZero() && !position.SortTime.After(sampledAt) && position.SortText == ""
	case adminuser.UserSortUsername:
		return position.SortTime.IsZero() && len(position.SortText) <= 64 &&
			(position.SortText == "" || identifier.IsCanonicalUsernameKey(position.SortText))
	case adminuser.UserSortUserID:
		return position.SortTime.IsZero() && position.SortText == ""
	default:
		return false
	}
}

func cursorSortText(position adminuser.UserListPosition, sortField adminuser.UserSortField) pgtype.Text {
	return pgtype.Text{String: position.SortText, Valid: position.UserID != uuid.Nil && sortField == adminuser.UserSortUsername}
}

func adminUserTagFromRow(row sqlcgen.AdminUserTag) (adminuser.Tag, error) {
	return adminUserTagFromValues(
		row.TagID, row.Name, row.NormalizedName, row.Color, row.Version, row.CreatedByAdminID, row.UpdatedByAdminID,
		row.Reason, row.CreatedAt, row.UpdatedAt,
	)
}

func adminUserTagFromValues(
	tagID pgtype.UUID,
	name, normalizedName, color string,
	version int64,
	createdBy, updatedBy pgtype.UUID,
	reason string,
	createdAt, updatedAt pgtype.Timestamptz,
) (adminuser.Tag, error) {
	if !tagID.Valid || tagID.Bytes == uuid.Nil || !createdBy.Valid || createdBy.Bytes == uuid.Nil || !updatedBy.Valid || updatedBy.Bytes == uuid.Nil ||
		version <= 0 || name == "" || normalizedName == "" || !adminTagColorPattern.MatchString(color) || !createdAt.Valid || !updatedAt.Valid {
		return adminuser.Tag{}, adminuser.ErrIntegrity
	}
	return adminuser.Tag{
		ID: tagID.Bytes, Name: name, NormalizedName: normalizedName, Color: color, Version: uint64(version),
		CreatedBy: createdBy.Bytes, UpdatedBy: updatedBy.Bytes, Reason: reason,
		CreatedAt: canonicalPostgresTime(createdAt), UpdatedAt: canonicalPostgresTime(updatedAt),
	}, nil
}

func adminUserNoteFromRow(row sqlcgen.AdminUserNote) (adminuser.Note, error) {
	if !row.NoteID.Valid || row.NoteID.Bytes == uuid.Nil || !row.UserID.Valid || row.UserID.Bytes == uuid.Nil ||
		!row.AuthorAdminID.Valid || row.AuthorAdminID.Bytes == uuid.Nil || row.Version != 1 || !row.CreatedAt.Valid {
		return adminuser.Note{}, adminuser.ErrIntegrity
	}
	return adminuser.Note{
		ID: row.NoteID.Bytes, UserID: row.UserID.Bytes, AuthorAdminID: row.AuthorAdminID.Bytes,
		Body: row.Body, Reason: row.Reason, Version: uint64(row.Version), CreatedAt: canonicalPostgresTime(row.CreatedAt),
	}, nil
}

func validTagDefinition(rawName, color string) (string, string, bool) {
	name, normalized, ok := normalizeAdminTagName(rawName)
	return name, normalized, ok && name != "" && adminTagColorPattern.MatchString(color)
}

func normalizeAdminTagName(rawName string) (string, string, bool) {
	if !utf8.ValidString(rawName) {
		return "", "", false
	}
	name := strings.TrimSpace(rawName)
	normalized := norm.NFKC.String(cases.Fold().String(name))
	nameLength, normalizedLength := utf8.RuneCountInString(name), utf8.RuneCountInString(normalized)
	return name, normalized, nameLength <= 64 && normalizedLength <= 64
}

func validAdminReason(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	length := utf8.RuneCountInString(trimmed)
	return utf8.ValidString(trimmed) && length >= 1 && length <= 512
}

func validAdminNoteBody(body string) bool {
	length := utf8.RuneCountInString(body)
	return utf8.ValidString(body) && strings.TrimSpace(body) != "" && length <= 4000
}

func validAdminErrorMessageKey(value string) bool {
	return len(value) <= 128 && adminErrorMessageKeyPattern.MatchString(value)
}

func uniqueUUIDs(values []uuid.UUID) ([]pgtype.UUID, bool) {
	result := make([]pgtype.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			return nil, false
		}
		seen[value] = struct{}{}
		result = append(result, uuidToPG(value))
	}
	return result, true
}

func adminOptionalTimeToPG(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return timeToPG(value)
}

func canonicalPostgresTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.Round(0).UTC()
}

func mapAdminUserQueryError(ctx context.Context, err, noRowsError error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, adminuser.ErrConflict) || errors.Is(err, adminuser.ErrIdempotencyConflict) || errors.Is(err, adminuser.ErrInvalidInput) {
		return err
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
		case "23505", "40001", "40P01":
			return adminuser.ErrConflict
		case "23503", "23514", "22000", "22023", "55000":
			return adminuser.ErrInvalidInput
		}
	}
	return adminuser.ErrRepositoryUnavailable
}

var _ adminuser.Repository = (*AdminUserRepository)(nil)
