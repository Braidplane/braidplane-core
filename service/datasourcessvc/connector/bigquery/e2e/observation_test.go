package bigquery_test

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	bigqueryconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	"github.com/stretchr/testify/require"
)

// Shares the provider fixture with ordinary introspection tests.
func TestTableMetadataObserverReadsSelectedResource(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local provider fixture")
	}
	resetMock(t, t.Context())
	observer := bigqueryconnector.TableMetadataObserver{ProjectID: "analytics-prod", DatasetID: "analytics", TableID: "orders"}
	plan, err := observer.Plan()
	require.NoError(t, err)
	_, err = plan.Fingerprint()
	require.NoError(t, err)
	credentials, err := json.Marshal(testCredentials())
	require.NoError(t, err)
	limits := testutil.ProofEvidenceRequest().Limits
	payload, rows, outcome := observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), testutil.ProofMetadataClient(t, plan, bigQueryMockBaseURL()), credentials, limits)
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.Positive(t, rows)
	var snapshot bigqueryconnector.TableMetadataSnapshot
	require.NoError(t, json.Unmarshal(payload, &snapshot))
	require.Equal(t, client.TableTypeTable, snapshot.TableType)
	require.NotEmpty(t, snapshot.Fields)
	verdict, diagnostics, err := observer.AssessMetadata(payload)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictUnverified, verdict)
	require.Len(t, diagnostics, 1)
	require.Equal(t, "proof.append_permission_unverified", diagnostics[0].Code)
	config, err := json.Marshal(bigqueryconnector.Config{ProjectID: observer.ProjectID, DefaultDataset: observer.DatasetID})
	require.NoError(t, err)
	operation := json.RawMessage(`{"type":"bigquery","operation":"insert_rows","table":"orders"}`)
	require.True(t, observer.MatchesResource(config, operation, false))
	other := observer
	other.TableID = "other_table"
	require.False(t, other.MatchesResource(config, operation, false))
	require.False(t, observer.MatchesResource(config, operation, true))
	limits.MaxRows = 1
	payload, rows, outcome = observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), testutil.ProofMetadataClient(t, plan, bigQueryMockBaseURL()), credentials, limits)
	require.Equal(t, proofevidence.OutcomeOverBudget, outcome)
	require.Greater(t, rows, limits.MaxRows)
	require.Nil(t, payload)
}
