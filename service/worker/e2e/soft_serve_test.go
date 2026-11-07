package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/fake"
)

func TestSoftServeConcurrentIsolatedFixtureCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Soft Serve cleanup in short mode")
	}
	require.True(t, isReachable("127.0.0.1", depsports.GitSSHPort()))

	const repositoryCount = 8
	cleanups := make([]func(context.Context) error, 0, repositoryCount)
	deleted := make([]bool, repositoryCount)
	t.Cleanup(func() {
		for i, cleanup := range cleanups {
			if deleted[i] {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			assert.NoError(t, cleanup(ctx))
			cancel()
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	for range repositoryCount {
		_, cleanup, err := fake.BootstrapIsolatedSeededGitFixture(ctx)
		require.NoError(t, err)
		cleanups = append(cleanups, cleanup)
	}

	// Distinct repositories still contend on Soft Serve's metadata database.
	type cleanupResult struct {
		index int
		err   error
	}
	start := make(chan struct{})
	results := make(chan cleanupResult, repositoryCount)
	for i, cleanup := range cleanups {
		go func() {
			<-start
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 20*time.Second)
			defer cleanupCancel()
			results <- cleanupResult{index: i, err: cleanup(cleanupCtx)}
		}()
	}
	close(start)
	for range repositoryCount {
		result := <-results
		deleted[result.index] = result.err == nil
		assert.NoErrorf(t, result.err, "cleanup repository %d", result.index)
	}
}
