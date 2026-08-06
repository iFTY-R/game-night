package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// minimumRuntimeSecretLength keeps the single deployment secret strong enough for derived HMAC keys.
const minimumRuntimeSecretLength = 32

// prepareRuntimeSecrets turns one deployment secret into stable, purpose-separated files for child processes.
// The files live only in the container's temporary filesystem; no secret directory or per-purpose env wiring is needed.
func prepareRuntimeSecrets(base environment) (environment, func(), error) {
	secret, ok := base.get(environmentSecret)
	secret = strings.TrimSpace(secret)
	if !ok || secret == "" {
		return base, func() {}, nil
	}
	if len([]byte(secret)) < minimumRuntimeSecretLength {
		return environment{}, nil, fmt.Errorf("launcher: %s must contain at least %d bytes", environmentSecret, minimumRuntimeSecretLength)
	}

	directory, err := os.MkdirTemp("", "game-night-secrets-")
	if err != nil {
		return environment{}, nil, errors.New("launcher: create runtime secret directory")
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := generateRuntimeKeyrings(directory, secret); err != nil {
		cleanup()
		return environment{}, nil, err
	}

	prepared := base.clone()
	prepared.set(environmentAPIKeyringDirectory, directory)
	prepared.set(environmentWorkerKeyringDirectory, directory)
	prepared.set(environmentAPIBootstrapSecretFile, filepath.Join(directory, "admin-bootstrap.txt"))
	prepared.set(environmentAPIRealtimeInternalToken, runtimeToken(secret, "realtime-internal"))
	prepared.set(environmentRealtimeInternalToken, runtimeToken(secret, "realtime-internal"))
	prepared.set(environmentAdminHeartbeatToken, runtimeToken(secret, "admin-heartbeat"))
	return prepared, cleanup, nil
}

func generateRuntimeKeyrings(directory, secret string) error {
	createdAt := time.Now().UTC()
	symmetricFiles := []string{
		"pii.json",
		"totp.json",
		"result-envelope.json",
		"device.json",
		"rate-limit.json",
		"user-challenge.json",
		"admin-challenge.json",
		"admin-session.json",
		"admin-cursor.json",
	}
	for _, filename := range symmetricFiles {
		material := deriveRuntimeKey(secret, filename)
		document := map[string]any{
			"active_version": 1,
			"keys": []any{map[string]any{
				"version":    1,
				"key":        base64.StdEncoding.EncodeToString(material),
				"not_before": createdAt,
			}},
		}
		if err := writeRuntimeSecret(filepath.Join(directory, filename), document); err != nil {
			return err
		}
	}

	seed := deriveRuntimeKey(secret, "audit.json")
	privateKey := ed25519.NewKeyFromSeed(seed)
	auditDocument := map[string]any{
		"active_version": 1,
		"keys": []any{map[string]any{
			"version":     1,
			"public_key":  base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
			"private_key": base64.StdEncoding.EncodeToString(privateKey),
			"not_before":  createdAt,
		}},
	}
	if err := writeRuntimeSecret(filepath.Join(directory, "audit.json"), auditDocument); err != nil {
		return err
	}
	return writeRuntimeSecret(filepath.Join(directory, "admin-bootstrap.txt"), []byte(secret))
}

func deriveRuntimeKey(secret, purpose string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("game-night/runtime-key/" + purpose))
	return mac.Sum(nil)
}

func runtimeToken(secret, purpose string) string {
	return base64.RawURLEncoding.EncodeToString(deriveRuntimeKey(secret, "token/"+purpose))
}

func writeRuntimeSecret(path string, value any) error {
	var contents []byte
	switch typed := value.(type) {
	case []byte:
		contents = append(typed, '\n')
	default:
		encoded, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return errors.New("launcher: encode runtime keyring")
		}
		contents = append(encoded, '\n')
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return errors.New("launcher: write runtime keyring")
	}
	defer file.Close()
	if _, err := file.Write(contents); err != nil {
		return errors.New("launcher: write runtime keyring")
	}
	if err := file.Chmod(0o400); err != nil {
		return errors.New("launcher: protect runtime keyring")
	}
	return nil
}
