package pipelineruntime

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/stretchr/testify/require"
)

func TestRunWriterNotifiesOnlyPersistedSuccess(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "canceled", "running", "queued", "partial"} {
		t.Run(status, func(t *testing.T) {
			persisted, notified := false, false
			storage := &fakeRunStorageService{createRunFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *createRunParams) error {
				persisted = true
				return nil
			}}
			writer := &runWriter{
				txm: &fakeTransactionManager{}, clock: clock.New(), runStorageSvc: storage,
				notifier: testutil.FakeRunSucceededNotifier{NotifyFn: func(_ context.Context, org, project, environment slug.Slug, run string) error {
					require.True(t, persisted)
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("ops"), project)
					require.Equal(t, slug.Slug("prod"), environment)
					require.Equal(t, "run-1", run)
					notified = true
					return nil
				}},
			}
			pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders"}}
			result := &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{RunSlug: "run-1", Status: status}, Timeline: pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{{At: time.Now()}}}}
			require.NoError(t, writer.PersistRun(t.Context(), "acme", "ops", "prod", pipeline, result))
			require.Equal(t, status == "succeeded", notified)

			storage.createRunFn = func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, *createRunParams) error {
				return xerror.Unexpected().Detail("persistence failed")
			}
			notified = false
			require.Error(t, writer.PersistRun(t.Context(), "acme", "ops", "prod", pipeline, result))
			require.False(t, notified)
		})
	}
}
