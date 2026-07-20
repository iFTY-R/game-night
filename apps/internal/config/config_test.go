package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadRealtimeDoesNotRequireSecurityKeyrings(t *testing.T) {
	environment := validEnvironment(t)
	for _, name := range []string{
		piiKeyringFileEnvironment, totpKeyringFileEnvironment, resultEnvelopeKeyringFileEnvironment,
		deviceKeyringFileEnvironment, rateLimitKeyringFileEnvironment, userChallengeKeyringFileEnvironment,
		adminChallengeKeyringFileEnvironment, adminSessionKeyringFileEnvironment, auditKeyringFileEnvironment,
		bootstrapSecretFileEnvironment,
	} {
		delete(environment, name)
	}
	loaded, err := LoadRealtime(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PostgreSQL.DSN == "" || loaded.Redis.URL == "" || len(loaded.Network.UserOrigins) == 0 {
		t.Fatalf("realtime dependencies = %+v", loaded)
	}
}

func TestLoadBuildsValidatedSharedConfig(t *testing.T) {
	environment := validEnvironment(t)
	environment[environmentName] = string(EnvironmentProduction)
	environment[databaseSchemaEnvironment] = "game_night"
	environment[databaseMinConnectionsEnvironment] = "2"
	environment[databaseMaxConnectionsEnvironment] = "20"
	environment[databaseMaxConnectionLifetimeEnvironment] = "45m"
	environment[databaseMaxConnectionIdleTimeEnvironment] = "10m"
	environment[databaseHealthCheckPeriodEnvironment] = "30s"
	environment[redisTimeoutEnvironment] = "750ms"
	environment[userOriginsEnvironment] = "https://play.example.test, https://friends.example.test"
	environment[adminOriginsEnvironment] = "https://admin.example.test"
	environment[trustedProxyCIDRsEnvironment] = "10.0.0.0/8, 2001:db8::/32"
	environment[cookieSecureEnvironment] = "true"
	environment[checkpointMaxEventsEnvironment] = "80"
	environment[checkpointMaxIntervalEnvironment] = "4m"
	environment[bootstrapSecretFileEnvironment] = filepath.Join(t.TempDir(), "bootstrap-password")

	loaded, err := Load(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Environment != EnvironmentProduction {
		t.Fatalf("unexpected environment: %s", loaded.Environment)
	}
	if loaded.PostgreSQL.Schema != "game_night" || loaded.PostgreSQL.MinConnections != 2 || loaded.PostgreSQL.MaxConnections != 20 {
		t.Fatalf("unexpected PostgreSQL config: %+v", loaded.PostgreSQL)
	}
	if loaded.PostgreSQL.MaxConnectionLifetime != 45*time.Minute || loaded.PostgreSQL.MaxConnectionIdleTime != 10*time.Minute || loaded.PostgreSQL.HealthCheckPeriod != 30*time.Second {
		t.Fatalf("unexpected PostgreSQL pool durations: %+v", loaded.PostgreSQL)
	}
	if loaded.Redis.Timeout != 750*time.Millisecond || loaded.Redis.KeyPrefix != "game-night:test:" {
		t.Fatalf("unexpected Redis config: %+v", loaded.Redis)
	}
	if got := []Origin(loaded.Network.UserOrigins); !reflect.DeepEqual(got, []Origin{"https://play.example.test", "https://friends.example.test"}) {
		t.Fatalf("unexpected user origins: %v", got)
	}
	if len(loaded.Network.TrustedProxies) != 2 || loaded.Network.TrustedProxies[0].String() != "10.0.0.0/8" || !loaded.Network.CookieSecure {
		t.Fatalf("unexpected network config: %+v", loaded.Network)
	}
	if loaded.Checkpoint.MaxEvents != 80 || loaded.Checkpoint.MaxInterval != 4*time.Minute {
		t.Fatalf("unexpected checkpoint config: %+v", loaded.Checkpoint)
	}
	if string(loaded.BootstrapSecretFile) != environment[bootstrapSecretFileEnvironment] {
		t.Fatalf("unexpected bootstrap secret file: %s", loaded.BootstrapSecretFile)
	}
	if string(loaded.Keyrings.PII) != environment[piiKeyringFileEnvironment] || string(loaded.Keyrings.Audit) != environment[auditKeyringFileEnvironment] {
		t.Fatalf("unexpected keyring files: %+v", loaded.Keyrings)
	}
}

func TestLoadUsesSafeNonSecretDefaults(t *testing.T) {
	loaded, err := Load(mapLookup(validEnvironment(t)))
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Environment != EnvironmentDevelopment || loaded.PostgreSQL.Schema != "public" {
		t.Fatalf("unexpected general defaults: %+v", loaded)
	}
	if loaded.PostgreSQL.MinConnections != 1 || loaded.PostgreSQL.MaxConnections != 10 {
		t.Fatalf("unexpected pool defaults: %+v", loaded.PostgreSQL)
	}
	if loaded.PostgreSQL.MaxConnectionLifetime != time.Hour || loaded.PostgreSQL.MaxConnectionIdleTime != 30*time.Minute || loaded.PostgreSQL.HealthCheckPeriod != time.Minute {
		t.Fatalf("unexpected pool duration defaults: %+v", loaded.PostgreSQL)
	}
	if loaded.Redis.Timeout != time.Second || loaded.Checkpoint.MaxEvents != 100 || loaded.Checkpoint.MaxInterval != 5*time.Minute {
		t.Fatalf("unexpected dependency defaults: redis=%+v checkpoint=%+v", loaded.Redis, loaded.Checkpoint)
	}
	if loaded.Network.CookieSecure || loaded.BootstrapSecretFile != "" {
		t.Fatalf("unexpected optional defaults: network=%+v bootstrap=%q", loaded.Network, loaded.BootstrapSecretFile)
	}
}

func TestLoadRequiresExplicitEnvironment(t *testing.T) {
	environment := validEnvironment(t)
	delete(environment, environmentName)

	_, err := Load(mapLookup(environment))
	assertSafeError(t, err, environmentName, "")
}

func TestLoadRequiresSecureCookiesInProduction(t *testing.T) {
	environment := validEnvironment(t)
	environment[environmentName] = string(EnvironmentProduction)
	environment[userOriginsEnvironment] = "https://play.example.test"
	environment[adminOriginsEnvironment] = "https://admin.example.test"
	environment[cookieSecureEnvironment] = "false"

	_, err := Load(mapLookup(environment))
	assertSafeError(t, err, cookieSecureEnvironment, "false")
}

func TestLoadDefaultsToSecureCookiesInProduction(t *testing.T) {
	environment := validEnvironment(t)
	environment[environmentName] = string(EnvironmentProduction)
	environment[userOriginsEnvironment] = "https://play.example.test"
	environment[adminOriginsEnvironment] = "https://admin.example.test"

	loaded, err := Load(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Network.CookieSecure {
		t.Fatal("production cookies must default to Secure")
	}
}

func TestLoadRequiresEncryptedDependenciesInProduction(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "PostgreSQL missing sslmode", field: databaseURLEnvironment, value: "postgres://runtime:secret@db.example.test/game_night"},
		{name: "PostgreSQL disabled TLS", field: databaseURLEnvironment, value: "postgres://runtime:secret@db.example.test/game_night?sslmode=disable"},
		{name: "PostgreSQL ambiguous TLS", field: databaseURLEnvironment, value: "postgres://runtime:secret@db.example.test/game_night?sslmode=require&sslmode=disable"},
		{name: "Redis plaintext", field: redisURLEnvironment, value: "redis://:secret@redis.example.test/0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := productionEnvironment(t)
			environment[test.field] = test.value
			_, err := Load(mapLookup(environment))
			assertSafeError(t, err, test.field, test.value)
		})
	}
}

