package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Environment variable names are kept with their parsers so validation errors can identify fields without values.
	environmentName                          = "GAME_NIGHT_ENVIRONMENT"
	databaseURLEnvironment                   = "GAME_NIGHT_DATABASE_URL"
	databaseSchemaEnvironment                = "GAME_NIGHT_DATABASE_SCHEMA"
	databaseMinConnectionsEnvironment        = "GAME_NIGHT_DATABASE_MIN_CONNECTIONS"
	databaseMaxConnectionsEnvironment        = "GAME_NIGHT_DATABASE_MAX_CONNECTIONS"
	databaseMaxConnectionLifetimeEnvironment = "GAME_NIGHT_DATABASE_MAX_CONNECTION_LIFETIME"
	databaseMaxConnectionIdleTimeEnvironment = "GAME_NIGHT_DATABASE_MAX_CONNECTION_IDLE_TIME"
	databaseHealthCheckPeriodEnvironment     = "GAME_NIGHT_DATABASE_HEALTH_CHECK_PERIOD"
	redisURLEnvironment                      = "GAME_NIGHT_REDIS_URL"
	redisTimeoutEnvironment                  = "GAME_NIGHT_REDIS_TIMEOUT"
	redisKeyPrefixEnvironment                = "GAME_NIGHT_REDIS_KEY_PREFIX"
	checkpointMaxEventsEnvironment           = "GAME_NIGHT_AUDIT_CHECKPOINT_MAX_EVENTS"
	checkpointMaxIntervalEnvironment         = "GAME_NIGHT_AUDIT_CHECKPOINT_MAX_INTERVAL"
	bootstrapSecretFileEnvironment           = "GAME_NIGHT_ADMIN_BOOTSTRAP_SECRET_FILE"
	piiKeyringFileEnvironment                = "GAME_NIGHT_PII_KEYRING_FILE"
	totpKeyringFileEnvironment               = "GAME_NIGHT_TOTP_KEYRING_FILE"
	resultEnvelopeKeyringFileEnvironment     = "GAME_NIGHT_RESULT_ENVELOPE_KEYRING_FILE"
	deviceKeyringFileEnvironment             = "GAME_NIGHT_DEVICE_KEYRING_FILE"
	rateLimitKeyringFileEnvironment          = "GAME_NIGHT_RATE_LIMIT_KEYRING_FILE"
	userChallengeKeyringFileEnvironment      = "GAME_NIGHT_USER_CHALLENGE_KEYRING_FILE"
	adminChallengeKeyringFileEnvironment     = "GAME_NIGHT_ADMIN_CHALLENGE_KEYRING_FILE"
	adminSessionKeyringFileEnvironment       = "GAME_NIGHT_ADMIN_SESSION_KEYRING_FILE"
	adminCursorKeyringFileEnvironment        = "GAME_NIGHT_ADMIN_CURSOR_KEYRING_FILE"
	auditKeyringFileEnvironment              = "GAME_NIGHT_AUDIT_KEYRING_FILE"
	// Pool defaults limit connection pressure while allowing operators to tune within a hard process cap.
	defaultDatabaseSchema                = "public"
	defaultDatabaseMinConnections        = 1
	defaultDatabaseMaxConnections        = 10
	maximumDatabaseConnections           = 100
	defaultDatabaseMaxConnectionLifetime = time.Hour
	maximumDatabaseMaxConnectionLifetime = 24 * time.Hour
	defaultDatabaseMaxConnectionIdleTime = 30 * time.Minute
	maximumDatabaseMaxConnectionIdleTime = time.Hour
	defaultDatabaseHealthCheckPeriod     = time.Minute
	maximumDatabaseHealthCheckPeriod     = 5 * time.Minute
	// Redis operations fail quickly because the protected flows must fail closed rather than queue indefinitely.
	defaultRedisTimeout = time.Second
	maximumRedisTimeout = 30 * time.Second
	// Checkpoint ceilings match the design's fail-closed boundary and cannot be relaxed by deployment config.
	defaultCheckpointMaxEvents   = 100
	maximumCheckpointMaxEvents   = 100
	defaultCheckpointMaxInterval = 5 * time.Minute
	maximumCheckpointMaxInterval = 5 * time.Minute
)

