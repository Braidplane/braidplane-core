package executionbroker

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestFTPArtifactPreparationMatchesSavedStatement(t *testing.T) {
	config := []byte(`{"host":"ftp.example.test","port":21}`)
	digest, err := connector.PublicConfigDigest(model.DataSourceTypeFTP, config)
	require.NoError(t, err)

	for _, tc := range []struct {
		name, saved, requested string
		wantPrepared           bool
	}{
		{name: "comma", saved: ",", requested: ",", wantPrepared: true},
		{name: "escaped ampersand", saved: "&", requested: "&", wantPrepared: true},
		{name: "different delimiter", saved: "&", requested: ","},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement := func(delimiter string) string {
				return `{"operation":"write","format":"csv","path":"orders.csv","delimiter":"` + delimiter + `"}`
			}
			saved, err := json.Marshal(map[string]string{"statement": statement(tc.saved)})
			require.NoError(t, err)
			payload, err := json.Marshal(FTPArtifactRequest{
				ConnectorExecutionRequest: queriessvc.ConnectorExecutionRequest{
					OrganizationSlug: "acme", DataSourceSlug: "ftp", DataSourceType: model.DataSourceTypeFTP, SinkExecution: true,
					ConfigJSON: config, PinnedConfigDigest: digest, StatementJSON: json.RawMessage(statement(tc.requested)),
					Limits: types.ExecutionLimits{MaxResponseBytes: 1024, MaxFileBytes: 1024},
				},
				SavedQueryData: saved, Directory: "reserved", Content: json.RawMessage(`[{"id":"102","name":"sample"}]`),
				ArtifactEvidence: ftp.ArtifactEvidence{DeliveryID: uuid.New()},
			})
			require.NoError(t, err)

			response, prepared := (&Broker{}).processFTPArtifact(t.Context(), requestrepo.Request{
				OrganizationSlug: "acme", Kind: kindFTPArtifactPrepare,
			}, payload)
			require.Equal(t, tc.wantPrepared, prepared)
			if tc.wantPrepared {
				require.NotNil(t, response.FTPArtifact)
				require.NotNil(t, response.FTPArtifact.Evidence)
			} else {
				require.Equal(t, artifactBindingRefused, response.ErrorCode)
			}
		})
	}
}
