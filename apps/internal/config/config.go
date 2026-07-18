package config

import (
	"errors"
	"fmt"
	"math/big"
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
	userOriginsEnvironment                   = "GAME_NIGHT_USER_ORIGINS"
	adminOriginsEnvironment                  = "GAME_NIGHT_ADMIN_ORIGINS"
	trustedProxyCIDRsEnvironment             = "GAME_NIGHT_TRUSTED_PROXY_CIDRS"
	cookieSecureEnvironment                  = "GAME_NIGHT_COOKIE_SECURE"
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
	postgres, err := loadPostgreSQL(reader, environment)
	if err != nil {
		return Config{}, err
	}
	redisConfig, err := loadRedis(reader, environment)
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

func loadPostgreSQL(reader environmentReader, environment Environment) (PostgreSQLConfig, error) {
	dsn, err := reader.required(databaseURLEnvironment)
	if err != nil {
		return PostgreSQLConfig{}, err
	}
	if !validServiceURL(dsn, map[string]struct{}{"postgres": {}, "postgresql": {}}, true) {
		return PostgreSQLConfig{}, fieldError(databaseURLEnvironment, "invalid PostgreSQL URL")
	}
	if environment == EnvironmentProduction && !validProductionPostgreSQLTLS(dsn) {
		return PostgreSQLConfig{}, fieldError(databaseURLEnvironment, "production PostgreSQL requires TLS")
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

func loadRedis(reader environmentReader, environment Environment) (RedisConfig, error) {
	redisURL, err := reader.required(redisURLEnvironment)
	if err != nil {
		return RedisConfig{}, err
	}
	if !validServiceURL(redisURL, map[string]struct{}{"redis": {}, "rediss": {}}, false) {
		return RedisConfig{}, fieldError(redisURLEnvironment, "invalid Redis URL")
	}
	if environment == EnvironmentProduction && !strings.HasPrefix(strings.ToLower(redisURL), "rediss://") {
		return RedisConfig{}, fieldError(redisURLEnvironment, "production Redis requires TLS")
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

func loadNetwork(reader environmentReader, environment Environment) (NetworkConfig, error) {
	userOrigins, err := parseOrigins(reader, userOriginsEnvironment, environment == EnvironmentProduction)
	if err != nil {
		return NetworkConfig{}, err
	}
	adminOrigins, err := parseOrigins(reader, adminOriginsEnvironment, environment == EnvironmentProduction)
	if err != nil {
		return NetworkConfig{}, err
	}
	if originAllowlistsOverlap(userOrigins, adminOrigins) {
		return NetworkConfig{}, fieldError(adminOriginsEnvironment, "user and admin origins must be isolated")
	}
	trustedProxies, err := parseTrustedProxies(reader)
	if err != nil {
		return NetworkConfig{}, err
	}
	cookieSecure, err := parseBool(reader, cookieSecureEnvironment, environment == EnvironmentProduction)
	if err != nil {
		return NetworkConfig{}, err
	}
	// Production cookies carry long-lived authentication material and may never traverse plaintext HTTP.
	if environment == EnvironmentProduction && !cookieSecure {
		return NetworkConfig{}, fieldError(cookieSecureEnvironment, "must be enabled in production")
	}
	return NetworkConfig{
		UserOrigins:    userOrigins,
		AdminOrigins:   adminOrigins,
		TrustedProxies: trustedProxies,
		CookieSecure:   cookieSecure,
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
		Audit:          AuditKeyringFile(audit),
	}, nil
}

func parseOrigins(reader environmentReader, name string, requireHTTPS bool) (OriginAllowlist, error) {
	value, err := reader.required(name)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(value, ",")
	origins := make(OriginAllowlist, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		parsed, parseErr := url.Parse(strings.TrimSpace(part))
		if parseErr != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, fieldError(name, "invalid origin allowlist")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fieldError(name, "invalid origin allowlist")
		}
		if requireHTTPS && parsed.Scheme != "https" {
			return nil, fieldError(name, "production origins require HTTPS")
		}
		canonical := parsed.Scheme + "://" + strings.ToLower(parsed.Host)
		if _, exists := seen[canonical]; exists {
			return nil, fieldError(name, "duplicate origin")
		}
		seen[canonical] = struct{}{}
		origins = append(origins, Origin(canonical))
	}
	if len(origins) == 0 {
		return nil, fieldError(name, "empty origin allowlist")
	}
	return origins, nil
}

func parseTrustedProxies(reader environmentReader) ([]netip.Prefix, error) {
	value, err := reader.required(trustedProxyCIDRsEnvironment)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(value, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	covered := map[int]*big.Int{32: new(big.Int), 128: new(big.Int)}
	for _, part := range parts {
		prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(part))
		if parseErr != nil {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "invalid CIDR allowlist")
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4In6() {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "mapped IPv4 CIDR is forbidden")
		}
		// A zero-length prefix would trust direct internet clients and defeat right-to-left proxy peeling.
		if prefix.Bits() == 0 {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "unbounded CIDR is forbidden")
		}
		if _, exists := seen[prefix]; exists {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "duplicate CIDR")
		}
		for existing := range seen {
			if existing.Addr().BitLen() == prefix.Addr().BitLen() &&
				(existing.Contains(prefix.Addr()) || prefix.Contains(existing.Addr())) {
				return nil, fieldError(trustedProxyCIDRsEnvironment, "overlapping CIDR")
			}
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)

		addressBits := prefix.Addr().BitLen()
		prefixSize := new(big.Int).Lsh(big.NewInt(1), uint(addressBits-prefix.Bits()))
		covered[addressBits].Add(covered[addressBits], prefixSize)
		addressSpaceSize := new(big.Int).Lsh(big.NewInt(1), uint(addressBits))
		if covered[addressBits].Cmp(addressSpaceSize) >= 0 {
			return nil, fieldError(trustedProxyCIDRsEnvironment, "unbounded CIDR set is forbidden")
		}
	}
	if len(prefixes) == 0 {
		return nil, fieldError(trustedProxyCIDRsEnvironment, "empty CIDR allowlist")
	}
	return prefixes, nil
}

func originAllowlistsOverlap(first, second OriginAllowlist) bool {
	seen := make(map[Origin]struct{}, len(first))
	for _, origin := range first {
		seen[origin] = struct{}{}
	}
	for _, origin := range second {
		if _, exists := seen[origin]; exists {
			return true
		}
	}
	return false
}

func validProductionPostgreSQLTLS(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	modes := parsed.Query()["sslmode"]
	if len(modes) != 1 {
		return false
	}
	switch strings.ToLower(modes[0]) {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
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

func parseBool(reader environmentReader, name string, fallback bool) (bool, error) {
	value := reader.optional(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fieldError(name, "invalid boolean")
	}
	return parsed, nil
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
