package proofevidence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestResumePreservesTheReservedDeadline(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	authorization := authorize(t, request, now)
	deadline := now.Add(request.Limits.Timeout / 2)
	grant, err := policy.Resume(request, authorization, deadline)
	require.NoError(t, err)
	require.Equal(t, deadline, grant.Deadline(), "queue time cannot renew the observation budget")
	for _, invalid := range []time.Time{{}, now.Add(-time.Second), now.Add(request.Limits.Timeout * 2)} {
		_, err := policy.Resume(request, authorization, invalid)
		require.ErrorIs(t, err, ErrAuthorizationDenied)
	}
	policy.Enabled = false
	_, err = policy.Resume(request, authorization, deadline)
	require.ErrorIs(t, err, ErrAuthorizationDenied)
}

func TestPersistedAdmissionAndResultPreserveTheirBinding(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	authorization := authorize(t, request, now)
	type persistedAdmission struct {
		Policy        Policy
		Request       Request
		Authorization Authorization
	}
	stored := persistedAdmission{policy, request, authorization}
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	var restored persistedAdmission
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, stored, restored)
	grant, err := restored.Policy.Authorize(restored.Request, restored.Authorization)
	require.NoError(t, err)
	result := Result{
		RequestDigest: authorization.RequestDigest, Outcome: OutcomeComplete, EvidenceLevel: request.EvidenceLevel,
		EvidenceDigest: "sha256:" + strings.Repeat("e", 64), ObservedAt: grant.admittedAt,
		ExpiresAt: now.Add(time.Hour), Usage: Usage{Duration: time.Second, Requests: 1, MaxConcurrency: 1},
	}
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	var restoredResult Result
	require.NoError(t, json.Unmarshal(raw, &restoredResult))
	require.Equal(t, result, restoredResult)
	require.NoError(t, grant.Accept(restoredResult))
	restoredResult.RequestDigest = "sha256:" + strings.Repeat("f", 64)
	requireDenied(t, grant.Accept(restoredResult))
}

func TestPolicyAuthorizesOnlyExactBoundedRequest(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	authorization := authorize(t, request, now)

	grant, err := policy.Authorize(request, authorization)
	require.NoError(t, err)
	require.Equal(t, request, grant.Request())
	request.Egress[0].Host = "attacker.example"
	require.Equal(t, "warehouse.example.com", grant.Request().Egress[0].Host)
	returned := grant.Request()
	returned.Egress[0].Host = "attacker.example"
	require.Equal(t, "warehouse.example.com", grant.Request().Egress[0].Host)
}

func TestGrantRequiresCurrentAdmissionAndExactResourceBinding(t *testing.T) {
	t.Parallel()
	requireDenied(t, (Grant{}).Check())
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	for _, field := range []string{"Fingerprint", "CredentialVersion"} {
		candidate := request
		if field == "Fingerprint" {
			candidate.TargetFingerprint = ""
		} else {
			candidate.CredentialVersion = ""
		}
		_, err := policy.Authorize(candidate, authorize(t, candidate, now))
		requireDenied(t, err)
	}
	grant, err := policy.Authorize(request, authorize(t, request, now))
	require.NoError(t, err)
	require.NoError(t, grant.Check())
	grant.admittedAt = now.Add(-2 * request.Limits.Timeout)
	requireDenied(t, grant.Check())
	grant.admittedAt = now
	grant.authorization.ExpiresAt = now.Add(-time.Second)
	requireDenied(t, grant.Check())
}

