package proofevidencesvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	bqconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	bigquery "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/stretchr/testify/require"
)

type credentialFake struct {
	calls int
	value []byte
}

func (f *credentialFake) ResolveProofCredential(_ context.Context, grant proofevidence.Grant) (credentialssvc.RuntimeCredential, error) {
	f.calls++
	return credentialssvc.RuntimeCredential{Version: grant.Request().CredentialVersion, PlaintextJSON: append([]byte(nil), f.value...)}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func tableTarget() bqconnector.TableMetadataObserver {
	return bqconnector.TableMetadataObserver{ProjectID: "project-1", DatasetID: "crm", TableID: "contacts"}
}

func requestFor(t *testing.T, target proofevidence.MetadataObserver) proofevidence.Request {
	t.Helper()
	r := testutil.ProofEvidenceRequest()
	plan, err := target.Plan()
	require.NoError(t, err)
	r.ConnectorType, r.Operation, r.Target, r.Egress = plan.ConnectorType, plan.Operation, plan.Target, plan.Egress
	r.TargetFingerprint, err = plan.Fingerprint()
	require.NoError(t, err)
	return r
}

func TestObserveBigQueryPreservesTableToViewChange(t *testing.T) {
	t.Parallel()
	accountJSON, err := os.ReadFile("../../pkg/testutil/testdata/bigquery-service-account.json")
	require.NoError(t, err)
	var account map[string]string
	require.NoError(t, json.Unmarshal(accountJSON, &account))
	account["token_uri"] = "https://attacker.example/token"
	accountJSON, err = json.Marshal(account)
	require.NoError(t, err)
	credentialJSON, err := json.Marshal(map[string]string{"service_account_json": string(accountJSON)})
	require.NoError(t, err)
	var digests []string
	for _, kind := range []bigquery.TableType{bigquery.TableTypeTable, bigquery.TableTypeView} {
		target := tableTarget()
		grant := testutil.AdmitProofEvidence(t, requestFor(t, target))
		credentials := &credentialFake{value: credentialJSON}
		svc, err := New(credentials)
		require.NoError(t, err)
		var calls []string
		got := svc.observe(t.Context(), grant, target, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls = append(calls, r.Method+" "+r.URL.String())
			if len(calls) == 1 {
				require.NoError(t, r.ParseForm())
				parts := strings.Split(r.Form.Get("assertion"), ".")
				require.Len(t, parts, 3)
				claims, err := base64.RawURLEncoding.DecodeString(parts[1])
				require.NoError(t, err)
				require.Contains(t, string(claims), `"scope":"https://www.googleapis.com/auth/bigquery.readonly"`)
				return response(`{"access_token":"fixture-token","expires_in":3600}`, http.StatusOK), nil
			}
			require.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
			return response(`{"tableReference":{"projectId":"project-1","datasetId":"crm","tableId":"contacts"},"type":"`+kind.String()+`","schema":{"fields":[{"name":"email","type":"STRING","mode":"NULLABLE"}]}}`, http.StatusOK), nil
		}))
		require.Equal(t, []string{"POST https://oauth2.googleapis.com/token", "GET https://bigquery.googleapis.com/bigquery/v2/projects/project-1/datasets/crm/tables/contacts"}, calls)
		require.Equal(t, proofevidence.OutcomeComplete, got.Result.Outcome)
		require.NoError(t, grant.Accept(got.Result))
		var metadata bqconnector.TableMetadataSnapshot
		require.NoError(t, json.Unmarshal(got.Snapshot.Metadata, &metadata))
		require.Equal(t, kind, metadata.TableType)
		require.EqualValues(t, 2, got.Result.Usage.Requests)
		require.EqualValues(t, 1, got.Result.Usage.Rows)
		require.Equal(t, 1, credentials.calls)
		digests = append(digests, got.Result.EvidenceDigest)
	}
	require.NotEqual(t, digests[0], digests[1])
}

