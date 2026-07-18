package postgres

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	adminDomain "github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/persistence/postgres/sqlcgen"
)

func TestAdminChallengeMappingPreservesPersistedExpiredState(t *testing.T) {
	createdAt := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	origin, err := challenge.DigestOrigin("https://admin.example.test")
	if err != nil {
		t.Fatal(err)
	}
	row := sqlcgen.AdminChallenge{
		ChallengeID:      uuidToPG(uuid.New()),
		AdminID:          uuidToPG(uuid.New()),
		Selector:         base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, challenge.SelectorBytes)),
		SecretHash:       bytes.Repeat([]byte{0x22}, 32),
		SecretKeyVersion: 1,
		Purpose:          adminDomain.ChallengePurposeLogin.String(),
		Audience:         string(adminDomain.ChallengeAudience),
		AdminVersion:     1,
		PasswordVersion:  1,
		OriginHash:       origin.Bytes(),
		RequestFlowID:    "expired_flow",
		MaxAttempts:      3,
		Status:           "expired",
		CreatedAt:        timeToPG(createdAt),
		ExpiresAt:        timeToPG(createdAt.Add(challenge.TTL)),
	}

	restored, err := adminChallengeFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().PersistedState != challenge.StateExpired ||
		restored.State(createdAt.Add(time.Minute)) != challenge.StateExpired {
		t.Fatalf("restored challenge state = %+v", restored.Snapshot())
	}

	row.ConsumedAt = timeToPG(createdAt.Add(time.Minute))
	if _, err := adminChallengeFromRow(row); err != challenge.ErrIntegrity {
		t.Fatalf("expired row with consumed metadata error = %v", err)
	}
}