func TestPolicyRejectsAuthorizationSubstitutionAndExpiredDecisions(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)

	tests := map[string]func(*Request, *Authorization){
		"incomplete candidate": func(r *Request, _ *Authorization) { r.Candidate.RepositoryID = "" },
		"tenant":               func(r *Request, _ *Authorization) { r.OrganizationSlug = "other" },
		"target":               func(r *Request, _ *Authorization) { r.DataSourceSlug = "other" },
		"resource fingerprint": func(r *Request, _ *Authorization) { r.TargetFingerprint = "sha256:" + strings.Repeat("f", 64) },
		"credential version":   func(r *Request, _ *Authorization) { r.CredentialVersion = "another-version" },
		"claim":                func(r *Request, _ *Authorization) { r.Claim.SemanticPath = "/other" },
		"operation":            func(r *Request, _ *Authorization) { r.Operation = "write" },
		"level":                func(r *Request, _ *Authorization) { r.EvidenceLevel = model.ProofEvidenceLevelStatic },
		"policy":               func(r *Request, _ *Authorization) { r.PolicyVersion = "other" },
		"decision digest":      func(_ *Request, a *Authorization) { a.RequestDigest = "sha256:" + strings.Repeat("f", 64) },
		"expired decision":     func(_ *Request, a *Authorization) { a.ExpiresAt = now },
		"wrong capability":     func(_ *Request, a *Authorization) { a.Capability = "git_provider" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.Egress = append([]Endpoint(nil), request.Egress...)
			decision := authorize(t, candidate, now)
			mutate(&candidate, &decision)
			_, err := policy.Authorize(candidate, decision)
			requireDenied(t, err)
		})
	}
}

func TestPolicyRejectsUnallowlistedEgressAndUnsafeExecution(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)

	tests := map[string]func(*Policy, *Request){
		"extra egress": func(_ *Policy, r *Request) {
			r.Egress = append(r.Egress, Endpoint{Scheme: "https", Host: "attacker.example", Port: 443})
		},
		"wildcard egress": func(_ *Policy, r *Request) {
			r.Target.Host, r.Egress[0].Host = "*.example.com", "*.example.com"
		},
		"plaintext target": func(_ *Policy, r *Request) {
			r.Target.Scheme, r.Egress[0].Scheme = "http", "http"
		},
		"private egress on managed worker": func(_ *Policy, r *Request) {
			r.Target.Host, r.Egress[0].Host = "10.0.0.1", "10.0.0.1"
		},
		"write capable live operation": func(p *Policy, _ *Request) {
			p.Operations[0].NoWriteGuarantee = NoWriteNativeValidation
		},
		"undeclared no-write guarantee": func(p *Policy, _ *Request) {
			p.Operations[0].NoWriteGuarantee = NoWriteGuarantee(100)
		},
		"private network on managed worker": func(_ *Policy, r *Request) { r.Worker.PrivateNetwork = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidatePolicy, candidateRequest := policy, request
			candidatePolicy.Operations = append([]OperationPolicy(nil), policy.Operations...)
			candidateRequest.Egress = append([]Endpoint(nil), request.Egress...)
			mutate(&candidatePolicy, &candidateRequest)
			_, err := candidatePolicy.Authorize(candidateRequest, authorize(t, candidateRequest, now))
			requireDenied(t, err)
		})
	}
}

func TestNativeMetadataStillRequiresPublicManagedEgress(t *testing.T) {
	t.Parallel()
	for _, kind := range []model.DataSourceType{model.DataSourceTypePostgres, model.DataSourceTypeMySQL, model.DataSourceTypeMongoDB} {
		for _, host := range []string{"database.example.com", "127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
			policy, request := validPolicyAndRequest(time.Now().UTC())
			request.ConnectorType, request.Operation = kind, kind.String()+".metadata"
			request.Target = Endpoint{Scheme: kind.String(), Host: host, Port: 12345}
			request.Egress = []Endpoint{request.Target}
			policy.Operations[0].ConnectorType, policy.Operations[0].Operation = request.ConnectorType, request.Operation
			policy.Operations[0].ProviderEndpoints = nil
			_, err := policy.Authorize(request, authorize(t, request, time.Now().UTC()))
			if host == "database.example.com" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrAuthorizationDenied)
			}
		}
	}
}

func TestNoWriteGuaranteeRejectsTransactionRollback(t *testing.T) {
	t.Parallel()
	_, err := NoWriteGuaranteeString("transaction_rollback")
	require.Error(t, err)
}

