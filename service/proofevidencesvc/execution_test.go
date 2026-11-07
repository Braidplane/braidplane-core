package proofevidencesvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/require"
)

func TestDeliveredGrantKeepsItsDeadlineAndCopiesOnlyTheAdmittedCredential(t *testing.T) {
	t.Parallel()
	target := tableTarget()
	r := requestFor(t, target)
	r.Worker.Identity = ManagedExecutorIdentity
	plan, err := target.Plan()
	require.NoError(t, err)
	digest, err := r.Digest()
	require.NoError(t, err)
	now := time.Now().UTC()
	delivery := ExecutionRequest{
		Request: r, Plan: plan, Deadline: now.Add(10 * time.Second),
		Authorization: proofevidence.Authorization{ID: "intent", Capability: proofevidence.Capability, RequestDigest: digest, PolicyVersion: r.PolicyVersion, AuthorizedAt: now, ExpiresAt: now.Add(time.Minute)},
		Policy:        proofevidence.Policy{Enabled: true, Version: r.PolicyVersion, MaxAuthorizationTTL: time.Minute, MaxEvidenceRetention: 24 * time.Hour, MaxMetadataRetention: 48 * time.Hour, Operations: []proofevidence.OperationPolicy{{ConnectorType: r.ConnectorType, Operation: r.Operation, EvidenceLevel: r.EvidenceLevel, NoWriteGuarantee: proofevidence.NoWriteReadOnlyAPI, ProviderEndpoints: r.Egress[1:], Limits: r.Limits, RunnerTrust: proofevidence.RunnerTrustManaged}}},
		Credential:    credentialssvc.RuntimeCredential{Version: r.CredentialVersion, PlaintextJSON: []byte(`{"fixture":true}`)},
	}
	grant, err := delivery.Grant()
	require.NoError(t, err)
	require.Equal(t, delivery.Deadline, grant.Deadline())
	for _, authorityTime := range []time.Time{delivery.Deadline.Add(time.Nanosecond), delivery.Authorization.ExpiresAt} {
		_, err := delivery.GrantAt(authorityTime)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		observation := ExecuteMetadata(t.Context(), delivery, target, authorityTime)
		require.Equal(t, proofevidence.OutcomeDenied, observation.Result.Outcome)
		require.Zero(t, observation.Result.Usage.Requests)
		require.Nil(t, observation.Snapshot)
	}
	resolver := deliveredCredential{delivery.Credential}
	credential, err := resolver.ResolveProofCredential(t.Context(), grant)
	require.NoError(t, err)
	clear(credential.PlaintextJSON)
	require.Equal(t, `{"fixture":true}`, string(delivery.Credential.PlaintextJSON), "clearing a use must not mutate the delivery's credential")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = resolver.ResolveProofCredential(ctx, grant)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	observation := ExecuteMetadata(ctx, delivery, target, time.Now())
	require.Equal(t, proofevidence.OutcomeCanceled, observation.Result.Outcome)
	require.Zero(t, observation.Result.Usage.Requests)
	require.Nil(t, observation.Snapshot)
	resolver.credential.Version = "rotated"
	_, err = resolver.ResolveProofCredential(t.Context(), grant)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	delivery.Credential.Version = "rotated"
	observation = ExecuteMetadata(t.Context(), delivery, target, time.Now())
	require.Equal(t, proofevidence.OutcomeDenied, observation.Result.Outcome)
	require.Zero(t, observation.Result.Usage.Requests)
	require.Nil(t, observation.Snapshot)
}
