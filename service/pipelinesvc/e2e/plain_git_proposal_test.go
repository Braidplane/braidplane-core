package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestPlainGitAuthorityAndPublishedDraftPersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL fixture")
	}
	ctx := t.Context()
	pool := openE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	repository := pipelinesrepo.New()
	store, err := pipelinesvc.NewPipelineStore(pool, txm, repository, odcscontracts.NewRepository())
	require.NoError(t, err)
	suffix := time.Now().UnixNano()
	org, member := must.Slug(fmt.Sprintf("plain-git-%d", suffix)), must.Slug(fmt.Sprintf("plain-member-%d", suffix))
	seedE2EOrg(t, pool, org)
	seedE2EMember(t, pool, org, member)
	source := slug.Slug("plain-git")
	seedE2EDataSource(t, pool, org, source)
	config := []byte(`{"repo_url":"ssh://git@git.example.test/acme/pipelines.git","default_branch":"main"}`)
	_, err = pool.Exec(ctx, `UPDATE data_sources ds SET config=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1`, org, config)
	require.NoError(t, err)
	projectRepo := projects.NewRepository()
	backend := &projects.SetPipelineBackendParams{OrgSlug: org, Slug: defaultProjectSlug, PipelineBackendDataSourceSlug: source, PipelineBackendRepo: repostore.NormalizeRemoteURL("ssh://git@git.example.test/acme/pipelines.git"), PipelineBackendTrackedBranch: "main", PipelineBackendPipelineDir: "pipelines"}
	require.NoError(t, projectRepo.SetPipelineBackend(ctx, pool, backend))
	binding := projects.BindProposalAuthorityParams{OrgSlug: org, ProjectSlug: defaultProjectSlug, DataSourceSlug: source}
	require.NoError(t, projectRepo.BindGitBranchProposalAuthority(ctx, pool, binding))
	bound, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.Equal(t, model.PipelineProposalModeRepositoryCredential, bound.PublicationMode)
	require.Empty(t, bound.ProviderAccountID)
	require.Empty(t, bound.ProviderRepositoryID)
	require.Empty(t, bound.ExecutingPrincipalID)
	ds, err := datasources.New().FindBySlug(ctx, pool, org, source)
	require.NoError(t, err)
	require.Equal(t, model.GitConfigurationHash(ds.Config), bound.GitConfigurationHash)
	require.Equal(t, ds.Credential.UpdatedAt.UTC().Format(time.RFC3339Nano), bound.GitCredentialRevision)
	require.NoError(t, projectRepo.RevokeChangedProposalAuthority(ctx, pool, binding))
	require.NoError(t, projectRepo.BindGitBranchProposalAuthority(ctx, pool, binding))
	same, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.Equal(t, bound, same)
	for _, field := range []string{"read_only", "is_active"} {
		denied, restored := true, false
		if field == "is_active" {
			denied, restored = false, true
		}
		_, err = pool.Exec(ctx, fmt.Sprintf(`UPDATE data_sources ds SET %s=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1`, field), org, denied)
		require.NoError(t, err)
		_, err = store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
		require.ErrorIs(t, err, pipelinesvc.ErrPipelineProposalAuthorityDenied)
		_, err = pool.Exec(ctx, fmt.Sprintf(`UPDATE data_sources ds SET %s=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1`, field), org, restored)
		require.NoError(t, err)
	}
	_, err = pool.Exec(ctx, `UPDATE data_sources ds SET config=jsonb_set(config,'{repo_url}','"ssh://other@git.example.test/acme/pipelines.git"') FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1`, org)
	require.NoError(t, err)
	changed, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.NotEqual(t, bound.Payload(), changed.Payload())
	_, err = pool.Exec(ctx, `UPDATE data_sources ds SET config=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1`, org, config)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE credentials c SET updated_at=c.updated_at+interval '1 second' FROM organizations o WHERE c.organization_id=o.id AND o.slug=$1`, org)
	require.NoError(t, err)
	changed, err = store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.NotEqual(t, bound.GitCredentialRevision, changed.GitCredentialRevision)
	_, err = store.ResolveProposalAuthority(ctx, "other-org", defaultProjectSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineProposalAuthorityDenied)
	pipeline := ownershipPipeline("plain-proposal")
	data, err := json.Marshal(pipeline)
	require.NoError(t, err)
	path, base, commit := "pipelines/plain-proposal.pipeline.json", "refs/heads/main", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := store.ReconcileFromGit(ctx, org, defaultProjectSlug, plainGitProposalRepo{ownershipRepoStore{existingPath: path, data: data}}, pipelinesvc.ReconcileFromGitParams{PipelineDir: "pipelines", SourceDataSourceSlug: &source, SourceRepo: &backend.PipelineBackendRepo, SourceRef: &base, LastAppliedCommit: &commit})
	require.NoError(t, err)
	require.Empty(t, result.Failures)
	require.Equal(t, 1, result.Scanned)
	canonical, err := store.FindSpec(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	pipeline.Metadata.Name = "Draft proposal"
	require.NoError(t, store.SaveDraft(ctx, org, defaultProjectSlug, member, pipeline, canonical.CurrentVersionSpecHash))
	queued, err := store.PrepareGitDraftPublish(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug, member, &base, canonical.CurrentVersionSpecHash, false)
	require.NoError(t, err)
	require.NoError(t, store.CompleteProposal(ctx, pipelinegit.ProposalCompletion{OrganizationSlug: org, ProjectSlug: defaultProjectSlug, PipelineSlug: pipeline.Metadata.Slug, MemberSlug: member, Provider: "git", BranchName: queued.BranchName, BranchHeadSHA: commit, DraftSpecHash: queued.DraftSpecHash, PublishAttemptID: queued.PublishAttemptID, Authority: queued.Authority.Payload()}))
	require.NoError(t, store.FailProposal(ctx, pipelinegit.ProposalFailure{OrganizationSlug: org, ProjectSlug: defaultProjectSlug, PipelineSlug: pipeline.Metadata.Slug, MemberSlug: member, PublishAttemptID: queued.PublishAttemptID, FailureCode: "late-failure"}))
	draft, err := store.FindDraft(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug, member)
	require.NoError(t, err)
	require.Equal(t, "branch_pushed", *draft.DraftState)
	require.Nil(t, draft.ProposalID)
	require.Nil(t, draft.ProposalURL)
	require.Nil(t, draft.ProposalState)
	after, err := store.FindSpec(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.Equal(t, canonical.CurrentVersionSpecHash, after.CurrentVersionSpecHash)
}

type plainGitProposalRepo struct{ ownershipRepoStore }

func (s plainGitProposalRepo) ListFiles(context.Context, string) ([]repostore.FileEntry, error) {
	return []repostore.FileEntry{{Path: s.existingPath}}, nil
}
