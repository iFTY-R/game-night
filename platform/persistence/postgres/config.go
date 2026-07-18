// Package postgres owns PostgreSQL connection management, generated queries, and transaction binding.
package postgres

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaIdentifierPattern limits configuration to PostgreSQL's unquoted identifier character set.
var schemaIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// PoolConfig defines validated runtime pool limits without deriving secrets or deployment defaults.
// Zero-valued tuning fields preserve pgxpool defaults; negative values are rejected.
type PoolConfig struct {
	DatabaseURL       string
	Schema            string
	MinConnections    int32
	MaxConnections    int32
	MaxConnectionAge  time.Duration
	MaxConnectionIdle time.Duration
	HealthCheckPeriod time.Duration
}

// Parse builds a pgxpool configuration with a fixed trusted schema and UTC session timezone.
// Errors identify the invalid field but never include the database URL.
func (config PoolConfig) Parse() (*pgxpool.Config, error) {
	databaseURL := strings.TrimSpace(config.DatabaseURL)
	if databaseURL == "" {
		return nil, errors.New("PostgreSQL database URL is required")
	}
	schema := strings.TrimSpace(config.Schema)
	if !schemaIdentifierPattern.MatchString(schema) {
		return nil, errors.New("PostgreSQL schema must be an unquoted identifier")
	}
	if config.MinConnections < 0 || config.MaxConnections < 0 {
		return nil, errors.New("PostgreSQL connections cannot be negative")
	}
	if config.MaxConnectionAge < 0 || config.MaxConnectionIdle < 0 || config.HealthCheckPeriod < 0 {
		return nil, errors.New("PostgreSQL pool duration cannot be negative")
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL database URL")
	}
	if config.MinConnections > 0 {
		poolConfig.MinConns = config.MinConnections
	}
	if config.MaxConnections > 0 {
		poolConfig.MaxConns = config.MaxConnections
	}
	if poolConfig.MinConns > poolConfig.MaxConns {
		return nil, fmt.Errorf("PostgreSQL minimum connections cannot exceed maximum connections")
	}
	if config.MaxConnectionAge > 0 {
		poolConfig.MaxConnLifetime = config.MaxConnectionAge
	}
	if config.MaxConnectionIdle > 0 {
		poolConfig.MaxConnIdleTime = config.MaxConnectionIdle
	}
	if config.HealthCheckPeriod > 0 {
		poolConfig.HealthCheckPeriod = config.HealthCheckPeriod
	}

	// Every connection uses the same trusted object namespace; pg_catalog remains an explicit fallback only.
	poolConfig.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize() + ",pg_catalog"
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	return poolConfig, nil
}
