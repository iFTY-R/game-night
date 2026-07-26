package user

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/profile"
)

// ListUsersInput is the application-facing filter before cursor decoding fixes sampled time and position.
type ListUsersInput struct {
	UserID           uuid.UUID
	Statuses         []string
	UsernamePrefix   string
	TagIDs           []uuid.UUID
	CreatedFrom      time.Time
	CreatedTo        time.Time
	LastActivityFrom time.Time
	LastActivityTo   time.Time
	PageSize         uint32
	PageToken        string
	SortField        UserSortField
	Direction        SortDirection
}

// UserPage returns a sampled read model and a token that is cryptographically bound to the normalized query.
type UserPage struct {
	Users         []UserRecord
	PageSize      uint32
	NextPageToken string
	SampledAt     time.Time
}

// GetUserPIIRequest is separate from GetUser so plaintext values cannot be conditionally expanded into ordinary details.
type GetUserPIIRequest struct {
	UserID uuid.UUID
	Fields []profile.Field
	Reason string
}

// PIIReadResult carries the audit event identifier that authorized the plaintext response.
type PIIReadResult struct {
	UserID             uuid.UUID
	Values             []PIIValue
	AccessAuditEventID uuid.UUID
	AccessedAt         time.Time
}

// SetUserTagsRequest is the authorized service command for versioned tag replacement.
type SetUserTagsRequest struct {
	OperationID     idempotency.OperationID
	UserID          uuid.UUID
	TagIDs          []uuid.UUID
	Reason          string
	ExpectedVersion uint64
}

// AppendUserNoteRequest is the authorized service command for append-only notes.
type AppendUserNoteRequest struct {
	OperationID     idempotency.OperationID
	UserID          uuid.UUID
	Body            string
	Reason          string
	ExpectedVersion uint64
}

type normalizedFilterDigest struct {
	UserID           string    `json:"user_id,omitempty"`
	Statuses         []string  `json:"statuses,omitempty"`
	UsernamePrefix   string    `json:"username_prefix,omitempty"`
	TagIDs           []string  `json:"tag_ids,omitempty"`
	CreatedFrom      time.Time `json:"created_from,omitempty"`
	CreatedTo        time.Time `json:"created_to,omitempty"`
	LastActivityFrom time.Time `json:"last_activity_from,omitempty"`
	LastActivityTo   time.Time `json:"last_activity_to,omitempty"`
}

func normalizeListUsersInput(input ListUsersInput, sampledAt time.Time) (UserListQuery, [sha256.Size]byte, error) {
	if sampledAt.IsZero() {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	pageSize := input.PageSize
	if pageSize == 0 {
		pageSize = DefaultUserPageSize
	}
	sortField := input.SortField
	if sortField == "" {
		sortField = UserSortCreatedAt
	}
	direction := input.Direction
	if direction == "" {
		direction = SortDescending
	}
	if pageSize == 0 || pageSize > MaximumUserPageSize || !validSort(sortField, direction) {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	usernamePrefix, err := identifier.NormalizeUsernamePrefix(input.UsernamePrefix)
	if err != nil || !validUserListRange(input.CreatedFrom, input.CreatedTo) || !validUserListRange(input.LastActivityFrom, input.LastActivityTo) {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	statuses := append([]string(nil), input.Statuses...)
	if !validStatuses(statuses) {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	tagIDs, ok := uniqueDomainUUIDs(input.TagIDs)
	if !ok || len(tagIDs) > 32 {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	query := UserListQuery{
		UserID: input.UserID, Statuses: statuses, UsernamePrefix: usernamePrefix, TagIDs: tagIDs,
		CreatedFrom: canonicalQueryTime(input.CreatedFrom), CreatedTo: canonicalQueryTime(input.CreatedTo),
		LastActivityFrom: canonicalQueryTime(input.LastActivityFrom), LastActivityTo: canonicalQueryTime(input.LastActivityTo),
		PageSize: pageSize, SampledAt: canonicalQueryTime(sampledAt), SortField: sortField, Direction: direction,
	}
	digest, err := digestUserFilter(query)
	if err != nil {
		return UserListQuery{}, [sha256.Size]byte{}, ErrInvalidInput
	}
	return query, digest, nil
}

func digestUserFilter(query UserListQuery) ([sha256.Size]byte, error) {
	tagIDs := make([]string, 0, len(query.TagIDs))
	for _, tagID := range query.TagIDs {
		tagIDs = append(tagIDs, tagID.String())
	}
	userID := ""
	if query.UserID != uuid.Nil {
		userID = query.UserID.String()
	}
	body, err := json.Marshal(normalizedFilterDigest{
		UserID: userID, Statuses: query.Statuses, UsernamePrefix: query.UsernamePrefix, TagIDs: tagIDs,
		CreatedFrom: query.CreatedFrom, CreatedTo: query.CreatedTo,
		LastActivityFrom: query.LastActivityFrom, LastActivityTo: query.LastActivityTo,
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(body), nil
}

func validStatuses(statuses []string) bool {
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if status != "onboarding" && status != "active" && status != "suspended" && status != "deleted" {
			return false
		}
		if _, exists := seen[status]; exists {
			return false
		}
		seen[status] = struct{}{}
	}
	return true
}

func validUserListRange(from, to time.Time) bool {
	return from.IsZero() || to.IsZero() || !from.After(to)
}

func uniqueDomainUUIDs(values []uuid.UUID) ([]uuid.UUID, bool) {
	result := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			return nil, false
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}

func canonicalQueryTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}