func TestObserveHubSpotReadsOnlySelectedAccountProperties(t *testing.T) {
	t.Parallel()
	target := hubspot.ContactMetadataObserver{AccountID: 123}
	grant := testutil.AdmitProofEvidence(t, requestFor(t, target))
	svc, err := New(&credentialFake{value: []byte(`{"access_token":"fixture-token"}`)})
	require.NoError(t, err)
	var paths []string
	got := svc.observe(t.Context(), grant, target, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if len(paths) == 1 {
			return response(`{"portalId":123,"companyName":"private-company"}`, http.StatusOK), nil
		}
		return response(`{"results":[{"name":"email","type":"string","label":"private-label"}]}`, http.StatusOK), nil
	}))
	require.Equal(t, []string{crm.AccountMetadataPath, crm.ContactPropertiesPath}, paths)
	require.Equal(t, proofevidence.OutcomeComplete, got.Result.Outcome)
	var metadata hubspot.ContactMetadataSnapshot
	require.NoError(t, json.Unmarshal(got.Snapshot.Metadata, &metadata))
	require.Equal(t, "email", metadata.Properties[0].Name)
	encoded, err := json.Marshal(got.Snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-")
}

func TestObserveRejectsSubstitutionBeforeCredentialResolution(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"Target", "Operation", "Endpoint", "ZeroGrant", "Canceled"} {
		t.Run(scenario, func(t *testing.T) {
			target := tableTarget()
			r := requestFor(t, target)
			if scenario == "Operation" {
				r.Operation = "insert_rows"
			}
			if scenario == "Endpoint" {
				r.Target.Host = "attacker.example"
				r.Egress[0] = r.Target
			}
			grant := testutil.AdmitProofEvidence(t, r)
			if scenario == "Target" {
				target.TableID = "other"
			}
			if scenario == "ZeroGrant" {
				grant = proofevidence.Grant{}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := proofevidence.OutcomeDenied
			if scenario == "Canceled" {
				cancel()
				want = proofevidence.OutcomeCanceled
			}
			credentials := &credentialFake{}
			svc, err := New(credentials)
			require.NoError(t, err)
			got := svc.observe(ctx, grant, target, roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("provider dispatched")
				return nil, proofevidence.ErrAuthorizationDenied
			}))
			require.Equal(t, want, got.Result.Outcome)
			require.Zero(t, credentials.calls)
			require.Nil(t, got.Snapshot)
		})
	}
}

func TestObserveProviderFailuresNeverReturnEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
		limit      func(*proofevidence.Limits)
		want       proofevidence.Outcome
	}{
		{name: "AccountMismatch", body: `{"portalId":456}`, status: 200, want: proofevidence.OutcomePartial},
		{name: "MissingIdentity", body: `{}`, status: 200, want: proofevidence.OutcomePartial},
		{name: "Denied", body: `private-error`, status: 403, want: proofevidence.OutcomeDenied},
		{name: "RateLimited", body: `private-error`, status: 429, want: proofevidence.OutcomeRateLimited},
		{name: "Redirect", body: `private-error`, status: 302, want: proofevidence.OutcomeProviderError},
		{name: "Partial", body: `private-error`, status: 206, want: proofevidence.OutcomePartial},
		{name: "Malformed", body: `private-error`, status: 200, want: proofevidence.OutcomePartial},
		{name: "Bytes", body: strings.Repeat("x", 100), status: 200, limit: func(l *proofevidence.Limits) { l.MaxBytes = 10 }, want: proofevidence.OutcomeOverBudget},
		{name: "Requests", body: `{"portalId":123}`, status: 200, limit: func(l *proofevidence.Limits) { l.MaxRequests = 1 }, want: proofevidence.OutcomeOverBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := hubspot.ContactMetadataObserver{AccountID: 123}
			r := requestFor(t, target)
			if tc.limit != nil {
				tc.limit(&r.Limits)
			}
			grant := testutil.AdmitProofEvidence(t, r)
			svc, err := New(&credentialFake{value: []byte(`{"private_app_token":"fixture-token"}`)})
			require.NoError(t, err)
			calls := 0
			got := svc.observe(t.Context(), grant, target, roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(tc.body, tc.status), nil }))
			require.Equal(t, tc.want, got.Result.Outcome)
			require.Equal(t, 1, calls)
			require.Nil(t, got.Snapshot)
			require.Empty(t, got.Result.EvidenceDigest)
			require.ErrorIs(t, grant.Accept(got.Result), proofevidence.ErrAuthorizationDenied)
		})
	}
}

