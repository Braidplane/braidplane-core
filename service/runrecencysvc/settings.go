package runrecencysvc

import (
	"context"
	"math"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
)

var ErrRevisionConflict = xerror.New(xerror.KindAlreadyExists, "RUN_RECENCY_EXPECTATION_CONFLICT", "recency expectation revision changed; reload before updating")

// Update appends an immutable revision; callers must enforce their normal authorization before this method.
func (s *Service) Update(ctx context.Context, scope runrecency.Scope, expectedRevision int64, maximumAge time.Duration, enabled bool, now time.Time) (*runrecency.Expectation, error) {
	for _, value := range []slug.Slug{scope.Org, scope.Project, scope.Environment, scope.Pipeline} {
		if _, err := slug.New(value.String()); err != nil {
			return nil, err
		}
	}
	if expectedRevision < 0 || expectedRevision == math.MaxInt64 || maximumAge <= 0 || maximumAge%time.Second != 0 || now.IsZero() {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("recency requires a nonnegative revision, positive whole-second maximum age and enablement time")
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*runrecency.Expectation, error) {
		if err := s.repo.LockScope(ctx, tx, scope); err != nil {
			return nil, err
		}
		previous, err := s.repo.FindLatest(ctx, tx, scope)
		if err != nil && !xerror.Is(err, runrecency.ErrExpectationNotFound) {
			return nil, err
		}
		revision := int64(0)
		if previous != nil {
			revision = previous.Revision
		}
		if revision != expectedRevision {
			return nil, ErrRevisionConflict
		}
		if previous != nil && previous.MaximumAge == maximumAge && previous.Enabled == enabled {
			return previous, nil
		}
		return s.repo.CreateExpectation(ctx, tx, runrecency.Expectation{Scope: scope, Revision: revision + 1, MaximumAge: maximumAge, Enabled: enabled, EnabledAt: now.UTC()})
	})
}
