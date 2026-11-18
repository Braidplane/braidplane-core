package dbpool

import (
	"context"
	"strings"

	pgxuuid "github.com/jackc/pgx-gofrs-uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RestrictAuthentication rejects MD5 while preserving the parsed operator policy.
func RestrictAuthentication(config *pgconn.Config) error {
	const denyMD5 = "!md5"
	if config.RequireAuth == "" {
		config.RequireAuth = denyMD5
		return nil
	}
	methods := strings.Split(config.RequireAuth, ",")
	if strings.HasPrefix(strings.TrimSpace(methods[0]), "!") {
		for _, method := range methods {
			if strings.TrimSpace(method) == denyMD5 {
				return nil
			}
		}
		config.RequireAuth += "," + denyMD5
		return nil
	}
	allowed := methods[:0]
	for _, method := range methods {
		if strings.TrimSpace(method) != "md5" {
			allowed = append(allowed, method)
		}
	}
	if len(allowed) == 0 {
		return xerror.ValidationFailed().Detail("PostgreSQL authentication policy allows only MD5")
	}
	config.RequireAuth = strings.Join(allowed, ",")
	return nil
}

// New opens a lazy pool with the application authentication policy.
func New(ctx context.Context, connectionString string) (*pgxpool.Pool, error) {
	config, err := parsePoolConfig(connectionString)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return pool, nil
}

func parsePoolConfig(connectionString string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if err := RestrictAuthentication(&config.ConnConfig.Config); err != nil {
		return nil, err
	}
	return config, nil
}

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
	dbConfig, err := parsePoolConfig(connectionString)
	if err != nil {
		return nil, err
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
