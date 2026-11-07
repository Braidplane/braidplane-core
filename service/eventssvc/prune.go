package eventssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

const DefaultRetentionPeriod = 180 * 24 * time.Hour

func (s *service) PruneExpiredEvents(
	ctx context.Context,
	retention time.Duration,
) (int64, error) {
	if retention <= 0 {
		return 0, ErrInvalidInput.Detail("retention must be positive")
	}

	cutoff := s.clk.Now().UTC().Add(-retention)

	var deletedCount int64
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		deletedCount, err = s.repo.PruneOlderThan(ctx, tx, cutoff)
		return err
	})
	if err != nil {
		return 0, xerror.Wrapf("prune events: %w", err)
	}

	return deletedCount, nil
}
