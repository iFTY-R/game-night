// Package config loads process-independent infrastructure and security settings for application binaries.
package config

import (
	"net/netip"
	"time"

	"github.com/iFTY-R/game-night/platform/security"
)

// LookupEnv matches os.LookupEnv and keeps process configuration deterministic in tests.
type LookupEnv func(string) (string, bool)

// Environment controls validations that must fail closed in a production deployment.
type Environment string

const (
	// EnvironmentDevelopment permits explicit local-only settings such as insecure cookies.
	EnvironmentDevelopment Environment = "development"
	// EnvironmentTest identifies isolated automated test processes.
	EnvironmentTest Environment = "test"
	// EnvironmentProduction enables deployment checks that protect browser credentials.
	EnvironmentProduction Environment = "production"
)

// PIIKeyringFile identifies only the keyring used to encrypt personally identifiable information.
type PIIKeyringFile string

// TOTPKeyringFile identifies only the keyring used to encrypt administrator TOTP seeds.
type TOTPKeyringFile string

// ResultEnvelopeKeyringFile identifies only the keyring used for short-lived secret result envelopes.
type ResultEnvelopeKeyringFile string

// DeviceKeyringFile identifies only the keyring used to authenticate device credential secrets.
type DeviceKeyringFile string

// RateLimitKeyringFile identifies only the keyring used to pseudonymize rate-limit dimensions.
type RateLimitKeyringFile string

// UserChallengeKeyringFile identifies only the signing keyring for user-side anonymous challenges.
type UserChallengeKeyringFile string

// AdminChallengeKeyringFile identifies only the signing keyring for administrator challenges.
type AdminChallengeKeyringFile string

// AdminSessionKeyringFile identifies only the HMAC keyring for administrator session and CSRF secrets.
type AdminSessionKeyringFile string

// AuditKeyringFile identifies only the Ed25519 keyring used to sign canonical audit events.
type AuditKeyringFile string

// BootstrapSecretFile is an optional, one-time administrator bootstrap password mount.
type BootstrapSecretFile string

// KeyringFiles preserves domain separation at compile time; key contents are loaded by security packages.
type KeyringFiles struct {
	PII            PIIKeyringFile
	TOTP           TOTPKeyringFile
	ResultEnvelope ResultEnvelopeKeyringFile
	Device         DeviceKeyringFile
	RateLimit      RateLimitKeyringFile
	UserChallenge  UserChallengeKeyringFile
	AdminChallenge AdminChallengeKeyringFile
	AdminSession   AdminSessionKeyringFile
	Audit          AuditKeyringFile
}

// OperationsKeyringFiles contains only the mounts required by worker cleanup, rotation, and signed audit work.
type OperationsKeyringFiles struct {
	PII   PIIKeyringFile
	TOTP  TOTPKeyringFile
	Audit AuditKeyringFile
}

// SecurityPaths preserves the reduced worker key authority when loading cryptographic material.
func (files OperationsKeyringFiles) SecurityPaths() security.OperationsKeyringPaths {
	return security.OperationsKeyringPaths{PII: string(files.PII), TOTP: string(files.TOTP), Audit: string(files.Audit)}
}

// SecurityPaths maps each named configuration type to the only matching cryptographic purpose.
func (files KeyringFiles) SecurityPaths() security.KeyringPaths {
	return security.KeyringPaths{
		PII:            string(files.PII),
		TOTP:           string(files.TOTP),
		ResultEnvelope: string(files.ResultEnvelope),
		Device:         string(files.Device),
		RateLimit:      string(files.RateLimit),
		UserChallenge:  string(files.UserChallenge),
		AdminChallenge: string(files.AdminChallenge),
		AdminSession:   string(files.AdminSession),
		Audit:          string(files.Audit),
	}
}

// PostgreSQLConfig bounds the runtime pool and selects the schema used by persistence adapters.
type PostgreSQLConfig struct {
	DSN                   string
	Schema                string
	MinConnections        int32
	MaxConnections        int32
	MaxConnectionLifetime time.Duration
	MaxConnectionIdleTime time.Duration
	HealthCheckPeriod     time.Duration
}

// RedisConfig configures the non-authoritative, fail-closed rate-limit store.
type RedisConfig struct {
	URL       string
	Timeout   time.Duration
	KeyPrefix string
}

// Origin is a canonical browser origin with no path, query, fragment, or credentials.
type Origin string

// OriginAllowlist contains the exact origins accepted for one browser security boundary.
type OriginAllowlist []Origin

// NetworkConfig separates user and administrator origins and defines which socket peers may proxy client IPs.
type NetworkConfig struct {
	UserOrigins    OriginAllowlist
	AdminOrigins   OriginAllowlist
	TrustedProxies []netip.Prefix
	CookieSecure   bool
}

// CheckpointConfig caps how long or how many audit events may remain outside the WORM checkpoint sink.
type CheckpointConfig struct {
	MaxEvents   int
	MaxInterval time.Duration
}

// Config is shared by API, worker, migration, and administrative command processes.
type Config struct {
	Environment         Environment
	PostgreSQL          PostgreSQLConfig
	Redis               RedisConfig
	Network             NetworkConfig
	Checkpoint          CheckpointConfig
	Keyrings            KeyringFiles
	BootstrapSecretFile BootstrapSecretFile
}

// WorkerDependencies omits browser, Redis, bootstrap, and authentication key material from the background process.
type WorkerDependencies struct {
	Environment Environment
	PostgreSQL  PostgreSQLConfig
	Checkpoint  CheckpointConfig
	Keyrings    OperationsKeyringFiles
}

// RealtimeDependencies omits every identity, administrator, PII, and audit key from the realtime process.
type RealtimeDependencies struct {
	Environment Environment
	PostgreSQL  PostgreSQLConfig
	Redis       RedisConfig
	Network     NetworkConfig
}
