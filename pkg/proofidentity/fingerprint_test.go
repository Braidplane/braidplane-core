package proofidentity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestCapabilityFingerprintPreservesReceiptFormat(t *testing.T) {
	t.Parallel()
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{
		Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "list"},
	}}}
	resolutions := []model.ResolvedPipelineDataSourceRef{{RefSlug: "source", DataSourceType: model.DataSourceTypePostgres}}

	got, err := CapabilityFingerprint(pipeline, resolutions)

	require.NoError(t, err)
	require.Equal(t, "sha256:4725851740f9de8f34d14366ac96f618802393a8991024bc27da4b4c8c58a31b", got)
}

func TestBindingRevisionDoesNotRewriteHistoricalProofIdentity(t *testing.T) {
	t.Parallel()
	original := []model.ResolvedPipelineDataSourceRef{{PipelineSlug: "sync", RefSlug: "logical", DataSourceSlug: "physical", DataSourceType: model.DataSourceTypePostgres}}
	before, err := BindingFingerprint(original)
	require.NoError(t, err)
	original[0].BindingRevision = "new-deployment-revision"
	after, err := BindingFingerprint(original)
	require.NoError(t, err)
	require.Equal(t, before, after)
	raw, err := json.Marshal(original)
	require.NoError(t, err)
	require.JSONEq(t, `[{"PipelineSlug":"sync","RefSlug":"logical","DataSourceSlug":"physical","DataSourceType":"postgres"}]`, string(raw))
}
