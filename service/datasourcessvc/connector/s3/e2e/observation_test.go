package s3_e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/stretchr/testify/require"
)

func TestSelectedObjectMetadataDoesNotReadBody(t *testing.T) {
	requireS3Reachable(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	provider := newProviderClient(t, ctx)
	bucket := uniqueBucketName("proof-metadata")
	createBucket(t, ctx, provider, bucket)
	putObject(t, ctx, provider, bucket, "fixtures/selected.json", `{"private":"not evidence"}`)
	cfg := testConfig(bucket, "fixtures")
	fixtureEndpoint := cfg.Endpoint
	cfg.Endpoint, cfg.AllowPrivateHosts = "https://storage.example.com", false
	config, err := json.Marshal(cfg)
	require.NoError(t, err)
	// #nosec G117 -- Local fixture credential JSON is passed only to the observer.
	credential, err := json.Marshal(testCredentials())
	require.NoError(t, err)
	observer := connector.ObjectMetadataObserver{Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: bucket, Key: "fixtures/selected.json"}
	require.True(t, observer.MatchesResource(config, json.RawMessage(`{"type":"s3","operation":"read","key":"selected.json"}`), true))
	require.False(t, observer.MatchesResource(config, json.RawMessage(`{"type":"s3","operation":"read","key":"other.json"}`), true))
	for _, exists := range []bool{true, false} {
		if !exists {
			observer.Key = "fixtures/missing.json"
		}
		plan, err := observer.Plan()
		require.NoError(t, err)
		// The fixture uses HTTP; only its exact HEAD replaces the public route.
		plan.Routes[0].URL = strings.Replace(plan.Routes[0].URL, cfg.Endpoint, fixtureEndpoint, 1)
		fixture := observer
		fixture.Endpoint = fixtureEndpoint
		raw, rows, outcome := fixture.ReadMetadata(transportsecurity.WithPlaintext(ctx), testutil.ProofMetadataClient(t, plan, fixtureEndpoint), credential, proofevidence.Limits{Timeout: 5 * time.Second, MaxRows: 1, MaxBytes: 1024})
		require.Equal(t, proofevidence.OutcomeComplete, outcome)
		require.Equal(t, int64(1), rows)
		require.NotContains(t, string(raw), "private")
		var snapshot connector.ObjectMetadataSnapshot
		require.NoError(t, json.Unmarshal(raw, &snapshot))
		require.Equal(t, exists, snapshot.Exists)
		verdict, diagnostics, err := observer.AssessMetadata(raw)
		require.NoError(t, err)
		if exists {
			require.Equal(t, model.ProofVerdictPassed, verdict)
			require.Empty(t, diagnostics)
		} else {
			require.Equal(t, model.ProofVerdictFailed, verdict)
			require.Equal(t, "proof.object_missing", diagnostics[0].Code)
		}
		other := observer
		other.Key = "fixtures/other.json"
		_, _, err = other.AssessMetadata(raw)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
}
