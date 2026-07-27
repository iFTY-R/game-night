package user

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// adminBatchCursorSchemaVersion invalidates all task-list tokens if their signed position shape changes.
	adminBatchCursorSchemaVersion = 1
	// adminBatchJobCursorDomain prevents a signed user-list cursor from becoming a valid batch-task cursor.
	adminBatchJobCursorDomain = "game-night/admin-batch-job-cursor/v1\x00"
	// adminBatchItemCursorDomain separates per-item cursors from batch-job cursors despite sharing the same keyring.
	adminBatchItemCursorDomain = "game-night/admin-batch-item-cursor/v1\x00"
)

// BatchJobListPosition is the stable keyset position for the selected batch-job ordering.
type BatchJobListPosition struct {
	SortTime   time.Time
	BatchJobID uuid.UUID
}

// BatchItemListPosition is the stable creation-order position for items within one batch job.
type BatchItemListPosition struct {
	CreatedAt time.Time
	ItemID    uuid.UUID
}

type batchJobCursorBody struct {
	Version      int               `json:"v"`
	FilterDigest []byte            `json:"f"`
	SortField    BatchJobSortField `json:"s"`
	Direction    SortDirection     `json:"d"`
	SortTime     int64             `json:"t,omitempty"`
	BatchJobID   uuid.UUID         `json:"j"`
}

type batchItemCursorBody struct {
	Version      int       `json:"v"`
	FilterDigest []byte    `json:"f"`
	BatchJobID   uuid.UUID `json:"j"`
	CreatedAt    int64     `json:"c"`
	ItemID       uuid.UUID `json:"i"`
}

// EncodeBatchJob signs one task-list position against its exact filter and ordering.
func (codec *CursorCodec) EncodeBatchJob(
	filterDigest [sha256.Size]byte,
	sortField BatchJobSortField,
	direction SortDirection,
	position BatchJobListPosition,
) (string, error) {
	if codec == nil || codec.keyring == nil || !validBatchJobCursorPosition(sortField, direction, position) {
		return "", ErrInvalidInput
	}
	sortTime := int64(0)
	if !position.SortTime.IsZero() {
		sortTime = position.SortTime.Round(0).UTC().UnixNano()
	}
	return codec.encodeBatchCursor(batchJobCursorBody{
		Version: adminBatchCursorSchemaVersion, FilterDigest: filterDigest[:], SortField: sortField, Direction: direction,
		SortTime: sortTime, BatchJobID: position.BatchJobID,
	}, adminBatchJobCursorDomain)
}

// DecodeBatchJob returns a verified task-list position only when it belongs to the same filter and ordering.
func (codec *CursorCodec) DecodeBatchJob(
	token string,
	expectedFilter [sha256.Size]byte,
	sortField BatchJobSortField,
	direction SortDirection,
) (BatchJobListPosition, error) {
	if codec == nil || codec.keyring == nil || !validBatchJobSort(sortField, direction) {
		return BatchJobListPosition{}, ErrInvalidInput
	}
	var body batchJobCursorBody
	if err := codec.decodeBatchCursor(token, adminBatchJobCursorDomain, &body); err != nil ||
		body.Version != adminBatchCursorSchemaVersion || len(body.FilterDigest) != sha256.Size ||
		!bytes.Equal(body.FilterDigest, expectedFilter[:]) || body.SortField != sortField || body.Direction != direction || body.BatchJobID == uuid.Nil {
		return BatchJobListPosition{}, ErrInvalidInput
	}
	position := BatchJobListPosition{BatchJobID: body.BatchJobID}
	if body.SortTime != 0 {
		position.SortTime = time.Unix(0, body.SortTime).UTC()
	}
	if !validBatchJobCursorPosition(sortField, direction, position) {
		return BatchJobListPosition{}, ErrInvalidInput
	}
	return position, nil
}

// EncodeBatchItem signs one immutable creation-order position for a single batch job.
func (codec *CursorCodec) EncodeBatchItem(
	filterDigest [sha256.Size]byte,
	batchJobID uuid.UUID,
	position BatchItemListPosition,
) (string, error) {
	if codec == nil || codec.keyring == nil || batchJobID == uuid.Nil || !validBatchItemCursorPosition(position) {
		return "", ErrInvalidInput
	}
	return codec.encodeBatchCursor(batchItemCursorBody{
		Version: adminBatchCursorSchemaVersion, FilterDigest: filterDigest[:], BatchJobID: batchJobID,
		CreatedAt: position.CreatedAt.Round(0).UTC().UnixNano(), ItemID: position.ItemID,
	}, adminBatchItemCursorDomain)
}

