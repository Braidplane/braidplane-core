package gitprovider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestVerifiedChangeRejectsNoncanonicalBranchRefs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		source string
		target string
	}{
		{name: "empty source", source: "refs/heads/", target: "refs/heads/main"},
		{name: "unnormalized source", source: "refs/heads/ feature", target: "refs/heads/main"},
		{name: "nested source prefix", source: "refs/heads/refs/heads/feature", target: "refs/heads/main"},
		{name: "empty target", source: "refs/heads/feature", target: "refs/heads/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			change := VerifiedChange{
				Provider: repostore.ProviderGitHub, ProviderConnectionID: "1", RepositoryID: "2",
				ChangeRequestID: "3", HeadSHA: "a123456789012345678901234567890123456789",
				SourceRef: tc.source, TargetRef: tc.target, PipelinePath: "pipelines/acme.pipeline.yaml",
			}
			require.Error(t, change.Validate())
		})
	}
}
