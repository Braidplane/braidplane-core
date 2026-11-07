package consumer

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

type publicationProcessor func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (bool, error)

func (f publicationProcessor) Refresh(ctx context.Context, o, p, e, r slug.Slug) (bool, error) {
	return f(ctx, o, p, e, r)
}

func TestProofPublicationRetriesWithoutExposingProviderErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		again bool
		err   error
	}{
		{"Fresh", true, nil},
		{"Finished", false, nil},
		{"ProviderFailure", true, xerror.Unexpected().Detail("private-provider-response")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			calls := 0
			worker, err := NewProofPublicationWorker(publicationProcessor(func(ctx context.Context, o, p, e, r slug.Slug) (bool, error) {
				calls++
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
				require.Equal(t, []slug.Slug{"acme", "data", "staging", "proof-1"}, []slug.Slug{o, p, e, r})
				return tc.again, tc.err
			}), slog.New(slog.NewTextHandler(&logs, nil)))
			require.NoError(t, err)
			err = worker.Work(t.Context(), &river.Job[jobs.ProofPublicationArgs]{Args: jobs.ProofPublicationArgs{Organization: "acme", Project: "data", Environment: "staging", Receipt: "proof-1"}})
			if tc.again {
				var snooze *river.JobSnoozeError
				require.ErrorAs(t, err, &snooze)
				require.NotContains(t, err.Error(), "private-provider-response")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
			require.NotContains(t, logs.String(), "private-provider-response")
		})
	}
}
