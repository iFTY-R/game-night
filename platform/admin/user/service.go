package user

import (
	"context"
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	admin "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/profile"
)

const (
	// DefaultUserPageSize keeps accidental first loads useful without allowing unbounded scans.
	DefaultUserPageSize uint32 = 50
	// MaximumUserPageSize mirrors the repository limit so transport adapters can clamp before querying.
	MaximumUserPageSize uint32 = 200
	// DefaultDetailNoteLimit gives detail drawers recent context without turning GetUser into a note export.
	DefaultDetailNoteLimit uint32 = 20
)

// AuditRecorder is the narrow side-effect port for sensitive reads and annotation writes.
type AuditRecorder interface {
	RecordPIIRead(context.Context, PIIAuditEvent) (uuid.UUID, error)
	RecordAnnotationWrite(context.Context, AnnotationAuditEvent) (uuid.UUID, error)
}

// PIIAuditEvent excludes plaintext and stores only the field names plus a digest of the reviewed reason.
type PIIAuditEvent struct {
	ActorAdminID uuid.UUID
	UserID       uuid.UUID
	Fields       []profile.Field
	Reason       string
	ReasonDigest [sha256.Size]byte
	RequestID    string
	OccurredAt   time.Time
}

// AnnotationAuditEvent binds tag/note changes to an operation and target without recording note body text.
type AnnotationAuditEvent struct {
	ActorAdminID uuid.UUID
	UserID       uuid.UUID
	OperationID  idempotency.OperationID
	Action       string
	Reason       string
	DetailDigest [sha256.Size]byte
	RequestID    string
	OccurredAt   time.Time
}

// Service coordinates authorization, cursor binding, PII decryption, and annotation audit for the user center.
type Service struct {
	repository Repository
	jobs       BatchRepository
	governance GovernanceExecutor
	// commandStore retains short-lived previews and idempotent single-user command outcomes independently from batch jobs.
	commandStore UserCommandRepository
	// singleGovernance owns destructive identity/device effects that must remain unavailable to ordinary query paths.
	singleGovernance SingleUserGovernanceExecutor
	profiles         ProfileReader
	protector        *profile.PIIProtector
	audit            AuditRecorder
	cursor           *CursorCodec
	clock            clock.Clock
}

// Config makes every side effect explicit so tests can run without hidden global time or audit state.
type Config struct {
	Repository       Repository
	Jobs             BatchRepository
	Governance       GovernanceExecutor
	UserCommands     UserCommandRepository
	SingleGovernance SingleUserGovernanceExecutor
	Profiles         ProfileReader
	Protector        *profile.PIIProtector
	Audit            AuditRecorder
	Cursor           *CursorCodec
	Clock            clock.Clock
}

// NewService validates the minimum query dependencies; PII dependencies are required only for GetUserPII.
func NewService(config Config) (*Service, error) {
	if config.Repository == nil || config.Clock == nil {
		return nil, ErrInvalidInput
	}
	return &Service{
		repository: config.Repository, jobs: config.Jobs, governance: config.Governance,
		commandStore: config.UserCommands, singleGovernance: config.SingleGovernance,
		profiles: config.Profiles, protector: config.Protector,
		audit: config.Audit, cursor: config.Cursor, clock: config.Clock,
	}, nil
}

