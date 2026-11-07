package proofstatussvc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
)

func TestPublishUsesOnlySanitizedAggregateAndExactHead(t *testing.T) {
	client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}}
	svc := testService(client)
	level := model.ProofEvidenceLevelStatic
	receipt := model.ProofReceipt{
		ID: "proof-1", AggregateVerdict: model.ProofVerdictUnverified,
		Candidate: model.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40)},
		Claims:    []model.ProofClaim{{Verdict: model.ProofVerdictPassed, ActualEvidenceLevel: &level, Description: "secret schema name"}, {Verdict: model.ProofVerdictUnverified}},
	}
	svc.receipts = fakeReceiptReader{receipt}
	require.NoError(t, svc.Publish(t.Context(), receipt))
	require.Equal(t, receipt.Candidate.HeadSHA, client.check.HeadSHA)
	require.NotContains(t, client.check.Summary, "secret schema name")
	require.Contains(t, client.check.Summary, "1 passed")
	require.Equal(t, "https://app.example.test/orgs/acme/projects/data/environments/staging/proof-receipts/proof-1", client.check.DetailsURL)
}

type gitlabAuth struct{}

func (gitlabAuth) ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
	return &datasourcessvc.GitDataSourceAuth{Provider: "gitlab", RepoURL: "https://gitlab.com/acme/pipelines.git", Env: []string{"GITLAB_TOKEN=managed-oauth"}}, nil
}

func TestGitLabPublicationPreservesVerdictsAndCandidate(t *testing.T) {
	t.Parallel()
	for _, verdict := range []model.ProofVerdict{model.ProofVerdictPassed, model.ProofVerdictFailed, model.ProofVerdictUnverified, model.ProofVerdictInconclusive, model.ProofVerdictStale} {
		t.Run(verdict.String(), func(t *testing.T) {
			client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}}
			svc := testService(client)
			svc.auth = gitlabAuth{}
			svc.newPublisher = func(_ context.Context, provider repostore.Provider, repo string, env []string) (gitprovider.ProofCheckClient, error) {
				require.Equal(t, repostore.ProviderGitLab, provider)
				require.Equal(t, "https://gitlab.com/acme/pipelines.git", repo)
				require.Equal(t, []string{"GITLAB_TOKEN=managed-oauth"}, env)
				return client, nil
			}
			receipt := model.ProofReceipt{ID: "proof-1", AggregateVerdict: verdict, Candidate: model.ProofCandidateIdentity{RepositoryID: "42", ChangeRequestID: "7", HeadSHA: client.proposal.HeadSHA}, Claims: []model.ProofClaim{{Verdict: verdict, Description: "private claim"}}}
			svc.receipts = fakeReceiptReader{receipt}
			require.NoError(t, svc.Publish(t.Context(), receipt))
			require.Equal(t, "42", client.check.RepositoryID)
			require.Equal(t, "7", client.check.ChangeRequestID)
			require.Equal(t, client.proposal.HeadSHA, client.check.HeadSHA)
			require.Equal(t, "neutral", client.check.Conclusion)
			require.Contains(t, client.check.Summary, "Informational Proof: "+verdict.String())
			require.NotContains(t, client.check.Summary, "private claim")
		})
	}
}

func TestUnsupportedPublicationIsVisibleAndRetries(t *testing.T) {
	svc := testService(&fakeClient{})
	svc.auth = gitlabAuth{}
	svc.newPublisher = func(context.Context, repostore.Provider, string, []string) (gitprovider.ProofCheckClient, error) {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("unsupported publication")
	}
	again, err := svc.publish(t.Context(), model.ProofReceipt{})
	require.True(t, again)
	require.ErrorContains(t, err, "unsupported publication")
}

func TestPublishInvalidatesOnlyExistingCheckOnChangedHead(t *testing.T) {
	client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("b", 40)}}
	svc := testService(client)
	receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{OrganizationSlug: "acme", ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40)}}
	svc.receipts = fakeReceiptReader{receipt}
	err := svc.Publish(t.Context(), receipt)
	require.ErrorIs(t, err, gitprovider.ErrChangeStale)
	require.Equal(t, receipt.Candidate.HeadSHA, client.check.HeadSHA)
	require.True(t, client.check.ExistingOnly)
	require.Contains(t, client.check.Summary, "superseded")
}

func TestProofCheckIDSurvivesRefreshedReceiptOnly(t *testing.T) {
	receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40), PipelinePath: "pipelines/proof.yaml"}}
	first := proofCheckID(receipt)
	receipt.ID = "proof-2"
	require.Equal(t, first, proofCheckID(receipt))
	receipt.Candidate.HeadSHA = strings.Repeat("b", 40)
	require.NotEqual(t, first, proofCheckID(receipt))
}

func TestPublishReturnsProviderFailureAfterLock(t *testing.T) {
	publishErr := xerror.Unavailable(xerror.DataSource).Detail("provider failed")
	client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}, publishErr: publishErr}
	locker := &fakeLocker{}
	svc := testServiceWithLocker(client, locker)
	receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{OrganizationSlug: "acme", ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40)}}
	svc.receipts = fakeReceiptReader{receipt}
	require.ErrorIs(t, svc.Publish(t.Context(), receipt), publishErr)
	require.True(t, locker.called)
}

