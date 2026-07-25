// Package user owns administrator-facing user read models and persistence ports.
package user

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidInput rejects malformed persistence commands before they reach PostgreSQL.
	ErrInvalidInput = errors.New("invalid admin user input")
	// ErrNotFound hides whether an absent row was deleted or never existed.
	ErrNotFound = errors.New("admin user resource not found")
	// ErrConflict reports stale versions, consumed previews, lost leases, and other CAS failures.
	ErrConflict = errors.New("admin user concurrent transition")
	// ErrIdempotencyConflict reports reuse of an operation ID with a different request digest.
	ErrIdempotencyConflict = errors.New("admin user idempotency conflict")
	// ErrIntegrity reports persisted state that cannot satisfy the reviewed model.
	ErrIntegrity = errors.New("admin user persistence integrity failure")
	// ErrRepositoryUnavailable hides driver and schema diagnostics from callers.
	ErrRepositoryUnavailable = errors.New("admin user repository unavailable")
)

// Tag is the immutable persistence view of one administrator-defined user label.
type Tag struct {
	ID             uuid.UUID
	Name           string
	NormalizedName string
	Color          string
	Version        uint64
	CreatedBy      uuid.UUID
	UpdatedBy      uuid.UUID
	Reason         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateTagCommand binds a definition change to the global catalog CAS boundary.
type CreateTagCommand struct {
	TagID                  uuid.UUID
	Name                   string
	Color                  string
	ActorAdminID           uuid.UUID
	Reason                 string
	ExpectedCatalogVersion uint64
	CreatedAt              time.Time
}

// UpdateTagCommand guards one tag definition with its exact version.
type UpdateTagCommand struct {
	TagID           uuid.UUID
	Name            string
	Color           string
	ActorAdminID    uuid.UUID
	Reason          string
	ExpectedVersion uint64
	UpdatedAt       time.Time
}

// DeleteTagCommand removes one exact tag version and advances the catalog version.
type DeleteTagCommand struct {
	TagID           uuid.UUID
	ExpectedVersion uint64
	DeletedAt       time.Time
}

// TagPage is a deterministic name-ordered page paired with the current catalog version.
type TagPage struct {
	Tags           []Tag
	CatalogVersion uint64
}

// TagPageQuery continues normalized-name ordering at an exact name/tag-ID tuple.
type TagPageQuery struct {
	NamePrefix          string
	PageSize            uint32
	AfterNormalizedName string
	AfterTagID          uuid.UUID
}

// TagMutation returns both the changed tag and the catalog version when the catalog changes.
type TagMutation struct {
	Tag            Tag
	CatalogVersion uint64
}

// SetTagsCommand replaces the complete tag set while advancing the same user version used by governance.
type SetTagsCommand struct {
	UserID          uuid.UUID
	TagIDs          []uuid.UUID
	ActorAdminID    uuid.UUID
	Reason          string
	ExpectedVersion uint64
	ChangedAt       time.Time
}

// Note is append-only administrator evidence attached to one user.
type Note struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	AuthorAdminID uuid.UUID
	Body          string
	Reason        string
	Version       uint64
	CreatedAt     time.Time
}

// AppendNoteCommand carries the controlled note body while audit writers retain only its digest and ID.
type AppendNoteCommand struct {
	NoteID        uuid.UUID
	UserID        uuid.UUID
	AuthorAdminID uuid.UUID
	Body          string
	Reason        string
	CreatedAt     time.Time
}

// NotePageQuery continues the descending append history at an exact timestamp/ID tuple.
type NotePageQuery struct {
	UserID         uuid.UUID
	PageSize       uint32
	AfterCreatedAt time.Time
	AfterNoteID    uuid.UUID
}

// UserRecord is a PII-free list row; profile values are deliberately absent.
type UserRecord struct {
	ID                 uuid.UUID
	Status             string
	Username           string
	CurrentUsernameKey string
	Tags               []Tag
	Version            uint64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastActivityAt     time.Time
}

// UserSortField is the closed set of PostgreSQL-backed stable user-list sort keys.
type UserSortField string

const (
	// UserSortCreatedAt orders by immutable account creation time.
	UserSortCreatedAt UserSortField = "created_at"
	// UserSortLastActivityAt orders by the latest authoritative PostgreSQL activity timestamp.
	UserSortLastActivityAt UserSortField = "last_activity_at"
	// UserSortUsername orders by the normalized current username, with absent names first.
	UserSortUsername UserSortField = "username"
	// UserSortUserID orders directly by the stable user identifier.
	UserSortUserID UserSortField = "user_id"
)

// SortDirection fixes keyset comparison and ordering to the same direction.
type SortDirection string

const (
	// SortAscending advances to tuples greater than the cursor position.
	SortAscending SortDirection = "ascending"
	// SortDescending advances to tuples less than the cursor position.
	SortDescending SortDirection = "descending"
)

// UserListQuery is a composable, stable keyset query bound to one sampled upper boundary.
type UserListQuery struct {
	UserID           uuid.UUID
	Statuses         []string
	UsernamePrefix   string
	TagIDs           []uuid.UUID
	CreatedFrom      time.Time
	CreatedTo        time.Time
	LastActivityFrom time.Time
	LastActivityTo   time.Time
	PageSize         uint32
	SampledAt        time.Time
	SortField        UserSortField
	Direction        SortDirection
	After            UserListPosition
}

// UserListPosition carries the exact sort value and user-ID tiebreaker used by PostgreSQL.
type UserListPosition struct {
	SortTime time.Time
	SortText string
	UserID   uuid.UUID
}

// Repository exposes only persistence primitives needed by the user query and annotation services.
type Repository interface {
	CreateTag(context.Context, CreateTagCommand) (TagMutation, error)
	UpdateTag(context.Context, UpdateTagCommand) (Tag, error)
	DeleteTag(context.Context, DeleteTagCommand) (uint64, error)
	ListTags(context.Context, TagPageQuery) (TagPage, error)
	SetTags(context.Context, SetTagsCommand) (uint64, error)
	AppendNote(context.Context, AppendNoteCommand) (Note, error)
	ListNotes(context.Context, NotePageQuery) ([]Note, error)
	ListUsers(context.Context, UserListQuery) ([]UserRecord, error)
}
