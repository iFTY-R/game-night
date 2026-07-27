package auditlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"

	"github.com/iFTY-R/game-night/platform/security"
)

const (
	// cursorSchemaVersion invalidates outstanding tokens if the persisted cursor body changes.
	cursorSchemaVersion = 1
	// maximumCursorTokenBytes bounds attacker-controlled decoding work before JSON parsing.
	maximumCursorTokenBytes = 2048
	// cursorDomain prevents an admin user-list cursor from authenticating as an audit-list cursor.
	cursorDomain = "game-night/admin-audit-cursor/v1\x00"
)

type cursorBody struct {
	Version       int    `json:"v"`
	FilterDigest  []byte `json:"f"`
	AfterSequence uint64 `json:"a"`
}

type cursorEnvelope struct {
	KeyVersion uint32 `json:"k"`
	Body       []byte `json:"b"`
	MAC        []byte `json:"m"`
}

// CursorCodec authenticates one continuation sequence against the exact normalized audit filter.
type CursorCodec struct {
	keyring *security.HMACKeyring[security.AdminCursorKeyPurpose]
}

// NewCursorCodec reuses the dedicated management cursor keyring; the domain prefix isolates audit tokens.
func NewCursorCodec(keyring *security.HMACKeyring[security.AdminCursorKeyPurpose]) (*CursorCodec, error) {
	if keyring == nil || keyring.ActiveVersion() == 0 {
		return nil, ErrInvalidInput
	}
	return &CursorCodec{keyring: keyring}, nil
}

// Encode signs the last inspected audit sequence so a caller cannot skip or replay a different filter's page.
func (codec *CursorCodec) Encode(filterDigest [sha256.Size]byte, afterSequence uint64) (string, error) {
	if codec == nil || codec.keyring == nil || afterSequence == 0 {
		return "", ErrInvalidInput
	}
	body, err := json.Marshal(cursorBody{Version: cursorSchemaVersion, FilterDigest: filterDigest[:], AfterSequence: afterSequence})
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

// Decode returns the verified continuation sequence only when the token belongs to the current filter.
func (codec *CursorCodec) Decode(token string, expectedFilter [sha256.Size]byte) (uint64, error) {
	if codec == nil || codec.keyring == nil || token == "" || len(token) > maximumCursorTokenBytes {
		return 0, ErrInvalidInput
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token {
		return 0, ErrInvalidInput
	}
	var envelope cursorEnvelope
	if err = decodeCursorJSON(raw, &envelope); err != nil || len(envelope.Body) == 0 || len(envelope.MAC) != sha256.Size {
		return 0, ErrInvalidInput
	}
	matched, err := codec.keyring.Verify(cursorClaims(envelope.Body), security.MAC[security.AdminCursorKeyPurpose]{
		KeyVersion: envelope.KeyVersion,
		Value:      envelope.MAC,
	})
	if err != nil || !matched {
		return 0, ErrInvalidInput
	}
	var body cursorBody
	if err = decodeCursorJSON(envelope.Body, &body); err != nil || body.Version != cursorSchemaVersion ||
		len(body.FilterDigest) != sha256.Size || !bytes.Equal(body.FilterDigest, expectedFilter[:]) || body.AfterSequence == 0 {
		return 0, ErrInvalidInput
	}
	return body.AfterSequence, nil
}

func cursorClaims(body []byte) []byte {
	claims := make([]byte, 0, len(cursorDomain)+len(body))
	claims = append(claims, cursorDomain...)
	return append(claims, body...)
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