// ListUsers normalizes filters, binds page tokens to the exact query digest, and never reads profile plaintext.
func (service *Service) ListUsers(ctx context.Context, actor admin.ActorContext, query ListUsersInput) (UserPage, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil {
		return UserPage{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersRead); err != nil {
		return UserPage{}, ErrPermissionDenied
	}
	normalized, digest, err := normalizeListUsersInput(query, service.clock.Now())
	if err != nil {
		return UserPage{}, err
	}
	if query.PageToken != "" {
		if service.cursor == nil {
			return UserPage{}, ErrInvalidInput
		}
		sampledAt, position, decodeErr := service.cursor.Decode(query.PageToken, digest, normalized.SortField, normalized.Direction)
		if decodeErr != nil {
			return UserPage{}, decodeErr
		}
		normalized.SampledAt = sampledAt
		normalized.After = position
	}
	rows, err := service.repository.ListUsers(ctx, normalized)
	if err != nil {
		return UserPage{}, err
	}
	nextToken := ""
	if len(rows) == int(normalized.PageSize) && service.cursor != nil {
		position := listPositionFor(rows[len(rows)-1], normalized.SortField)
		nextToken, err = service.cursor.Encode(digest, normalized.SortField, normalized.Direction, normalized.SampledAt, position)
		if err != nil {
			return UserPage{}, err
		}
	}
	return UserPage{Users: rows, PageSize: normalized.PageSize, NextPageToken: nextToken, SampledAt: normalized.SampledAt}, nil
}

// GetUser builds a PII-free detail view; only availability metadata is derived from profile state.
func (service *Service) GetUser(ctx context.Context, actor admin.ActorContext, userID uuid.UUID) (UserDetail, error) {
	if service == nil || service.repository == nil || service.clock == nil || ctx == nil || userID == uuid.Nil {
		return UserDetail{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersRead); err != nil {
		return UserDetail{}, ErrPermissionDenied
	}
	sampledAt := service.clock.Now()
	rows, err := service.repository.ListUsers(ctx, UserListQuery{
		UserID: userID, PageSize: 1, SampledAt: sampledAt, SortField: UserSortUserID, Direction: SortAscending,
	})
	if err != nil {
		return UserDetail{}, err
	}
	if len(rows) != 1 {
		return UserDetail{}, ErrNotFound
	}
	notes, err := service.repository.ListNotes(ctx, NotePageQuery{UserID: userID, PageSize: DefaultDetailNoteLimit})
	if err != nil {
		return UserDetail{}, err
	}
	availability, err := service.piiAvailability(ctx, userID)
	if err != nil {
		return UserDetail{}, err
	}
	return UserDetail{Summary: rows[0], PIIAvailability: availability, RecentNotes: notes, SampledAt: sampledAt}, nil
}

// GetUserPII is the only service method that decrypts profile plaintext, and it records audit before decryption.
func (service *Service) GetUserPII(ctx context.Context, actor admin.ActorContext, request GetUserPIIRequest) (PIIReadResult, error) {
	if service == nil || service.repository == nil || service.profiles == nil || service.protector == nil || service.audit == nil ||
		service.clock == nil || ctx == nil || request.UserID == uuid.Nil || !validReason(request.Reason) {
		return PIIReadResult{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersReadPII); err != nil {
		return PIIReadResult{}, ErrPermissionDenied
	}
	fields, err := normalizePIIFields(request.Fields)
	if err != nil {
		return PIIReadResult{}, err
	}
	now := service.clock.Now()
	auditID, err := service.audit.RecordPIIRead(ctx, PIIAuditEvent{
		ActorAdminID: actor.AdminID(), UserID: request.UserID, Fields: fields, Reason: strings.TrimSpace(request.Reason),
		ReasonDigest: sha256.Sum256([]byte(strings.TrimSpace(request.Reason))), RequestID: actor.RequestID(), OccurredAt: now,
	})
	if err != nil {
		return PIIReadResult{}, ErrAuditUnavailable
	}
	loaded, err := service.profiles.GetByID(ctx, request.UserID)
	if errors.Is(err, profile.ErrProfileNotFound) {
		return PIIReadResult{UserID: request.UserID, Values: nil, AccessAuditEventID: auditID, AccessedAt: now}, nil
	}
	if err != nil {
		return PIIReadResult{}, mapProfileReadError(err)
	}
	snapshot := loaded.Snapshot()
	values := make([]PIIValue, 0, len(fields))
	for _, field := range fields {
		switch field {
		case profile.FieldRealName:
			value, decryptErr := service.protector.DecryptRealName(request.UserID, loaded.EncryptedRealName())
			if decryptErr != nil {
				return PIIReadResult{}, mapProfileReadError(decryptErr)
			}
			values = append(values, PIIValue{Field: field, Value: value, Version: snapshot.ProfileVersion, UpdatedAt: snapshot.RealNameUpdatedAt})
		default:
			return PIIReadResult{}, ErrInvalidInput
		}
	}
	return PIIReadResult{UserID: request.UserID, Values: values, AccessAuditEventID: auditID, AccessedAt: now}, nil
}

// ListTags reads the administrator-managed tag catalog for filter pickers and assignment dialogs.
func (service *Service) ListTags(ctx context.Context, actor admin.ActorContext, query TagPageQuery) (TagPage, error) {
	if service == nil || service.repository == nil || ctx == nil {
		return TagPage{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersRead); err != nil {
		return TagPage{}, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultUserPageSize
	}
	return service.repository.ListTags(ctx, query)
}

// tagColorPattern is the canonical stored color shape; it must stay aligned with the PostgreSQL
// CHECK constraint on user_tag definitions (uppercase #RRGGBB) in the user-center migration.
var tagColorPattern = regexp.MustCompile(`^#[0-9A-F]{6}$`)

// canonicalTagColor folds operator-entered hex colors (any case, padded) into the single stored
// form so audit digests, idempotent replays, and the database constraint all see one value.
// Rejecting here keeps invalid colors from producing an audit event for a doomed write.
func canonicalTagColor(raw string) (string, bool) {
	color := strings.ToUpper(strings.TrimSpace(raw))
	return color, tagColorPattern.MatchString(color)
}

// CreateTag adds one catalog tag after an audited annotate-permission check.
func (service *Service) CreateTag(ctx context.Context, actor admin.ActorContext, command CreateTagCommand, operationID idempotency.OperationID) (TagMutation, error) {
	// Catalog versions are 1-based, so 0 can never match a live catalog; rejecting it here keeps a
	// doomed request from producing an audit event before persistence refuses it.
	if service == nil || service.repository == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		!operationID.Valid() || !validReason(command.Reason) || command.ExpectedCatalogVersion == 0 {
		return TagMutation{}, ErrInvalidInput
	}
	color, colorOK := canonicalTagColor(command.Color)
	if !colorOK {
		return TagMutation{}, ErrInvalidInput
	}
	command.Color = color
	if err := actor.Require(admin.PermissionUsersAnnotate); err != nil {
		return TagMutation{}, ErrPermissionDenied
	}
	command.ActorAdminID = actor.AdminID()
	command.CreatedAt = service.clock.Now()
	if _, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), OperationID: operationID, Action: "create_user_tag", Reason: strings.TrimSpace(command.Reason),
		DetailDigest: digestStrings(command.Name, command.Color), RequestID: actor.RequestID(), OccurredAt: command.CreatedAt,
	}); err != nil {
		return TagMutation{}, ErrAuditUnavailable
	}
	return service.repository.CreateTag(ctx, command)
}

// UpdateTag changes one exact tag version and records the reviewed operator reason before persistence.
func (service *Service) UpdateTag(ctx context.Context, actor admin.ActorContext, command UpdateTagCommand, operationID idempotency.OperationID) (Tag, error) {
	if service == nil || service.repository == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		command.TagID == uuid.Nil || !operationID.Valid() || !validReason(command.Reason) || command.ExpectedVersion == 0 {
		return Tag{}, ErrInvalidInput
	}
	color, colorOK := canonicalTagColor(command.Color)
	if !colorOK {
		return Tag{}, ErrInvalidInput
	}
	command.Color = color
	if err := actor.Require(admin.PermissionUsersAnnotate); err != nil {
		return Tag{}, ErrPermissionDenied
	}
	command.ActorAdminID = actor.AdminID()
	command.UpdatedAt = service.clock.Now()
	if _, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), OperationID: operationID, Action: "update_user_tag", Reason: strings.TrimSpace(command.Reason),
		DetailDigest: digestStrings(command.TagID.String(), command.Name, command.Color), RequestID: actor.RequestID(), OccurredAt: command.UpdatedAt,
	}); err != nil {
		return Tag{}, ErrAuditUnavailable
	}
	return service.repository.UpdateTag(ctx, command)
}

