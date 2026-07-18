package secretresult

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/security"
)

// testReplayCapability deliberately uses the full issue-consume-authorize protocol so tests cannot bypass it.
func testReplayCapability(t testing.TB, now time.Time, result Result) challenge.Authorization {
	t.Helper()
	keyring, err := security.LoadHMACKeyring[security.UserChallengeKeyPurpose](testChallengeKeyringPath(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := challenge.NewService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	origin, err := challenge.DigestOrigin("https://play.example")
	if err != nil {
		t.Fatal(err)
	}
	requestBinding := challenge.Binding{
		Purpose:       "identity.bootstrap",
		Audience:      "identity_api",
		Origin:        origin,
		RequestFlowID: "flow_bootstrap",
	}
	issued, err := service.Issue(requestBinding, challenge.DefaultMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot()
	consumed, err := issued.Challenge.Consume(now, challenge.ReplayAuthorization{
		OperationID:   snapshot.Binding.Key.OperationID,
		RequestDigest: snapshot.Binding.RequestDigest,
		ResultID:      snapshot.ID,
		ReplayUntil:   now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := service.Authorize(
		consumed,
		requestBinding,
		issued.Credentials,
		snapshot.Binding.Key.OperationID,
		snapshot.Binding.RequestDigest,
	)
	if err != nil || authorization.Kind() != challenge.AuthorizeExactReplay {
		t.Fatalf("replay authorization = %+v, err=%v", authorization, err)
	}
	return authorization
}

func testChallengeKeyringPath(t testing.TB, now time.Time) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "challenge-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}
