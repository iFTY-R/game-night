package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OpenPool validates configuration, opens the runtime pool, and verifies connectivity before returning it.
func OpenPool(ctx context.Context, config PoolConfig) (*pgxpool.Pool, error) {
	poolConfig, err := config.Parse()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect PostgreSQL pool: %w", err)
	}
	return pool, nil
}
