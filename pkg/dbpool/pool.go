package dbpool

import (
	"context"

	pgxuuid "github.com/jackc/pgx-gofrs-uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type poolConfigOption func(*pgxpool.Config)

// WithTracing installs the application pgx tracer on the pool config.
func WithTracing() poolConfigOption {
	return func(config *pgxpool.Config) {
		config.ConnConfig.Tracer = tracing.NewPGXTracer()
	}
}

// Open creates a pgx pool from a connection string, registers UUID mappings,
// and verifies connectivity with Ping.
func Open(ctx context.Context, connectionString string) (*pgxpool.Pool, error) {
	return OpenWithOptions(ctx, connectionString, WithTracing())
}

// OpenWithOptions creates a pgx pool with optional configuration hooks.
func OpenWithOptions(
	ctx context.Context,
	connectionString string,
	options ...poolConfigOption,
) (*pgxpool.Pool, error) {
	dbConfig, err := newPoolConfig(connectionString, options...)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, dbConfig)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, xerror.Unexpected().With(err)
	}

	return pool, nil
}

func newPoolConfig(
	connectionString string,
	options ...poolConfigOption,
) (*pgxpool.Config, error) {
	dbConfig, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	dbConfig.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}

	for _, option := range options {
		option(dbConfig)
	}

	return dbConfig, nil
}
