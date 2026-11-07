package testutil

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

// ProofEvidenceRequest provides complete identities for service-boundary tests.
func ProofEvidenceRequest() proofevidence.Request {
	now := time.Now().UTC()
	hash := "sha256:" + strings.Repeat("b", 64)
	// #nosec G101 -- Public fixture identities and a version reference, no credential material.
	return proofevidence.Request{
		JobID: "proof-job-1", OrganizationSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", CandidateSlug: "candidate-1",
		Candidate: model.ProofCandidateIdentity{
			OrganizationSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
			RepositoryID: "repo", ChangeRequestID: "42", HeadSHA: strings.Repeat("a", 40), TargetRef: "main", PipelinePath: "pipelines/contacts.yaml",
			SpecHash: hash, BindingFingerprint: hash, CapabilityFingerprint: hash, CompilerVersion: "v1", PolicyVersion: "evidence-v1",
		},
		Claim:          model.ProofClaimIdentity{Family: model.ProofClaimFamilyAccessCapability, PipelineVersionHash: hash, SemanticPath: "/steps/sink"},
		DataSourceSlug: "warehouse", CredentialDataSourceSlug: "warehouse", CredentialVersion: "credential-1/version-1", TargetFingerprint: hash,
		ConnectorType: model.DataSourceTypeBigQuery, Operation: "bigquery.table_metadata", EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly,
		Target: proofevidence.Endpoint{Scheme: "https", Host: "bigquery.googleapis.com", Port: 443},
		Egress: []proofevidence.Endpoint{{Scheme: "https", Host: "bigquery.googleapis.com", Port: 443}, {Scheme: "https", Host: "oauth2.googleapis.com", Port: 443}},
		Limits: proofevidence.Limits{Timeout: time.Minute, MaxRows: 100, MaxBytes: 1 << 20, MaxRequests: 2, MaxConcurrency: 1, RateWindow: time.Minute},
		Worker: proofevidence.Worker{Identity: "proof_worker", Trust: proofevidence.RunnerTrustManaged}, PolicyVersion: "evidence-v1",
		EvidenceRetainUntil: now.Add(time.Hour), MetadataRetainUntil: now.Add(24 * time.Hour),
	}
}

func AdmitProofEvidence(t *testing.T, request proofevidence.Request) proofevidence.Grant {
	t.Helper()
	policy := proofevidence.Policy{
		Enabled: true, Version: request.PolicyVersion, MaxAuthorizationTTL: 5 * time.Minute,
		MaxEvidenceRetention: 24 * time.Hour, MaxMetadataRetention: 48 * time.Hour,
		Operations: []proofevidence.OperationPolicy{{
			ConnectorType: request.ConnectorType, Operation: request.Operation,
			EvidenceLevel: request.EvidenceLevel, NoWriteGuarantee: proofevidence.NoWriteReadOnlyAPI,
			ProviderEndpoints: request.Egress[1:], Limits: request.Limits, RunnerTrust: proofevidence.RunnerTrustManaged,
		}},
	}
	digest, err := request.Digest()
	require.NoError(t, err)
	now := time.Now().UTC()
	grant, err := policy.Authorize(request, proofevidence.Authorization{
		ID: "authorization-1", Capability: proofevidence.Capability,
		RequestDigest: digest, PolicyVersion: request.PolicyVersion, AuthorizedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	})
	require.NoError(t, err)
	return grant
}

// ProofMetadataClient directs an observer's exact requests to a local provider
// fixture without adding endpoint overrides to production observation code.
func ProofMetadataClient(t *testing.T, plan proofevidence.MetadataPlan, baseURL string) proofevidence.MetadataIO {
	t.Helper()
	base, err := url.Parse(baseURL)
	require.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	return ProofHTTPTransport{Client: &http.Client{Timeout: 5 * time.Second, Transport: &proofMetadataTransport{t: t, routes: slices.Clone(plan.Routes), base: base, next: transport}}}
}

type proofMetadataTransport struct {
	t      *testing.T
	routes []proofevidence.MetadataRoute
	base   *url.URL
	next   http.RoundTripper
}

func (tr *proofMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.t.Helper()
	require.NotEmpty(tr.t, tr.routes, "unexpected metadata request")
	require.Equal(tr.t, tr.routes[0].Method, req.Method)
	require.Equal(tr.t, tr.routes[0].URL, req.URL.String())
	tr.routes = tr.routes[1:]
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = tr.base.Scheme, tr.base.Host, tr.base.Host
	return tr.next.RoundTrip(clone)
}

// ProofHTTPTransport admits only HTTP in HTTPS observer unit tests.
type ProofHTTPTransport struct{ Client *http.Client }

func (t ProofHTTPTransport) HTTPClient() *http.Client { return t.Client }
func (t ProofHTTPTransport) BeginNative(proofevidence.MetadataRoute) error {
	return proofevidence.ErrAuthorizationDenied
}

func (t ProofHTTPTransport) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, proofevidence.ErrAuthorizationDenied
}

// ProofNativeTransport confines dependency tests to a known local fixture.
type ProofNativeTransport struct {
	mu      sync.Mutex
	Plan    proofevidence.MetadataPlan
	Address string
	Started bool
	Dials   int
}

func (t *ProofNativeTransport) HTTPClient() *http.Client { return nil }
func (t *ProofNativeTransport) BeginNative(route proofevidence.MetadataRoute) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Started || len(t.Plan.Routes) != 1 || route != t.Plan.Routes[0] {
		return proofevidence.ErrAuthorizationDenied
	}
	t.Started = true
	return nil
}

func (t *ProofNativeTransport) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	t.mu.Lock()
	if !t.Started || t.Dials >= t.Plan.MaxConnections || network != "tcp" || address != t.Address {
		t.mu.Unlock()
		return nil, proofevidence.ErrAuthorizationDenied
	}
	t.Dials++
	t.mu.Unlock()
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return conn, nil
}
