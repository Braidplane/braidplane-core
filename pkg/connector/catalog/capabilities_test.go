package catalog

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/stretchr/testify/require"
)

func TestEveryShippedConnectorDescribesItsCapabilityScope(t *testing.T) {
	for _, descriptor := range AllDescriptors() {
		t.Run(descriptor.Type.String(), func(t *testing.T) {
			d := descriptor.Capabilities
			require.NotEmpty(t, d.ReadOperations)
			require.NotEmpty(t, d.WriteOperations)
			require.NotEmpty(t, d.LoadModes)
			require.NotEmpty(t, d.SourcePayloads)
			require.NotEmpty(t, d.SinkPayloads)
			require.NotEmpty(t, d.Pagination.Scope)
			require.NotEmpty(t, d.Pagination.Limitation)
			require.NotEmpty(t, d.Pagination.Throttling)
			require.NotEmpty(t, d.Authentication.Modes)
			require.NotEmpty(t, d.Authentication.Renewal)
			require.NotEmpty(t, d.Authentication.Recovery)
			require.NotEmpty(t, d.Limitations)
			seen := map[model.ContractRepresentation]bool{}
			for _, support := range d.SinkPayloads {
				require.NotEqual(t, model.ContractRepresentationUnknown, support.Representation)
				require.True(t, support.Representation.IsAContractRepresentation())
				require.NotEqual(t, model.ConnectorPayloadModeUnknown, support.Mode)
				require.True(t, support.Mode.IsAConnectorPayloadMode())
				require.NotEmpty(t, support.Limitation)
				require.False(t, seen[support.Representation], "payload support must have one unambiguous binding mode")
				seen[support.Representation] = true
			}
		})
	}
}

func TestCapabilityMetadataDoesNotAdvertiseUnavailableOperations(t *testing.T) {
	bigquery, _ := DescriptorFor(model.DataSourceTypeBigQuery)
	require.Equal(t, []string{"append"}, bigquery.Capabilities.LoadModes)
	require.Equal(t, []string{"insert_rows"}, bigquery.Capabilities.WriteOperations)
	mongo, _ := DescriptorFor(model.DataSourceTypeMongoDB)
	require.Equal(t, []model.ContractRepresentation{model.ContractRepresentationJSON}, mongo.Capabilities.SourcePayloads)
	require.NotContains(t, mongo.Capabilities.LoadModes, "upsert")
	require.NotContains(t, mongo.Capabilities.WriteOperations, "replace_one")
	require.NotContains(t, mongo.Capabilities.WriteOperations, "insert_many", "adapter-only operations must not bypass the public statement contract")
	s3, _ := DescriptorFor(model.DataSourceTypeS3)
	require.Contains(t, s3.Capabilities.Pagination.Scope, "introspection")
	for _, input := range s3.Capabilities.SinkPayloads {
		require.NotEqual(t, model.ContractRepresentationMultipartForm, input.Representation)
	}
	github, _ := DescriptorFor(model.DataSourceTypeGithub)
	require.Equal(t, []string{"github_app"}, github.Capabilities.Authentication.Modes)
	require.NotContains(t, github.Capabilities.ReadOperations, "issues")
	workspace, _ := DescriptorFor(model.DataSourceTypeGoogleWorkspace)
	require.Contains(t, workspace.Capabilities.WriteOperations, "spreadsheet.update")
	require.NotContains(t, workspace.Capabilities.WriteOperations, "spreadsheet.append")
}
