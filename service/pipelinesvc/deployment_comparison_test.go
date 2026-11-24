package pipelinesvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pipelinediff "github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"

	"github.com/stretchr/testify/require"
)

const comparisonCandidate = `{
	"metadata":{"slug":"users","name":"Candidate name"},
	"steps":[{"slug":"source","kind":"query","query":{
		"data_source_ref":"db","operation_type":"sql","operation_ref":"list-users",
		"output_contract":{"name":"users","representation":"rows","fields":[
			{"name":"email_address","type":"string","required":true}
		]}
	}}]
}`

func TestCompareDeploymentPinsObservedRevision(t *testing.T) {
	t.Parallel()

	deployed := pipelinesrepo.PipelineDeployment{
		Revision: 1, CurrentVersionID: 3, CurrentVersionSpecHash: "old-version",
	}
	baseline := strings.NewReplacer("email_address", "email", "Candidate name", "Redacted old name").Replace(comparisonCandidate)
	store := &fakeServicePipelineStore{
		findDeploymentFn: func(_ context.Context, org, project, env, pipeline slug.Slug) (*pipelinesrepo.PipelineDeployment, error) {
			require.Equal(t, []slug.Slug{"acme", "default", "dev", "users"}, []slug.Slug{org, project, env, pipeline})
			snapshot := deployed
			return &snapshot, nil
		},
		findVersionBySpecHashFn: func(_ context.Context, org, project, pipeline slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, []slug.Slug{"acme", "default", "users"}, []slug.Slug{org, project, pipeline})
			deployed = pipelinesrepo.PipelineDeployment{Revision: 2, CurrentVersionID: 4, CurrentVersionSpecHash: "new-version"}
			require.Equal(t, "old-version", hash)
			return &pipelinesrepo.PipelineVersion{ID: 3, SpecHash: hash, SpecJSONB: []byte(baseline)}, nil
		},
	}
	svc := &service{pipelineStore: store}
	got, err := svc.CompareDeployment(t.Context(), "acme", "default", "dev", "users", json.RawMessage(comparisonCandidate))
	require.NoError(t, err)
	require.EqualValues(t, 2, deployed.Revision)
	require.EqualValues(t, 1, got.DeploymentRevision)
	require.Equal(t, "old-version", got.BaselineSpecHash)
	require.Len(t, got.CandidateSpecHash, 64)
	require.Equal(t, pipelinediff.SeverityBreaking, got.Impact.Severity)
	require.Contains(t, got.Impact.Format(), "output_contract.email")
	require.NotContains(t, got.Impact.Format(), "Redacted old name")
	require.NotContains(t, got.Impact.Format(), "Candidate name")
}

func TestCompareDeploymentRejectsInvalidCandidate(t *testing.T) {
	t.Parallel()
	svc := &service{} // Invalid candidates must fail before any storage lookup.
	for _, candidate := range []string{
		`{"secret-value": "do not echo"}`,
		comparisonCandidate + "{}",
		strings.Replace(comparisonCandidate, `"slug":"users"`, `"slug":"different"`, 1),
	} {
		got, err := svc.CompareDeployment(t.Context(), "acme", "default", "dev", "users", json.RawMessage(candidate))
		require.Error(t, err)
		require.NotContains(t, err.Error(), "do not echo")
		require.NotContains(t, err.Error(), "secret-value")
		require.Nil(t, got)
	}
}

func TestCompareDeploymentDoesNotSubstituteMissingVersion(t *testing.T) {
	t.Parallel()
	store := &fakeServicePipelineStore{
		findDeploymentFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDeployment, error) {
			return &pipelinesrepo.PipelineDeployment{CurrentVersionID: 3, CurrentVersionSpecHash: "old-version"}, nil
		},
		findVersionBySpecHashFn: func(_ context.Context, _, _, _ slug.Slug, hash string) (*pipelinesrepo.PipelineVersion, error) {
			require.Equal(t, "old-version", hash)
			return nil, pipelinesrepo.ErrPipelineVersionNotFound
		},
		findVersionBySpecHashPrefixFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesrepo.PipelineVersion, error) {
			t.Error("comparison tried a prefix lookup")
			return nil, ErrPipelineNotFound
		},
	}
	svc := &service{pipelineStore: store}
	got, err := svc.CompareDeployment(t.Context(), "acme", "default", "dev", "users", json.RawMessage(comparisonCandidate))
	require.ErrorIs(t, err, pipelinesrepo.ErrPipelineVersionNotFound)
	require.Nil(t, got)
}