func TestLoadAllowsExplicitPlaintextDependenciesOutsideProduction(t *testing.T) {
	environment := validEnvironment(t)
	environment[databaseURLEnvironment] = "postgres://runtime:secret@localhost/game_night?sslmode=disable"
	environment[redisURLEnvironment] = "redis://:secret@localhost/0"

	if _, err := Load(mapLookup(environment)); err != nil {
		t.Fatalf("development plaintext dependencies should remain explicit and valid: %v", err)
	}
}

func TestLoadRejectsOriginOverlapAcrossUserAndAdminSurfaces(t *testing.T) {
	environment := validEnvironment(t)
	environment[adminOriginsEnvironment] = environment[userOriginsEnvironment]

	_, err := Load(mapLookup(environment))
	assertSafeError(t, err, adminOriginsEnvironment, environment[adminOriginsEnvironment])
}

func TestLoadRejectsCheckpointThresholdsAboveFailClosedBounds(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "events", environment: checkpointMaxEventsEnvironment, value: "101"},
		{name: "interval", environment: checkpointMaxIntervalEnvironment, value: "5m1s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment(t)
			environment[test.environment] = test.value
			_, err := Load(mapLookup(environment))
			assertSafeError(t, err, test.environment, test.value)
		})
	}
}

func TestLoadRejectsTrustingEveryProxyAddress(t *testing.T) {
	for _, cidr := range []string{
		"0.0.0.0/0",
		"::/0",
		"0.0.0.0/1,128.0.0.0/1",
		"::/1,8000::/1",
		"10.0.0.0/8,10.0.0.0/9",
	} {
		t.Run(cidr, func(t *testing.T) {
			environment := validEnvironment(t)
			environment[trustedProxyCIDRsEnvironment] = cidr

			_, err := Load(mapLookup(environment))
			assertSafeError(t, err, trustedProxyCIDRsEnvironment, cidr)
		})
	}
}

