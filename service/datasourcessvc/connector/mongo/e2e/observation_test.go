package e2e

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	"github.com/stretchr/testify/require"
)

func TestSelectedCollectionMetadataDoesNotSampleDocuments(t *testing.T) {
	config := testMongoConfig()
	creds := testMongoCredentials()
	// Authenticate against the externally mapped fixture before its bounded observation.
	ready, err := connector.New(transportsecurity.WithPlaintext(t.Context()), config, creds)
	require.NoError(t, err)
	require.NoError(t, ready.Close())
	collection := "users"
	port, err := strconv.ParseUint(strconv.Itoa(config.Port), 10, 16)
	require.NoError(t, err)
	observer := connector.CollectionMetadataObserver{Host: config.Host, Port: uint16(port), Database: config.Database, Collection: collection, AuthDatabase: config.AuthDatabase}
	plan, err := observer.Plan()
	require.NoError(t, err)
	// #nosec G117 -- Local fixture credential JSON is passed only to the observer.
	credential, err := json.Marshal(creds)
	require.NoError(t, err)
	transport := &testutil.ProofNativeTransport{Plan: plan, Address: net.JoinHostPort(config.Host, strconv.Itoa(config.Port))}
	raw, rows, outcome := observer.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport, credential, proofevidence.Limits{Timeout: 5 * time.Second, MaxRows: 1, MaxBytes: 1 << 20})
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.Equal(t, int64(1), rows)
	require.JSONEq(t, `{"database":"braidplane","collection":"`+collection+`","type":"collection"}`, string(raw))
	require.LessOrEqual(t, transport.Dials, 2)
	require.NotContains(t, string(raw), "private")
	verdict, diagnostics, err := observer.AssessMetadata(raw)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictPassed, verdict)
	require.Empty(t, diagnostics)
	other := observer
	other.Collection = "other_collection"
	_, _, err = other.AssessMetadata(raw)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	config.AllowPrivateHosts = false
	boundConfig, err := json.Marshal(config)
	require.NoError(t, err)
	operation := json.RawMessage(`{"type":"mongodb","operation":"find","collection":"users"}`)
	require.True(t, observer.MatchesResource(boundConfig, operation, true))
	require.False(t, other.MatchesResource(boundConfig, operation, true))
	require.False(t, observer.MatchesResource(boundConfig, operation, false))
	require.True(t, observer.MatchesResource(boundConfig, json.RawMessage(`{"type":"mongodb","operation":"insert_one","collection":"users"}`), false))
	for _, tc := range []struct {
		name       string
		collection string
		maxRows    int64
		maxBytes   int64
		outcome    proofevidence.Outcome
	}{
		{name: "MissingCollection", collection: "proof_missing_collection", maxRows: 1, maxBytes: 1 << 20, outcome: proofevidence.OutcomePartial},
		{name: "RowBudget", collection: collection, maxRows: 0, maxBytes: 1 << 20, outcome: proofevidence.OutcomeOverBudget},
		{name: "ByteBudget", collection: collection, maxRows: 1, maxBytes: 1, outcome: proofevidence.OutcomeOverBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := observer
			selected.Collection = tc.collection
			selectedPlan, err := selected.Plan()
			require.NoError(t, err)
			transport := &testutil.ProofNativeTransport{Plan: selectedPlan, Address: net.JoinHostPort(config.Host, strconv.Itoa(config.Port))}
			raw, _, outcome := selected.ReadMetadata(transportsecurity.WithPlaintext(t.Context()), transport, credential, proofevidence.Limits{Timeout: 5 * time.Second, MaxRows: tc.maxRows, MaxBytes: tc.maxBytes})
			require.Equal(t, tc.outcome, outcome)
			require.Nil(t, raw, "incomplete or over-budget metadata must not become evidence")
			require.LessOrEqual(t, transport.Dials, 2)
		})
	}
}