// DeleteTag removes an unused tag definition only at the exact reviewed version.
func (service *Service) DeleteTag(ctx context.Context, actor admin.ActorContext, command DeleteTagCommand, operationID idempotency.OperationID, reason string) (uint64, error) {
	if service == nil || service.repository == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		command.TagID == uuid.Nil || !operationID.Valid() || !validReason(reason) || command.ExpectedVersion == 0 {
		return 0, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersAnnotate); err != nil {
		return 0, ErrPermissionDenied
	}
	command.DeletedAt = service.clock.Now()
	if _, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), OperationID: operationID, Action: "delete_user_tag", Reason: strings.TrimSpace(reason),
		DetailDigest: digestStrings(command.TagID.String()), RequestID: actor.RequestID(), OccurredAt: command.DeletedAt,
	}); err != nil {
		return 0, ErrAuditUnavailable
	}
	return service.repository.DeleteTag(ctx, command)
}

// SetUserTags replaces the complete tag assignment only after annotate permission and durable audit are available.
func (service *Service) SetUserTags(ctx context.Context, actor admin.ActorContext, command SetUserTagsRequest) (uint64, error) {
	if service == nil || service.repository == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		command.UserID == uuid.Nil || !command.OperationID.Valid() || command.ExpectedVersion == 0 || !validReason(command.Reason) {
		return 0, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersAnnotate); err != nil {
		return 0, ErrPermissionDenied
	}
	now := service.clock.Now()
	if _, err := service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), UserID: command.UserID, OperationID: command.OperationID, Action: "set_user_tags",
		Reason: strings.TrimSpace(command.Reason), DetailDigest: digestUUIDs(command.TagIDs), RequestID: actor.RequestID(), OccurredAt: now,
	}); err != nil {
		return 0, ErrAuditUnavailable
	}
	return service.repository.SetTags(ctx, SetTagsCommand{
		UserID: command.UserID, TagIDs: command.TagIDs, ActorAdminID: actor.AdminID(), Reason: strings.TrimSpace(command.Reason),
		ExpectedVersion: command.ExpectedVersion, ChangedAt: now,
	})
}