func TestLoadRejectsInvalidConfigurationWithoutLeakingValues(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		value       string
	}{
		{name: "PostgreSQL URL", environment: databaseURLEnvironment, value: "postgres://top-secret@"},
		{name: "PostgreSQL schema", environment: databaseSchemaEnvironment, value: "private;drop-secret"},
		{name: "PostgreSQL minimum pool", environment: databaseMinConnectionsEnvironment, value: "secret-min"},
		{name: "PostgreSQL maximum pool", environment: databaseMaxConnectionsEnvironment, value: "secret-max"},
		{name: "PostgreSQL pool order", environment: databaseMinConnectionsEnvironment, value: "11"},
		{name: "PostgreSQL pool lifetime", environment: databaseMaxConnectionLifetimeEnvironment, value: "secret-lifetime"},
		{name: "Redis URL", environment: redisURLEnvironment, value: "redis://top-secret@"},
		{name: "Redis timeout", environment: redisTimeoutEnvironment, value: "secret-timeout"},
		{name: "Redis key prefix", environment: redisKeyPrefixEnvironment, value: "secret prefix"},
		{name: "user origins", environment: userOriginsEnvironment, value: "https://user-secret.example.test/path"},
		{name: "admin origins", environment: adminOriginsEnvironment, value: "admin-secret.example.test"},
		{name: "trusted proxies", environment: trustedProxyCIDRsEnvironment, value: "proxy-secret"},
		{name: "Cookie secure", environment: cookieSecureEnvironment, value: "secret-bool"},
		{name: "bootstrap secret file", environment: bootstrapSecretFileEnvironment, value: "relative-secret-file"},
		{name: "PII keyring file", environment: piiKeyringFileEnvironment, value: "relative-pii-secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := validEnvironment(t)
			environment[test.environment] = test.value
			_, err := Load(mapLookup(environment))
			assertSafeError(t, err, test.environment, test.value)
		})
	}
}

func TestLoadReportsMissingSecretEnvironmentByNameOnly(t *testing.T) {
	environment := validEnvironment(t)
	delete(environment, resultEnvelopeKeyringFileEnvironment)

	_, err := Load(mapLookup(environment))
	assertSafeError(t, err, resultEnvelopeKeyringFileEnvironment, "")
}

func TestLoadRejectsKeyringFileReuseAcrossSecurityDomains(t *testing.T) {
	environment := validEnvironment(t)
	environment[adminChallengeKeyringFileEnvironment] = environment[userChallengeKeyringFileEnvironment]

	_, err := Load(mapLookup(environment))
	assertSafeError(t, err, adminChallengeKeyringFileEnvironment, environment[adminChallengeKeyringFileEnvironment])
}