func TestPublicationUsesCurrentReceiptProjection(t *testing.T) {
	t.Parallel()
	for _, verdict := range []model.ProofVerdict{model.ProofVerdictStale, model.ProofVerdictSuperseded} {
		t.Run(verdict.String(), func(t *testing.T) {
			client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}}
			svc := testService(client)
			expires := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
			level := model.ProofEvidenceLevelLiveReadOnly
			original := model.ProofReceipt{ID: "proof-1", AggregateVerdict: model.ProofVerdictPassed, Candidate: model.ProofCandidateIdentity{RepositoryID: "42", HeadSHA: client.proposal.HeadSHA}}
			current := original
			current.AggregateVerdict = verdict
			current.Claims = []model.ProofClaim{{Verdict: model.ProofVerdictStale, ActualEvidenceLevel: &level, ExpiresAt: &expires}}
			svc.receipts = fakeReceiptReader{current}
			require.NoError(t, svc.Publish(t.Context(), original))
			require.Equal(t, model.ProofVerdictPassed, original.AggregateVerdict)
			if verdict == model.ProofVerdictSuperseded {
				require.Empty(t, client.check.HeadSHA, "old receipt cannot overwrite refreshed evidence")
			} else {
				require.Contains(t, client.check.Summary, "Informational Proof: stale")
				require.Contains(t, client.check.Summary, "1 stale")
				require.Contains(t, client.check.Summary, "2026-09-16T12:00:00Z")
			}
		})
	}
}

func TestRefreshDisabledStillChecksReceiptAndCanResume(t *testing.T) {
	t.Parallel()
	svc := &service{receipts: fakeReceiptReader{model.ProofReceipt{}}}
	again, err := svc.Refresh(t.Context(), "acme", "data", "staging", "proof-1")
	require.NoError(t, err)
	require.True(t, again)
	require.NoError(t, svc.Publish(t.Context(), model.ProofReceipt{}))
}

func TestClosedChangesOnlyInvalidateExistingChecks(t *testing.T) {
	t.Parallel()
	for _, proposal := range []gitprovider.ProposalRef{
		{State: gitprovider.ProposalStateOpen, Closed: true, RepositoryID: "42"},
		{State: gitprovider.ProposalStateOpen, Merged: true, RepositoryID: "42"},
		{RepositoryID: "42"},
	} {
		client := &fakeClient{proposal: proposal}
		svc := testService(client)
		receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}}
		svc.receipts = fakeReceiptReader{receipt}
		again, err := svc.Refresh(t.Context(), "acme", "data", "staging", receipt.ID)
		require.NoError(t, err)
		require.False(t, again)
		require.True(t, client.check.ExistingOnly)
		require.Contains(t, client.check.Summary, "superseded")
	}
}

func TestPermissionLossPreservesReceiptAndRetries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	svc := testService(client)
	svc.auth = deniedAuth{}
	receipt := model.ProofReceipt{ID: "proof-1", AggregateVerdict: model.ProofVerdictPassed}
	svc.receipts = fakeReceiptReader{receipt}
	again, err := svc.Refresh(t.Context(), "acme", "data", "staging", receipt.ID)
	require.ErrorIs(t, err, gitprovider.ErrChangePermissionLost)
	require.True(t, again)
	require.Empty(t, client.check.HeadSHA)
	require.Equal(t, model.ProofVerdictPassed, svc.receipts.(fakeReceiptReader).receipt.AggregateVerdict)
}

type deniedAuth struct{}

func (deniedAuth) ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
	return nil, gitprovider.ErrChangePermissionLost
}

type receiptReadFunc func() (*proofreceiptsvc.ReceiptDetail, error)

func (f receiptReadFunc) Get(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
	return f()
}

func TestRefreshStopsWhenReceiptChangesBetweenReads(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		name := "Superseded"
		if deleted {
			name = "Deleted"
		}
		t.Run(name, func(t *testing.T) {
			client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}}
			svc := testService(client)
			calls := 0
			svc.receipts = receiptReadFunc(func() (*proofreceiptsvc.ReceiptDetail, error) {
				calls++
				receipt := model.ProofReceipt{ID: "proof-1", RetainUntil: time.Now().Add(time.Hour), Candidate: model.ProofCandidateIdentity{RepositoryID: "42", HeadSHA: client.proposal.HeadSHA}}
				if calls == 2 {
					if deleted {
						return nil, proofreceipts.ErrReceiptNotFound
					}
					receipt.AggregateVerdict = model.ProofVerdictSuperseded
				}
				return &proofreceiptsvc.ReceiptDetail{Receipt: receipt}, nil
			})
			again, err := svc.Refresh(t.Context(), "acme", "data", "staging", "proof-1")
			require.NoError(t, err)
			require.False(t, again)
			require.Equal(t, 2, calls)
			require.Empty(t, client.check.HeadSHA)
		})
	}
}

type recordingPublicationQueue struct{ receipts []model.ProofReceipt }

