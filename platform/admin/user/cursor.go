package user

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// adminUserCursorSchemaVersion invalidates tokens when the signed body shape changes.
	adminUserCursorSchemaVersion = 1
	// adminUserCursorMaximumTokenBytes bounds attacker-controlled decoding work at the HTTP boundary.
	adminUserCursorMaximumTokenBytes = 4096
	// adminUserCursorDomain prevents a valid MAC from another HMAC-backed feature authenticating as a cursor.
	adminUserCursorDomain = "game-night/admin-user-cursor/v1\x00"
)

type cursorBody struct {
	Version      int           `json:"v"`
	FilterDigest []byte        `json:"f"`
	SortField    UserSortField `json:"s"`
	Direction    SortDirection `json:"d"`
	SampledAt    int64         `json:"a"`
	SortTime     int64         `json:"t,omitempty"`
	SortText     string        `json:"x,omitempty"`
	UserID       uuid.UUID     `json:"u"`
}

type cursorEnvelope struct {
	KeyVersion uint32 `json:"k"`
	Body       []byte `json:"b"`
	MAC        []byte `json:"m"`
}

// CursorCodec authenticates list positions and binds them to one normalized filter/sort query.
type CursorCodec struct {
	keyring *security.HMACKeyring[security.AdminCursorKeyPurpose]
}

// NewCursorCodec uses a dedicated management-cursor keyring so a cursor cannot authenticate as a session or download grant.
func NewCursorCodec(keyring *security.HMACKeyring[security.AdminCursorKeyPurpose]) (*CursorCodec, error) {
	if keyring == nil || keyring.ActiveVersion() == 0 {
		return nil, ErrInvalidInput
	}
	return &CursorCodec{keyring: keyring}, nil
}

// Encode signs one sort position against a canonical SHA-256 filter digest.
func (codec *CursorCodec) Encode(
	filterDigest [sha256.Size]byte,
	sortField UserSortField,
	direction SortDirection,
	sampledAt time.Time,
	position UserListPosition,
) (string, error) {
	if codec == nil || codec.keyring == nil || sampledAt.IsZero() ||
		!validCursorPosition(sortField, direction, sampledAt, position) {
		return "", ErrInvalidInput
	}
	sortTime := int64(0)
	if !position.SortTime.IsZero() {
		sortTime = position.SortTime.Round(0).UTC().UnixNano()
	}
	body, err := json.Marshal(cursorBody{
		Version: adminUserCursorSchemaVersion, FilterDigest: filterDigest[:], SortField: sortField, Direction: direction,
		SampledAt: sampledAt.Round(0).UTC().UnixNano(), SortTime: sortTime, SortText: position.SortText, UserID: position.UserID,
	})
	if err != nil {
		return "", ErrInvalidInput
	}
	mac, err := codec.keyring.Sum(cursorClaims(body))
	if err != nil {
		return "", ErrInvalidInput
	}
	envelope, err := json.Marshal(cursorEnvelope{KeyVersion: mac.KeyVersion, Body: body, MAC: mac.Value})
	if err != nil {
		return "", ErrInvalidInput
	}
	return base64.RawURLEncoding.EncodeToString(envelope), nil
}

// Decode rejects tokens from another query, schema, sort, or key version before returning a database position.
func (codec *CursorCodec) Decode(
	token string,
	expectedFilter [sha256.Size]byte,
	sortField UserSortField,
	direction SortDirection,
) (time.Time, UserListPosition, error) {
	if codec == nil || codec.keyring == nil || token == "" || len(token) > adminUserCursorMaximumTokenBytes ||
		!validSort(sortField, direction) {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	var envelope cursorEnvelope
	if err = decodeCursorJSON(raw, &envelope); err != nil || len(envelope.Body) == 0 || len(envelope.MAC) != sha256.Size {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	matched, err := codec.keyring.Verify(
		cursorClaims(envelope.Body),
		security.MAC[security.AdminCursorKeyPurpose]{KeyVersion: envelope.KeyVersion, Value: envelope.MAC},
	)
	if err != nil || !matched {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	var body cursorBody
	if err = decodeCursorJSON(envelope.Body, &body); err != nil || body.Version != adminUserCursorSchemaVersion ||
		len(body.FilterDigest) != sha256.Size || string(body.FilterDigest) != string(expectedFilter[:]) ||
		body.SortField != sortField || body.Direction != direction || body.UserID == uuid.Nil || body.SampledAt <= 0 {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	sampledAt := time.Unix(0, body.SampledAt).UTC()
	position := UserListPosition{SortText: body.SortText, UserID: body.UserID}
	if body.SortTime != 0 {
		position.SortTime = time.Unix(0, body.SortTime).UTC()
	}
	if !validCursorPosition(sortField, direction, sampledAt, position) {
		return time.Time{}, UserListPosition{}, ErrInvalidInput
	}
	return sampledAt, position, nil
}

func cursorClaims(body []byte) []byte {
	claims := make([]byte, 0, len(adminUserCursorDomain)+len(body))
	claims = append(claims, adminUserCursorDomain...)
	return append(claims, body...)
}

func validSort(sortField UserSortField, direction SortDirection) bool {
	validField := sortField == UserSortCreatedAt || sortField == UserSortLastActivityAt ||
		sortField == UserSortUsername || sortField == UserSortUserID
	return validField && (direction == SortAscending || direction == SortDescending)
}

func validCursorPosition(
	sortField UserSortField,
	direction SortDirection,
	sampledAt time.Time,
	position UserListPosition,
) bool {
	if !validSort(sortField, direction) || position.UserID == uuid.Nil {
		return false
	}
	switch sortField {
	case UserSortCreatedAt, UserSortLastActivityAt:
		return !position.SortTime.IsZero() && !position.SortTime.After(sampledAt) && position.SortText == ""
	case UserSortUsername:
		return position.SortTime.IsZero() && len(position.SortText) <= 64 &&
			(position.SortText == "" || identifier.IsCanonicalUsernameKey(position.SortText))
	case UserSortUserID:
		return position.SortTime.IsZero() && position.SortText == ""
	default:
		return false
	}
}

func decodeCursorJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidInput
	}
	return nil
}
