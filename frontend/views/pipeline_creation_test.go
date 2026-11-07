package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestCreationListDoesNotCallUnavailableHistoryEmpty(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		data := PipelineCreationListData{Org: "acme", Project: "ops", Page: &api.PipelineCreationProposalListResponse{PipelineCreations: []api.PipelineCreationProposal{}}}
		if unavailable {
			data.Page = nil
			data.Error = "Creation proposals are unavailable. Check pipeline read access and refresh."
		}
		var output bytes.Buffer
		require.NoError(t, PipelineCreationListContent(data).Render(context.Background(), &output))
		if unavailable {
			require.Contains(t, output.String(), `role="alert"`)
			require.NotContains(t, output.String(), "No creation proposals")
		} else {
			require.Contains(t, output.String(), "No creation proposals on this page")
			require.NotContains(t, output.String(), `role="alert"`)
		}
	}
}

func TestQueryPipelineSetupHidesProposalWithoutCreatePermission(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, QueryPipelineSetupContent(QueryPipelineSetupData{
		Org: "acme", DataSource: "warehouse", Query: &api.Query{Slug: "orders", Name: "Orders"},
	}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Pipeline creation needs access")
	require.NotContains(t, out.String(), "Propose pipeline")
	require.NotContains(t, out.String(), `<form`)
}
