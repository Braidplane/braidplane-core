package hubspot_test

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/stretchr/testify/require"
)

// Shares the provider fixture with ordinary introspection tests.
func TestContactMetadataObserverChecksAccountBeforeSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local provider fixture")
	}
	resetMock(t, t.Context())
	observer := hubspotconnector.ContactMetadataObserver{AccountID: 12345}
	plan, err := observer.Plan()
	require.NoError(t, err)
	_, err = plan.Fingerprint()
	require.NoError(t, err)
	// #nosec G117 -- Serializes only the existing public mock credential fixture.
	credentials, err := json.Marshal(testCredentials())
	require.NoError(t, err)
	limits := testutil.ProofEvidenceRequest().Limits
	payload, rows, outcome := observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), testutil.ProofMetadataClient(t, plan, hubSpotMockBaseURL()), credentials, limits)
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.Positive(t, rows)
	var snapshot hubspotconnector.ContactMetadataSnapshot
	require.NoError(t, json.Unmarshal(payload, &snapshot))
	require.NotEmpty(t, snapshot.Properties)
	for _, property := range snapshot.Properties {
		require.Empty(t, property.Label)
	}
	observer.AccountID++
	payload, rows, outcome = observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), testutil.ProofMetadataClient(t, plan, hubSpotMockBaseURL()), credentials, limits)
	require.Equal(t, proofevidence.OutcomePartial, outcome)
	require.Zero(t, rows)
	require.Nil(t, payload)
}
