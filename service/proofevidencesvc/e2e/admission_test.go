package e2e

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/stretchr/testify/require"
)

func TestReviewedIntentReservesOnceAndRejectsChangedAuthority(t *testing.T) {
	f := newFixture(t)
	compiler, services := newReceiptCompiler(t, f)
	plan, err := f.target.Plan()
	require.NoError(t, err)
	settings := proofreceiptsvc.ObservationPolicySettings{Enabled: true, TimeoutSeconds: 30, EvidenceTTLSeconds: 300, MaxRequests: 20, MaxRows: 10000, MaxBytes: 10 << 20}
	policy, err := compiler.SetObservationPolicy(t.Context(), f.scope, "reviewer", settings)
	require.NoError(t, err)
	read, err := compiler.GetObservationPolicy(t.Context(), f.scope, "reviewer")
	require.NoError(t, err)
	require.Equal(t, policy, read)
	preview, err := compiler.PrepareObservation(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, "sink", plan)
	require.NoError(t, err)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Zero(t, count, "preview cannot dispatch or reserve provider work")
	_, err = compiler.AuthorizeObservation(t.Context(), f.scope, "other-user", preview.IntentToken, preview.RequestDigest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	originalBinding := services.resolutions[1].DataSourceSlug
	services.resolutions[1].DataSourceSlug = "changed-warehouse"
	_, err = compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", preview.IntentToken, preview.RequestDigest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	services.resolutions[1].DataSourceSlug = originalBinding
	job, err := compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", preview.IntentToken, preview.RequestDigest)
	require.NoError(t, err)
	require.Equal(t, proofobservations.StateFinished.String(), job.State)
	stored, err := f.ledger.Job(t.Context(), f.scope, job.Slug)
	require.NoError(t, err)
	require.Equal(t, "reviewer", stored.Authorization.UserSlug.String())
	again, err := compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", preview.IntentToken, preview.RequestDigest)
	require.NoError(t, err)
	require.Equal(t, job.Slug, again.Slug)
	receipt, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{job.Slug})
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictFailed, appendClaim(t, receipt.Value).Verdict)
	settings.Enabled = false
	_, err = compiler.SetObservationPolicy(t.Context(), f.scope, "reviewer", settings)
	require.NoError(t, err)
	_, err = compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", preview.IntentToken, preview.RequestDigest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	settings.Enabled = true
	_, err = compiler.SetObservationPolicy(t.Context(), f.scope, "reviewer", settings)
	require.NoError(t, err)
	next, err := compiler.PrepareObservation(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, "sink", plan)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE credentials SET updated_at=updated_at+interval '1 second' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse-credential'`, f.scope.Organization)
	require.NoError(t, err)
	_, err = compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", next.IntentToken, next.RequestDigest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM proof_observation_jobs WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization).Scan(&count))
	require.Equal(t, 1, count)
	environmentPreview, err := compiler.PrepareObservation(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, "sink", plan)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE environments SET deleted_at=clock_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='staging'`, f.scope.Organization)
	require.NoError(t, err)
	_, err = compiler.AuthorizeObservation(t.Context(), f.scope, "reviewer", environmentPreview.IntentToken, environmentPreview.RequestDigest)
	require.Error(t, err)
	_, err = compiler.SetObservationPolicy(t.Context(), f.scope, "reviewer", settings)
	require.Error(t, err)
}
