package s3_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObjectMetadataUsesOnlySelectedHEAD(t *testing.T) {
	for _, key := range []string{"exports/selected.json", "exports/name=one+two.json", "exports/space %2F.json"} {
		for _, exists := range []bool{true, false} {
			t.Run(key+"/"+map[bool]string{true: "exists", false: "missing"}[exists], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					assert.Equal(t, http.MethodHead, r.Method)
					assert.Equal(t, "/fixture/"+key, r.URL.Path)
					if !exists {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				observer := connector.ObjectMetadataObserver{Endpoint: "https://storage.example.com", Region: "eu-north-1", Bucket: "fixture", Key: key}
				plan, err := observer.Plan()
				require.NoError(t, err)
				raw, rows, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_key_id":"fixture-key","secret_access_key":"fixture-secret"}`), proofevidence.Limits{MaxRows: 1, MaxBytes: 1024})
				require.Equal(t, proofevidence.OutcomeComplete, outcome)
				require.Equal(t, int64(1), rows)
				require.Equal(t, 1, calls)
				var snapshot connector.ObjectMetadataSnapshot
				require.NoError(t, json.Unmarshal(raw, &snapshot))
				require.Equal(t, exists, snapshot.Exists)
				verdict, _, err := observer.AssessMetadata(raw)
				require.NoError(t, err)
				if exists {
					require.Equal(t, model.ProofVerdictPassed, verdict)
				} else {
					require.Equal(t, model.ProofVerdictFailed, verdict)
				}
				require.True(t, observer.MatchesResource(json.RawMessage(`{"endpoint":"https://storage.example.com","region":"eu-north-1","bucket":"fixture","prefix":"exports"}`), json.RawMessage(`{"type":"s3","operation":"read","key":"`+key[len("exports/"):]+`"}`), true))
				require.False(t, observer.MatchesResource(json.RawMessage(`{"endpoint":"https://storage.example.com","region":"eu-north-1","bucket":"fixture","prefix":"exports"}`), json.RawMessage(`{"type":"s3","operation":"read","key":"other.json"}`), true))
			})
		}
	}
}