func TestObserveRejectsIncompleteOrOverBudgetProperties(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, properties string
		want             proofevidence.Outcome
	}{
		{"Missing", `{}`, proofevidence.OutcomePartial},
		{"Empty", `{"results":[]}`, proofevidence.OutcomePartial},
		{"MissingType", `{"results":[{"name":"email"}]}`, proofevidence.OutcomePartial},
		{"Duplicate", `{"results":[{"name":"email","type":"string"},{"name":"email","type":"string"}]}`, proofevidence.OutcomePartial},
		{"Rows", `{"results":[{"name":"email","type":"string"},{"name":"phone","type":"string"}]}`, proofevidence.OutcomeOverBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := hubspot.ContactMetadataObserver{AccountID: 123}
			r := requestFor(t, target)
			r.Limits.MaxRows = 1
			grant := testutil.AdmitProofEvidence(t, r)
			svc, err := New(&credentialFake{value: []byte(`{"access_token":"fixture-token"}`)})
			require.NoError(t, err)
			got := svc.observe(t.Context(), grant, target, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == crm.AccountMetadataPath {
					return response(`{"portalId":123}`, 200), nil
				}
				return response(tc.properties, 200), nil
			}))
			require.Equal(t, tc.want, got.Result.Outcome)
			require.Nil(t, got.Snapshot)
			require.Empty(t, got.Result.EvidenceDigest)
		})
	}
}

// fixtureObserver exercises the runner without any provider-specific schema.
type fixtureObserver struct {
	plan proofevidence.MetadataPlan
	rows int64
}

func (o fixtureObserver) Plan() (proofevidence.MetadataPlan, error) { return o.plan, nil }
func (o fixtureObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, _ []byte, _ proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.plan.Routes[0].URL, nil)
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	res, err := transport.HTTPClient().Do(req)
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	defer func() { _ = res.Body.Close() }()
	return json.RawMessage(`{"resource_kind":"fixture"}`), o.rows, proofevidence.OutcomeComplete
}

func TestObserveConnectorOwnedMetadataEnforcesSharedBudget(t *testing.T) {
	t.Parallel()
	endpoint := proofevidence.Endpoint{Scheme: "https", Host: "warehouse.example.com", Port: 443}
	for _, rows := range []int64{1, 2} {
		observer := fixtureObserver{plan: proofevidence.MetadataPlan{ConnectorType: model.DataSourceTypePostgres, Operation: "fixture.metadata", Resource: json.RawMessage(`{"name":"fixture"}`), Target: endpoint, Egress: []proofevidence.Endpoint{endpoint}, Routes: []proofevidence.MetadataRoute{{Method: http.MethodGet, URL: "https://warehouse.example.com/metadata"}}}, rows: rows}
		request := requestFor(t, observer)
		request.Limits.MaxRows = 1
		grant := testutil.AdmitProofEvidence(t, request)
		svc, err := New(&credentialFake{})
		require.NoError(t, err)
		got := svc.observe(t.Context(), grant, observer, roundTripFunc(func(*http.Request) (*http.Response, error) { return response(`{}`, 200), nil }))
		if rows == 1 {
			require.Equal(t, proofevidence.OutcomeComplete, got.Result.Outcome)
			require.JSONEq(t, `{"resource_kind":"fixture"}`, string(got.Snapshot.Metadata))
			require.NoError(t, grant.Accept(got.Result))
		} else {
			require.Equal(t, proofevidence.OutcomeOverBudget, got.Result.Outcome)
			require.Nil(t, got.Snapshot)
		}
	}
}

func TestObserveBigQueryOAuthUsesOnlyMetadataAndRejectsDeniedAccess(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			target := tableTarget()
			target.OAuth = true
			grant := testutil.AdmitProofEvidence(t, requestFor(t, target))
			credentials := &credentialFake{value: []byte(`{"access_token":"fixture-token"}`)}
			svc, err := New(credentials)
			require.NoError(t, err)
			calls := 0
			got := svc.observe(t.Context(), grant, target, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "GET https://bigquery.googleapis.com/bigquery/v2/projects/project-1/datasets/crm/tables/contacts", r.Method+" "+r.URL.String())
				require.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
				return response(`{"tableReference":{"projectId":"project-1","datasetId":"crm","tableId":"contacts"},"type":"TABLE","schema":{"fields":[{"name":"email","type":"STRING","mode":"NULLABLE"}]}}`, status), nil
			}))
			require.Equal(t, 1, calls)
			if status == http.StatusOK {
				require.Equal(t, proofevidence.OutcomeComplete, got.Result.Outcome)
				require.NoError(t, grant.Accept(got.Result))
				require.EqualValues(t, 1, got.Result.Usage.Requests)
			} else {
				require.Equal(t, proofevidence.OutcomeDenied, got.Result.Outcome)
			}
			if got.Snapshot != nil {
				require.NotContains(t, string(got.Snapshot.Metadata), "fixture-token")
			}
		})
	}
}