// ListUserNotes pages the append-only annotation timeline without exposing unrelated users' notes.
func (service *Service) ListUserNotes(ctx context.Context, actor admin.ActorContext, query NotePageQuery) ([]Note, error) {
	if service == nil || service.repository == nil || ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersRead); err != nil {
		return nil, ErrPermissionDenied
	}
	if query.PageSize == 0 {
		query.PageSize = DefaultDetailNoteLimit
	}
	return service.repository.ListNotes(ctx, query)
}

// AppendUserNote stores the original note body but audits only an irreversible digest plus the note identifier.
func (service *Service) AppendUserNote(ctx context.Context, actor admin.ActorContext, command AppendUserNoteRequest) (Note, error) {
	if service == nil || service.repository == nil || service.audit == nil || service.clock == nil || ctx == nil ||
		command.UserID == uuid.Nil || !command.OperationID.Valid() || command.ExpectedVersion == 0 || !validReason(command.Reason) ||
		!validNoteBody(command.Body) {
		return Note{}, ErrInvalidInput
	}
	if err := actor.Require(admin.PermissionUsersAnnotate); err != nil {
		return Note{}, ErrPermissionDenied
	}
	now := service.clock.Now()
	noteID, err := uuid.NewV7()
	if err != nil {
		return Note{}, err
	}
	bodyDigest := sha256.Sum256([]byte(command.Body))
	if _, err = service.audit.RecordAnnotationWrite(ctx, AnnotationAuditEvent{
		ActorAdminID: actor.AdminID(), UserID: command.UserID, OperationID: command.OperationID, Action: "append_user_note",
		Reason: strings.TrimSpace(command.Reason), DetailDigest: bodyDigest, RequestID: actor.RequestID(), OccurredAt: now,
	}); err != nil {
		return Note{}, ErrAuditUnavailable
	}
	return service.repository.AppendNote(ctx, AppendNoteCommand{
		NoteID: noteID, UserID: command.UserID, AuthorAdminID: actor.AdminID(), Body: command.Body,
		Reason: strings.TrimSpace(command.Reason), ExpectedVersion: command.ExpectedVersion, CreatedAt: now,
	})
}

func (service *Service) piiAvailability(ctx context.Context, userID uuid.UUID) ([]PIIAvailability, error) {
	if service.profiles == nil {
		return []PIIAvailability{{Field: profile.FieldRealName, Available: false}}, nil
	}
	loaded, err := service.profiles.GetByID(ctx, userID)
	if errors.Is(err, profile.ErrProfileNotFound) {
		return []PIIAvailability{{Field: profile.FieldRealName, Available: false}}, nil
	}
	if err != nil {
		return nil, mapProfileReadError(err)
	}
	snapshot := loaded.Snapshot()
	return []PIIAvailability{{
		Field: profile.FieldRealName, Available: true, Version: snapshot.ProfileVersion, UpdatedAt: snapshot.RealNameUpdatedAt,
	}}, nil
}

func mapProfileReadError(err error) error {
	switch {
	case errors.Is(err, profile.ErrInvalidProfileInput):
		return ErrInvalidInput
	case errors.Is(err, profile.ErrProfileIntegrity), errors.Is(err, profile.ErrPIIAuthentication):
		return ErrIntegrity
	default:
		return ErrRepositoryUnavailable
	}
}

func validReason(reason string) bool {
	trimmed := strings.TrimSpace(reason)
	length := utf8.RuneCountInString(trimmed)
	return utf8.ValidString(trimmed) && length >= 1 && length <= 512
}

func validNoteBody(body string) bool {
	length := utf8.RuneCountInString(body)
	return utf8.ValidString(body) && strings.TrimSpace(body) != "" && length <= 4000
}

func normalizePIIFields(fields []profile.Field) ([]profile.Field, error) {
	if len(fields) == 0 || len(fields) > 8 {
		return nil, ErrInvalidInput
	}
	result := make([]profile.Field, 0, len(fields))
	seen := make(map[profile.Field]struct{}, len(fields))
	for _, field := range fields {
		if !field.Valid() {
			return nil, ErrInvalidInput
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, ErrInvalidInput
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	return result, nil
}

func listPositionFor(row UserRecord, sortField UserSortField) UserListPosition {
	position := UserListPosition{UserID: row.ID}
	switch sortField {
	case UserSortCreatedAt:
		position.SortTime = row.CreatedAt
	case UserSortLastActivityAt:
		position.SortTime = row.LastActivityAt
	case UserSortUsername:
		position.SortText = row.CurrentUsernameKey
	}
	return position
}

func digestUUIDs(values []uuid.UUID) [sha256.Size]byte {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(value.String()))
		hash.Write([]byte{0})
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func digestStrings(values ...string) [sha256.Size]byte {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(strings.TrimSpace(value)))
		hash.Write([]byte{0})
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}