func TestPolicyRejectsEveryOverBudgetDimension(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)

	tests := map[string]func(*Limits){
		"timeout":     func(l *Limits) { l.Timeout++ },
		"rows":        func(l *Limits) { l.MaxRows++ },
		"bytes":       func(l *Limits) { l.MaxBytes++ },
		"requests":    func(l *Limits) { l.MaxRequests++ },
		"concurrency": func(l *Limits) { l.MaxConcurrency++ },
		"retries":     func(l *Limits) { l.MaxRetries++ },
		"rate":        func(l *Limits) { l.RateWindow-- },
		"cost":        func(l *Limits) { l.MaxCostUnits++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := request
			mutate(&candidate.Limits)
			_, err := policy.Authorize(candidate, authorize(t, candidate, now))
			requireDenied(t, err)
			require.ErrorContains(t, err, "limits exceed policy")
		})
	}
}

func TestPolicyRejectsAmbiguousOperationPolicy(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	policy.Operations = append(policy.Operations, policy.Operations[0])
	_, err := policy.Authorize(request, authorize(t, request, now))
	requireDenied(t, err)
	require.ErrorContains(t, err, "operation is not allowlisted")
}

func TestPolicyRequiresAttestedCustomerRunnerAndBoundedRetention(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	policy.Operations[0].RunnerTrust = RunnerTrustCustomer
	policy.Operations[0].AllowPrivateNetwork = true
	request.Worker = Worker{Identity: "customer_runner", Trust: RunnerTrustCustomer, PrivateNetwork: true}

	_, err := policy.Authorize(request, authorize(t, request, now))
	requireDenied(t, err)
	require.ErrorContains(t, err, "attestation is required")

	request.Worker.AttestationDigest = "sha256:" + strings.Repeat("d", 64)
	request.EvidenceRetainUntil = now.Add(policy.MaxEvidenceRetention + time.Second)
	_, err = policy.Authorize(request, authorize(t, request, now))
	requireDenied(t, err)
	require.ErrorContains(t, err, "retention exceeds policy")
}

func TestGrantAcceptsOnlyCompleteCurrentBoundedResults(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	policy, request := validPolicyAndRequest(now)
	grant, err := policy.Authorize(request, authorize(t, request, now))
	require.NoError(t, err)
	digest, err := request.Digest()
	require.NoError(t, err)
	result := Result{
		RequestDigest: digest, Outcome: OutcomeComplete, EvidenceLevel: request.EvidenceLevel,
		EvidenceDigest: "sha256:" + strings.Repeat("e", 64), ObservedAt: grant.admittedAt, ExpiresAt: now.Add(time.Hour),
		Usage: Usage{Duration: time.Second, Rows: 1, Bytes: 100, Requests: 1, MaxConcurrency: 1},
	}
	require.NoError(t, grant.Accept(result))
	for _, offset := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		reported := result
		reported.ObservedAt = now.Add(offset)
		require.NoError(t, grant.Accept(reported), "executor wall time is descriptive")
	}
	missingTime := result
	missingTime.ObservedAt = time.Time{}
	requireDenied(t, grant.Accept(missingTime))

	for _, outcome := range []Outcome{OutcomeUnsupported, OutcomeDenied, OutcomeUnreachable, OutcomeRateLimited, OutcomeTimedOut, OutcomeOverBudget, OutcomeCanceled, OutcomeWorkerLost, OutcomePartial, OutcomeProviderError, OutcomeAttestationFailed, OutcomeCleanupUncertain} {
		candidate := result
		candidate.Outcome = outcome
		err := grant.Accept(candidate)
		requireDenied(t, err)
		require.ErrorContains(t, err, "result is incomplete", outcome)
	}
	overBudget := result
	overBudget.Usage.Requests = request.Limits.MaxRequests + 1
	err = grant.Accept(overBudget)
	requireDenied(t, err)
	require.ErrorContains(t, err, "result is incomplete")

	afterAuthorizationExpiry := result
	afterAuthorizationExpiry.ObservedAt = now.Add(-30 * time.Second)
	expiredGrant := grant
	expiredGrant.admittedAt = now.Add(-2 * time.Minute)
	expiredGrant.authorization.ExpiresAt = now.Add(-time.Minute)
	err = expiredGrant.Accept(afterAuthorizationExpiry)
	requireDenied(t, err)
	require.ErrorContains(t, err, "result is incomplete")
}

