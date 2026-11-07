package postgres

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionLocker holds an advisory lock without keeping a transaction open during external work.
type SessionLocker interface {
	WithLock(context.Context, string, func(context.Context) error) (bool, error)
}

type sessionLocker struct{ pool *pgxpool.Pool }

func NewSessionLocker(pool *pgxpool.Pool) SessionLocker {
	if pool == nil {
		panic("postgres: session lock pool is required")
	}
	return &sessionLocker{pool: pool}
}

func (s *sessionLocker) WithLock(ctx context.Context, key string, execute func(context.Context) error) (acquired bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, xerror.Wrapf("acquire session lock connection: %w", xerror.Unexpected().With(err))
	}
	defer conn.Release()
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// The server may have acquired the lock even when its reply was lost.
		closeErr := conn.Hijack().Close(cleanup)
		return false, xerror.Join(xerror.Wrapf("acquire session advisory lock: %w", xerror.Unexpected().With(err)), closeErr)
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked)
		if unlockErr != nil || !unlocked {
			// Never return a connection with a possibly held lock to the pool, including after panic or cancellation.
			closeErr := conn.Hijack().Close(cleanup)
			if unlockErr == nil {
				unlockErr = xerror.Unexpected().Detail("session advisory lock was not held")
			}
			err = xerror.Join(err, xerror.Wrapf("release session advisory lock: %w", xerror.Unexpected().With(unlockErr)), closeErr)
		}
	}()
	return true, execute(ctx)
}