func TestKeyringFilesUseDistinctNamedTypes(t *testing.T) {
	files := KeyringFiles{
		PII:            PIIKeyringFile("pii"),
		TOTP:           TOTPKeyringFile("totp"),
		ResultEnvelope: ResultEnvelopeKeyringFile("result"),
		Device:         DeviceKeyringFile("device"),
		RateLimit:      RateLimitKeyringFile("rate-limit"),
		UserChallenge:  UserChallengeKeyringFile("user-challenge"),
		AdminChallenge: AdminChallengeKeyringFile("admin-challenge"),
		AdminSession:   AdminSessionKeyringFile("admin-session"),
		Audit:          AuditKeyringFile("audit"),
	}

	types := []reflect.Type{
		reflect.TypeOf(files.PII),
		reflect.TypeOf(files.TOTP),
		reflect.TypeOf(files.ResultEnvelope),
		reflect.TypeOf(files.Device),
		reflect.TypeOf(files.RateLimit),
		reflect.TypeOf(files.UserChallenge),
		reflect.TypeOf(files.AdminChallenge),
		reflect.TypeOf(files.AdminSession),
		reflect.TypeOf(files.Audit),
	}
	seen := make(map[reflect.Type]struct{}, len(types))
	for _, keyType := range types {
		if _, exists := seen[keyType]; exists {
			t.Fatalf("keyring file types must be distinct: %v", keyType)
		}
		seen[keyType] = struct{}{}
	}
	paths := files.SecurityPaths()
	if paths.PII != "pii" || paths.TOTP != "totp" || paths.ResultEnvelope != "result" ||
		paths.Device != "device" || paths.RateLimit != "rate-limit" ||
		paths.UserChallenge != "user-challenge" || paths.AdminChallenge != "admin-challenge" || paths.AdminSession != "admin-session" || paths.Audit != "audit" {
		t.Fatalf("security path mapping crossed keyring purposes: %+v", paths)
	}
}

func validEnvironment(t *testing.T) map[string]string {
	t.Helper()
	secretDirectory := t.TempDir()
	return map[string]string{
		environmentName:                      string(EnvironmentDevelopment),
		databaseURLEnvironment:               "postgres://runtime:database-secret@db.example.test/game_night?sslmode=require",
		redisURLEnvironment:                  "rediss://:redis-secret@redis.example.test/0",
		redisKeyPrefixEnvironment:            "game-night:test:",
		userOriginsEnvironment:               "http://localhost:3000",
		adminOriginsEnvironment:              "http://localhost:3001",
		trustedProxyCIDRsEnvironment:         "127.0.0.1/32,::1/128",
		piiKeyringFileEnvironment:            filepath.Join(secretDirectory, "pii.json"),
		totpKeyringFileEnvironment:           filepath.Join(secretDirectory, "totp.json"),
		resultEnvelopeKeyringFileEnvironment: filepath.Join(secretDirectory, "result-envelope.json"),
		deviceKeyringFileEnvironment:         filepath.Join(secretDirectory, "device.json"),
		rateLimitKeyringFileEnvironment:      filepath.Join(secretDirectory, "rate-limit.json"),
		userChallengeKeyringFileEnvironment:  filepath.Join(secretDirectory, "user-challenge.json"),
		adminChallengeKeyringFileEnvironment: filepath.Join(secretDirectory, "admin-challenge.json"),
		adminSessionKeyringFileEnvironment:   filepath.Join(secretDirectory, "admin-session.json"),
		auditKeyringFileEnvironment:          filepath.Join(secretDirectory, "audit.json"),
	}
}

func productionEnvironment(t *testing.T) map[string]string {
	t.Helper()
	environment := validEnvironment(t)
	environment[environmentName] = string(EnvironmentProduction)
	environment[userOriginsEnvironment] = "https://play.example.test"
	environment[adminOriginsEnvironment] = "https://admin.example.test"
	return environment
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, exists := values[name]
		return value, exists
	}
}

func assertSafeError(t *testing.T, err error, field, sensitiveValue string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error for %s", field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("error does not identify %s: %v", field, err)
	}
	if sensitiveValue != "" && strings.Contains(err.Error(), sensitiveValue) {
		t.Fatalf("error leaked configured value for %s: %v", field, err)
	}
}
