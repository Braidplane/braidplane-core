package run

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestNormalizeAttemptSourceRootRunSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		source              RunSourceSummary
		runSlug             slug.Slug
		expectedRoot        slug.Slug
		expectedRetryOfSlug *slug.Slug
	}{
		{
			name:         "InitialRunDefaultsRoot",
			source:       RunSourceSummary{},
			runSlug:      slug.Slug("run-001"),
			expectedRoot: slug.Slug("run-001"),
		},
		{
			name: "RetryRunKeepsMissingRootEmpty",
			source: RunSourceSummary{
				AttemptNumber: 2,
			},
			runSlug: slug.Slug("run-001-retry-2"),
		},
		{
			name: "RetryRunKeepsProvidedRoot",
			source: RunSourceSummary{
				AttemptNumber: 2,
				RootRunSlug:   slug.Slug("run-001"),
			},
			runSlug:             slug.Slug("run-001-retry-2"),
			expectedRoot:        slug.Slug("run-001"),
			expectedRetryOfSlug: ptrSlug(slug.Slug("run-001")),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := NormalizeAttemptSource(test.source, test.runSlug)

			require.Equal(t, test.expectedRoot, got.RootRunSlug)
			require.Equal(t, test.expectedRetryOfSlug, got.RetryOfRunSlug)
		})
	}
}

func ptrSlug(value slug.Slug) *slug.Slug {
	return &value
}