func (q *recordingPublicationQueue) EnqueueProofReceipt(_ context.Context, _ postgres.DBTX, receipt model.ProofReceipt) error {
	q.receipts = append(q.receipts, receipt)
	return nil
}

func TestProviderRerequestRestoresMonitoringAfterPublishingWasDisabled(t *testing.T) {
	t.Parallel()
	providerErr := xerror.Unavailable(xerror.DataSource).Detail("provider unavailable")
	client := &fakeClient{proposal: gitprovider.ProposalRef{State: gitprovider.ProposalStateOpen, RepositoryID: "42", HeadSHA: strings.Repeat("a", 40)}, publishErr: providerErr}
	svc := testService(client)
	queued := &recordingPublicationQueue{}
	svc.publication = queued
	receipt := model.ProofReceipt{ID: "proof-1", RetainUntil: time.Now().Add(time.Hour), Candidate: model.ProofCandidateIdentity{RepositoryID: "42", HeadSHA: client.proposal.HeadSHA, CompilerVersion: proofreceiptsvc.LiveCompilerVersion}}
	svc.receipts = fakeReceiptReader{receipt}
	svc.enabled = false
	require.NoError(t, svc.Publish(t.Context(), receipt))
	require.Empty(t, queued.receipts)
	require.Empty(t, client.check.HeadSHA)
	svc.enabled = true
	require.ErrorIs(t, svc.Publish(t.Context(), receipt), providerErr)
	require.Equal(t, []model.ProofReceipt{receipt}, queued.receipts, "monitoring must survive provider failure")
	client.publishErr = nil
	again, err := svc.Refresh(t.Context(), "acme", "data", "staging", receipt.ID)
	require.NoError(t, err)
	require.True(t, again)
	require.Len(t, queued.receipts, 1, "the monitoring job must not enqueue itself")
	require.Equal(t, receipt.Candidate.HeadSHA, client.check.HeadSHA)
}

type fakeAuth struct{}

func (fakeAuth) ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
	return &datasourcessvc.GitDataSourceAuth{Provider: "github", RepoURL: "https://github.com/acme/pipelines.git"}, nil
}

type fakeLocker struct {
	called   bool
	disabled bool
}

func (f *fakeLocker) LockTarget(context.Context, postgres.DBTX, string) error {
	f.called = true
	return nil
}

func testService(client gitprovider.ProofCheckClient) *service {
	return testServiceWithLocker(client, &fakeLocker{})
}

func testServiceWithLocker(client gitprovider.ProofCheckClient, locker PublicationLocker) *service {
	return &service{txm: testutil.FakeTransactionManager{}, locker: locker, auth: fakeAuth{}, enabled: true, baseURL: "https://app.example.test", newPublisher: func(context.Context, repostore.Provider, string, []string) (gitprovider.ProofCheckClient, error) {
		return client, nil
	}}
}

type fakeReceiptReader struct{ receipt model.ProofReceipt }

func (f fakeReceiptReader) Get(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
	return &proofreceiptsvc.ReceiptDetail{Receipt: f.receipt}, nil
}

type fakeClient struct {
	proposal   gitprovider.ProposalRef
	check      gitprovider.ProofCheck
	publishErr error
}

func (f *fakeClient) GetProposal(context.Context, gitprovider.ProposalGetRequest) (*gitprovider.ProposalRef, error) {
	return &f.proposal, nil
}

func (f *fakeClient) PublishProofCheck(_ context.Context, check gitprovider.ProofCheck) error {
	f.check = check
	return f.publishErr
}

func (f *fakeLocker) PublicationEnabled(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (bool, error) {
	return !f.disabled, nil
}

func TestDisabledDataSourcePublicationStopsBeforeProviderAccess(t *testing.T) {
	client := &fakeClient{}
	svc := testServiceWithLocker(client, &fakeLocker{disabled: true})
	svc.auth = nil
	receipt := model.ProofReceipt{ID: "proof-1", Candidate: model.ProofCandidateIdentity{OrganizationSlug: "acme", CompilerVersion: proofreceiptsvc.LiveCompilerVersion}}
	require.NoError(t, svc.Publish(t.Context(), receipt))
	again, err := svc.publish(t.Context(), receipt)
	require.NoError(t, err)
	require.False(t, again)
	require.Empty(t, client.check.HeadSHA)
}

func TestRefreshExpiredReceiptStopsRegardlessFlagsAndProviderFailure(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			client := &fakeClient{publishErr: xerror.Unavailable(xerror.DataSource)}
			svc := testService(client)
			svc.enabled = enabled
			svc.receipts = receiptReadFunc(func() (*proofreceiptsvc.ReceiptDetail, error) { return nil, proofreceipts.ErrReceiptNotFound })
			svc.newPublisher = func(context.Context, repostore.Provider, string, []string) (gitprovider.ProofCheckClient, error) {
				t.Fatal("expired receipt reached provider")
				return client, nil
			}
			again, err := svc.Refresh(t.Context(), "acme", "data", "staging", "expired")
			require.NoError(t, err)
			require.False(t, again)
			require.Empty(t, client.check.HeadSHA, "historical provider snapshot remains untouched")
		})
	}
}