var (
	// PostgreSQL identifiers remain unquoted so every adapter resolves the same schema safely.
	postgresIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)
	// Redis prefixes are operational namespaces and must remain bounded and delimiter-terminated.
	redisKeyPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,62}:$`)
)

// Load reads and validates shared settings before any network client, keyring, or listener is opened.
// It never includes configured values in returned errors because DSNs and file paths may contain secrets.
func Load(lookupEnv LookupEnv) (Config, error) {
	if lookupEnv == nil {
		return Config{}, errors.New("LookupEnv: invalid configuration")
	}
	reader := environmentReader{lookup: lookupEnv}

	environment, err := loadEnvironment(reader)
	if err != nil {
		return Config{}, err
	}
	postgres, err := loadPostgreSQL(reader)
	if err != nil {
		return Config{}, err
	}
	redisConfig, err := loadRedis(reader)
	if err != nil {
		return Config{}, err
	}
	network, err := loadNetwork(reader, environment)
	if err != nil {
		return Config{}, err
	}
	checkpoint, err := loadCheckpoint(reader)
	if err != nil {
		return Config{}, err
	}
	keyrings, err := loadKeyringFiles(reader)
	if err != nil {
		return Config{}, err
	}
	bootstrapFile, err := optionalAbsolutePath(reader, bootstrapSecretFileEnvironment)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment:         environment,
		PostgreSQL:          postgres,
		Redis:               redisConfig,
		Network:             network,
		Checkpoint:          checkpoint,
		Keyrings:            keyrings,
		BootstrapSecretFile: BootstrapSecretFile(bootstrapFile),
	}, nil
}

// LoadWorker reads only dependencies that the background process is authorized to use.
func LoadWorker(lookupEnv LookupEnv) (WorkerDependencies, error) {
	if lookupEnv == nil {
		return WorkerDependencies{}, errors.New("LookupEnv: invalid configuration")
	}
	reader := environmentReader{lookup: lookupEnv}
	environment, err := loadEnvironment(reader)
	if err != nil {
		return WorkerDependencies{}, err
	}
	postgres, err := loadPostgreSQL(reader)
	if err != nil {
		return WorkerDependencies{}, err
	}
	checkpoint, err := loadCheckpoint(reader)
	if err != nil {
		return WorkerDependencies{}, err
	}
	pii, err := requiredAbsolutePath(reader, piiKeyringFileEnvironment)
	if err != nil {
		return WorkerDependencies{}, err
	}
	totp, err := requiredAbsolutePath(reader, totpKeyringFileEnvironment)
	if err != nil {
		return WorkerDependencies{}, err
	}
	auditFile, err := requiredAbsolutePath(reader, auditKeyringFileEnvironment)
	if err != nil {
		return WorkerDependencies{}, err
	}
	if pii == totp || pii == auditFile || totp == auditFile {
		return WorkerDependencies{}, fieldError(auditKeyringFileEnvironment, "keyring file is already assigned")
	}
	return WorkerDependencies{
		Environment: environment, PostgreSQL: postgres, Checkpoint: checkpoint,
		Keyrings: OperationsKeyringFiles{PII: PIIKeyringFile(pii), TOTP: TOTPKeyringFile(totp), Audit: AuditKeyringFile(auditFile)},
	}, nil
}

// LoadRealtime reads only authoritative game persistence, Redis coordination, and browser Origin policy.
func LoadRealtime(lookupEnv LookupEnv) (RealtimeDependencies, error) {
	if lookupEnv == nil {
		return RealtimeDependencies{}, errors.New("LookupEnv: invalid configuration")
	}
	reader := environmentReader{lookup: lookupEnv}
	environment, err := loadEnvironment(reader)
	if err != nil {
		return RealtimeDependencies{}, err
	}
	postgres, err := loadPostgreSQL(reader)
	if err != nil {
		return RealtimeDependencies{}, err
	}
	redisConfig, err := loadRedis(reader)
	if err != nil {
		return RealtimeDependencies{}, err
	}
	network, err := loadNetwork(reader, environment)
	if err != nil {
		return RealtimeDependencies{}, err
	}
	return RealtimeDependencies{
		Environment: environment, PostgreSQL: postgres, Redis: redisConfig, Network: network,
	}, nil
}

type environmentReader struct {
	lookup LookupEnv
}

func (r environmentReader) optional(name string) string {
	value, _ := r.lookup(name)
	return strings.TrimSpace(value)
}

func (r environmentReader) valueOrDefault(name, fallback string) string {
	if value := r.optional(name); value != "" {
		return value
	}
	return fallback
}

func (r environmentReader) required(name string) (string, error) {
	value := r.optional(name)
	if value == "" {
		return "", fieldError(name, "required configuration is missing")
	}
	return value, nil
}

func loadEnvironment(reader environmentReader) (Environment, error) {
	raw, err := reader.required(environmentName)
	if err != nil {
		return "", err
	}
	value := Environment(raw)
	switch value {
	case EnvironmentDevelopment, EnvironmentTest, EnvironmentProduction:
		return value, nil
	default:
		return "", fieldError(environmentName, "unsupported deployment environment")
	}
}

func loadPostgreSQL(reader environmentReader) (PostgreSQLConfig, error) {
	dsn, err := reader.required(databaseURLEnvironment)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	if !validServiceURL(dsn, map[string]struct{}{"postgres": {}, "postgresql": {}}, true) {
		return PostgreSQLConfig{}, fieldError(databaseURLEnvironment, "invalid PostgreSQL URL")
	}
	schema := reader.valueOrDefault(databaseSchemaEnvironment, defaultDatabaseSchema)
	if !postgresIdentifierPattern.MatchString(schema) {
		return PostgreSQLConfig{}, fieldError(databaseSchemaEnvironment, "invalid PostgreSQL identifier")
	}
	minConnections, err := parseInt32InRange(reader, databaseMinConnectionsEnvironment, defaultDatabaseMinConnections, 0, maximumDatabaseConnections)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	maxConnections, err := parseInt32InRange(reader, databaseMaxConnectionsEnvironment, defaultDatabaseMaxConnections, 1, maximumDatabaseConnections)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	if minConnections > maxConnections {
		return PostgreSQLConfig{}, fieldError(databaseMinConnectionsEnvironment, "invalid pool relationship")
	}
	maxLifetime, err := parseDurationInRange(reader, databaseMaxConnectionLifetimeEnvironment, defaultDatabaseMaxConnectionLifetime, time.Second, maximumDatabaseMaxConnectionLifetime)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	maxIdleTime, err := parseDurationInRange(reader, databaseMaxConnectionIdleTimeEnvironment, defaultDatabaseMaxConnectionIdleTime, time.Second, maximumDatabaseMaxConnectionIdleTime)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	healthCheckPeriod, err := parseDurationInRange(reader, databaseHealthCheckPeriodEnvironment, defaultDatabaseHealthCheckPeriod, time.Second, maximumDatabaseHealthCheckPeriod)
	if err != nil {
		return PostgreSQLConfig{}, err
	}

	return PostgreSQLConfig{
		DSN:                   dsn,
		Schema:                schema,
		MinConnections:        minConnections,
		MaxConnections:        maxConnections,
		MaxConnectionLifetime: maxLifetime,
		MaxConnectionIdleTime: maxIdleTime,
		HealthCheckPeriod:     healthCheckPeriod,
	}, nil
}

func loadRedis(reader environmentReader) (RedisConfig, error) {
	redisURL, err := reader.required(redisURLEnvironment)
	if err != nil {
		return RedisConfig{}, err
	}
	if !validServiceURL(redisURL, map[string]struct{}{"redis": {}, "rediss": {}}, false) {
		return RedisConfig{}, fieldError(redisURLEnvironment, "invalid Redis URL")
	}
	timeout, err := parseDurationInRange(reader, redisTimeoutEnvironment, defaultRedisTimeout, time.Millisecond, maximumRedisTimeout)
	if err != nil {
		return RedisConfig{}, err
	}
	keyPrefix, err := reader.required(redisKeyPrefixEnvironment)
	if err != nil {
		return RedisConfig{}, err
	}
	if !redisKeyPrefixPattern.MatchString(keyPrefix) {
		return RedisConfig{}, fieldError(redisKeyPrefixEnvironment, "invalid Redis key prefix")
	}
	return RedisConfig{URL: redisURL, Timeout: timeout, KeyPrefix: keyPrefix}, nil
}

func loadNetwork(_ environmentReader, environment Environment) (NetworkConfig, error) {
	// Browser origins and proxy headers are bound to the request/edge topology, so deployments no longer
	// carry per-domain allowlists. Loopback remains the only trusted proxy boundary for the local edge.
	return NetworkConfig{
		TrustedProxies: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"),
			netip.MustParsePrefix("::1/128"),
		},
		CookieSecure: environment == EnvironmentProduction,
	}, nil
}

func loadCheckpoint(reader environmentReader) (CheckpointConfig, error) {
	maxEvents, err := parseIntInRange(reader, checkpointMaxEventsEnvironment, defaultCheckpointMaxEvents, 1, maximumCheckpointMaxEvents)
	if err != nil {
		return CheckpointConfig{}, err
	}
	maxInterval, err := parseDurationInRange(reader, checkpointMaxIntervalEnvironment, defaultCheckpointMaxInterval, time.Second, maximumCheckpointMaxInterval)
	if err != nil {
		return CheckpointConfig{}, err
	}
	return CheckpointConfig{MaxEvents: maxEvents, MaxInterval: maxInterval}, nil
}

func loadKeyringFiles(reader environmentReader) (KeyringFiles, error) {
	pii, err := requiredAbsolutePath(reader, piiKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	totp, err := requiredAbsolutePath(reader, totpKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	resultEnvelope, err := requiredAbsolutePath(reader, resultEnvelopeKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	device, err := requiredAbsolutePath(reader, deviceKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	rateLimit, err := requiredAbsolutePath(reader, rateLimitKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	userChallenge, err := requiredAbsolutePath(reader, userChallengeKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	adminChallenge, err := requiredAbsolutePath(reader, adminChallengeKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	adminSession, err := requiredAbsolutePath(reader, adminSessionKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	adminCursor, err := requiredAbsolutePath(reader, adminCursorKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	audit, err := requiredAbsolutePath(reader, auditKeyringFileEnvironment)
	if err != nil {
		return KeyringFiles{}, err
	}
	keyringPaths := []struct {
		field string
		path  string
	}{
		{field: piiKeyringFileEnvironment, path: pii},
		{field: totpKeyringFileEnvironment, path: totp},
		{field: resultEnvelopeKeyringFileEnvironment, path: resultEnvelope},
		{field: deviceKeyringFileEnvironment, path: device},
		{field: rateLimitKeyringFileEnvironment, path: rateLimit},
		{field: userChallengeKeyringFileEnvironment, path: userChallenge},
		{field: adminChallengeKeyringFileEnvironment, path: adminChallenge},
		{field: adminSessionKeyringFileEnvironment, path: adminSession},
		{field: adminCursorKeyringFileEnvironment, path: adminCursor},
		{field: auditKeyringFileEnvironment, path: audit},
	}
	seenPaths := make(map[string]struct{}, len(keyringPaths))
	for _, keyringPath := range keyringPaths {
		// Reusing a file would collapse cryptographic domains even though callers receive distinct Go types.
		if _, exists := seenPaths[keyringPath.path]; exists {
			return KeyringFiles{}, fieldError(keyringPath.field, "keyring file is already assigned")
		}
		seenPaths[keyringPath.path] = struct{}{}
	}
	return KeyringFiles{
		PII:            PIIKeyringFile(pii),
		TOTP:           TOTPKeyringFile(totp),
		ResultEnvelope: ResultEnvelopeKeyringFile(resultEnvelope),
		Device:         DeviceKeyringFile(device),
		RateLimit:      RateLimitKeyringFile(rateLimit),
		UserChallenge:  UserChallengeKeyringFile(userChallenge),
		AdminChallenge: AdminChallengeKeyringFile(adminChallenge),
		AdminSession:   AdminSessionKeyringFile(adminSession),
		AdminCursor:    AdminCursorKeyringFile(adminCursor),
		Audit:          AuditKeyringFile(audit),
	}, nil
}

func validServiceURL(value string, allowedSchemes map[string]struct{}, requireDatabasePath bool) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" {
		return false
	}
	if _, allowed := allowedSchemes[strings.ToLower(parsed.Scheme)]; !allowed {
		return false
	}
	return !requireDatabasePath || strings.Trim(parsed.Path, "/") != ""
}

func parseInt32InRange(reader environmentReader, name string, fallback, minimum, maximum int32) (int32, error) {
	parsed, err := parseIntInRange(reader, name, int(fallback), int(minimum), int(maximum))
	return int32(parsed), err
}

func parseIntInRange(reader environmentReader, name string, fallback, minimum, maximum int) (int, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fieldError(name, "integer outside allowed range")
	}
	return parsed, nil
}

func parseDurationInRange(reader environmentReader, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fieldError(name, "duration outside allowed range")
	}
	return parsed, nil
}

func requiredAbsolutePath(reader environmentReader, name string) (string, error) {
	value, err := reader.required(name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		return "", fieldError(name, "path must be absolute")
	}
	return filepath.Clean(value), nil
}

func optionalAbsolutePath(reader environmentReader, name string) (string, error) {
	value := reader.optional(name)
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		return "", fieldError(name, "path must be absolute")
	}
	return filepath.Clean(value), nil
}

func fieldError(name, reason string) error {
	return fmt.Errorf("%s: %s", name, reason)
}