func validPolicyAndRequest(now time.Time) (Policy, Request) {
	limits := Limits{Timeout: time.Minute, MaxRows: 100, MaxBytes: 1 << 20, MaxRequests: 4, MaxConcurrency: 1, MaxRetries: 2, RateWindow: time.Minute, MaxCostUnits: 1_000}
	target := Endpoint{Scheme: "https", Host: "warehouse.example.com", Port: 443}
	provider := Endpoint{Scheme: "https", Host: "auth.example.com", Port: 443}
	request := Request{
		JobID: "proof-job-1", OrganizationSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", CandidateSlug: "candidate-1",
		Candidate: model.ProofCandidateIdentity{
			OrganizationSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", ProviderConnectionSlug: "github",
			RepositoryID: "repo", ChangeRequestID: "42", HeadSHA: strings.Repeat("a", 40), TargetRef: "main", PipelinePath: "pipelines/orders.yaml",
			SpecHash: "sha256:" + strings.Repeat("b", 64), BindingFingerprint: "sha256:" + strings.Repeat("c", 64), CapabilityFingerprint: "sha256:" + strings.Repeat("d", 64), CompilerVersion: "v1", PolicyVersion: "evidence-v1",
		},
		Claim:          model.ProofClaimIdentity{Family: model.ProofClaimFamilyAccessCapability, PipelineVersionHash: "sha256:" + strings.Repeat("b", 64), SemanticPath: "/steps/source"},
		DataSourceSlug: "warehouse", CredentialDataSourceSlug: "warehouse", ConnectorType: model.DataSourceTypeBigQuery,
		CredentialVersion: "credential-1/version-1", TargetFingerprint: "sha256:" + strings.Repeat("e", 64),
		Operation: "inspect_schema", EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly,
		Target: target, Egress: []Endpoint{target, provider}, Limits: limits, Worker: Worker{Identity: "proof_worker", Trust: RunnerTrustManaged}, PolicyVersion: "evidence-v1",
		EvidenceRetainUntil: now.Add(24 * time.Hour), MetadataRetainUntil: now.Add(30 * 24 * time.Hour),
	}
	policy := Policy{
		Enabled: true, Version: "evidence-v1", MaxAuthorizationTTL: 5 * time.Minute, MaxEvidenceRetention: 7 * 24 * time.Hour, MaxMetadataRetention: 90 * 24 * time.Hour,
		Operations: []OperationPolicy{{ConnectorType: model.DataSourceTypeBigQuery, Operation: "inspect_schema", EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly, NoWriteGuarantee: NoWriteReadOnlyAPI, ProviderEndpoints: []Endpoint{provider}, Limits: limits, RunnerTrust: RunnerTrustManaged}},
	}
	return policy, request
}

func authorize(t *testing.T, request Request, now time.Time) Authorization {
	t.Helper()
	digest, err := request.Digest()
	require.NoError(t, err)
	return Authorization{ID: "authorization-1", Capability: Capability, RequestDigest: digest, PolicyVersion: request.PolicyVersion, AuthorizedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
}

func requireDenied(t *testing.T, err error) {
	t.Helper()
	require.True(t, xerror.Is(err, ErrAuthorizationDenied), "got %v", err)
}

func TestResumedGrantUsesAuthorityTimeAndMonotonicBudget(t *testing.T) {
	t.Parallel()
	for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		now := time.Now().Add(skew)
		policy, request := validPolicyAndRequest(now)
		grant, err := policy.ResumeAt(request, authorize(t, request, now), now.Add(10*time.Second), now)
		require.NoError(t, err)
		require.NoError(t, grant.Check())
		require.Greater(t, grant.Remaining(), 9*time.Second)
		require.LessOrEqual(t, grant.Remaining(), 10*time.Second)
		grant.referenceLocal = grant.referenceLocal.Add(-11 * time.Second)
		requireDenied(t, grant.Check())
	}
}