// DecodeBatchItem rejects tokens that were issued for a different job or state filter before exposing a position.
func (codec *CursorCodec) DecodeBatchItem(
	token string,
	expectedFilter [sha256.Size]byte,
	batchJobID uuid.UUID,
) (BatchItemListPosition, error) {
	if codec == nil || codec.keyring == nil || batchJobID == uuid.Nil {
		return BatchItemListPosition{}, ErrInvalidInput
	}
	var body batchItemCursorBody
	if err := codec.decodeBatchCursor(token, adminBatchItemCursorDomain, &body); err != nil ||
		body.Version != adminBatchCursorSchemaVersion || len(body.FilterDigest) != sha256.Size ||
		!bytes.Equal(body.FilterDigest, expectedFilter[:]) || body.BatchJobID != batchJobID || body.ItemID == uuid.Nil || body.CreatedAt <= 0 {
		return BatchItemListPosition{}, ErrInvalidInput
	}
	position := BatchItemListPosition{CreatedAt: time.Unix(0, body.CreatedAt).UTC(), ItemID: body.ItemID}
	if !validBatchItemCursorPosition(position) {
		return BatchItemListPosition{}, ErrInvalidInput
	}
	return position, nil
}

func (codec *CursorCodec) encodeBatchCursor(body any, domain string) (string, error) {
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return "", ErrInvalidInput
	}
	mac, err := codec.keyring.Sum(batchCursorClaims(domain, encodedBody))
	if err != nil {
		return "", ErrInvalidInput
	}
	envelope, err := json.Marshal(cursorEnvelope{KeyVersion: mac.KeyVersion, Body: encodedBody, MAC: mac.Value})
	if err != nil {
		return "", ErrInvalidInput
	}
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

func (codec *CursorCodec) decodeBatchCursor(token, domain string, target any) error {
	if token == "" || len(token) > adminUserCursorMaximumTokenBytes {
		return ErrInvalidInput
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ErrInvalidInput
	}
	var envelope cursorEnvelope
	if err = decodeCursorJSON(raw, &envelope); err != nil || len(envelope.Body) == 0 || len(envelope.MAC) != sha256.Size {
		return ErrInvalidInput
	}
	matched, err := codec.keyring.Verify(
		batchCursorClaims(domain, envelope.Body),
		security.MAC[security.AdminCursorKeyPurpose]{KeyVersion: envelope.KeyVersion, Value: envelope.MAC},
	)
	if err != nil || !matched {
		return ErrInvalidInput
	}
	if err = decodeCursorJSON(envelope.Body, target); err != nil {
		return ErrInvalidInput
	}
	return nil
}

func batchCursorClaims(domain string, body []byte) []byte {
	claims := make([]byte, 0, len(domain)+len(body))
	claims = append(claims, domain...)
	return append(claims, body...)
}

func batchJobQueryDigest(query BatchJobListQuery) [sha256.Size]byte {
	states := make([]string, len(query.States))
	copy(states, query.States)
	sort.Strings(states)
	commands := make([]string, 0, len(query.Commands))
	for _, command := range query.Commands {
		commands = append(commands, string(command))
	}
	sort.Strings(commands)
	payload, _ := json.Marshal(struct {
		States      []string `json:"states"`
		Commands    []string `json:"commands"`
		CreatedFrom int64    `json:"created_from"`
		CreatedTo   int64    `json:"created_to"`
	}{
		States: states, Commands: commands,
		CreatedFrom: batchCursorTime(query.CreatedFrom), CreatedTo: batchCursorTime(query.CreatedTo),
	})
	return sha256.Sum256(payload)
}

func batchItemQueryDigest(batchJobID uuid.UUID, states []string) [sha256.Size]byte {
	canonicalStates := make([]string, len(states))
	copy(canonicalStates, states)
	sort.Strings(canonicalStates)
	payload, _ := json.Marshal(struct {
		BatchJobID uuid.UUID `json:"batch_job_id"`
		States     []string  `json:"states"`
	}{BatchJobID: batchJobID, States: canonicalStates})
	return sha256.Sum256(payload)
}

func batchCursorTime(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Round(0).UTC().UnixNano()
}

func batchJobListPosition(job BatchJob, sortField BatchJobSortField) (BatchJobListPosition, error) {
	position := BatchJobListPosition{BatchJobID: job.ID}
	switch sortField {
	case BatchJobSortCreatedAt:
		position.SortTime = job.CreatedAt
	case BatchJobSortUpdatedAt:
		position.SortTime = job.UpdatedAt
	case BatchJobSortID:
	default:
		return BatchJobListPosition{}, ErrInvalidInput
	}
	if !validBatchJobCursorPosition(sortField, SortAscending, position) && !validBatchJobCursorPosition(sortField, SortDescending, position) {
		return BatchJobListPosition{}, ErrIntegrity
	}
	return position, nil
}

func batchItemListPosition(item BatchItem) (BatchItemListPosition, error) {
	position := BatchItemListPosition{CreatedAt: item.CreatedAt, ItemID: item.ID}
	if !validBatchItemCursorPosition(position) {
		return BatchItemListPosition{}, ErrIntegrity
	}
	return position, nil
}

func validBatchJobCursorPosition(sortField BatchJobSortField, direction SortDirection, position BatchJobListPosition) bool {
	if !validBatchJobSort(sortField, direction) || position.BatchJobID == uuid.Nil {
		return false
	}
	switch sortField {
	case BatchJobSortCreatedAt, BatchJobSortUpdatedAt:
		return !position.SortTime.IsZero()
	case BatchJobSortID:
		return position.SortTime.IsZero()
	default:
		return false
	}
}

func validBatchItemCursorPosition(position BatchItemListPosition) bool {
	return position.ItemID != uuid.Nil && !position.CreatedAt.IsZero()
}
