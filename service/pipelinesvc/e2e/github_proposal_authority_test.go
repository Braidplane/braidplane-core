package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/stretchr/testify/require"
)

func TestManagedGitHubBackendProvisionsProposalAuthority(t *testing.T) {
	t.Parallel()
	pool := openE2EPool(t)
	ctx := t.Context()
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	unique := time.Now().UnixNano()
	org, member := must.Slug(fmt.Sprintf("github-authority-%d", unique)), must.Slug(fmt.Sprintf("github-member-%d", unique))
	seedE2EOrg(t, pool, org)
	seedE2EMember(t, pool, org, member)
	source := slug.Slug("github-pipelines")
	seedE2EDataSource(t, pool, org, source)
	config := []byte(`{"repository":{"id":"42","name":"pipelines","full_name":"acme/pipelines","web_url":"https://github.com/acme/pipelines"}}`)
	_, err = pool.Exec(ctx, `UPDATE data_sources ds SET type='github', config=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1 AND ds.slug='github-pipelines'`, org, config)
	require.NoError(t, err)
	var orgID int32
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM organizations WHERE slug=$1`, org).Scan(&orgID))
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM github_app_repositories WHERE organization_id=$1`, orgID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM github_app_installations WHERE organization_id=$1`, orgID)
		require.NoError(t, cleanupErr)
	})
	installationID, err := githubapps.New().UpsertInstallation(ctx, pool, githubapps.Intent{OrganizationID: orgID}, githubapps.VerifiedInstallation{
		InstallationID: unique, AccountID: 2, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected",
	})
	require.NoError(t, err)
	require.NoError(t, githubapps.New().UpsertRepository(ctx, pool, orgID, installationID, githubapps.RepositoryBinding{ID: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}))
	authority := &datasourcessvc.GitHubProposalAuthority{CredentialSlug: "github-pipelines-credential", Authority: model.PipelineProposalAuthority{
		PublicationMode: "organization_service", ProviderHost: "github.com", ProviderAccountID: "2", ProviderRepositoryID: "42", ProviderCredentialID: fmt.Sprint(unique),
		ExecutingPrincipalID: "77", ExecutingPrincipalDisplay: "braidplane[bot]", AuthoritySource: "github_app_installation",
	}}
	sourceService := &fakes.DataSourceService{
		FindBySlugFn: func(_ context.Context, gotOrg, gotSource slug.Slug) (*datasourcessvc.DataSource, error) {
			require.Equal(t, org, gotOrg)
			require.Equal(t, source, gotSource)
			return &datasourcessvc.DataSource{Slug: source, Type: model.DataSourceTypeGithub, IsActive: true, Config: config}, nil
		},
		ResolveGitExecutionAuthFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitExecutionAuth, error) {
			return &datasourcessvc.GitExecutionAuth{DataSourceType: model.DataSourceTypeGithub, ConfigJSON: []byte(`{"repo_url":"https://github.com/acme/pipelines.git"}`), GitHubProposalAuthority: authority}, nil
		},
	}
	pipelineJSON := []byte(`{"metadata":{"slug":"synthetic","name":"Synthetic"},"steps":[{"slug":"payload","kind":"input","input":{"output_contract":{"name":"synthetic-payload","representation":"json","root_shape":"object","fields":[{"name":"message","type":"string","required":true}]}}},{"slug":"copy","kind":"transform","transform":{"inputs":{"input":"payload"},"sql":"SELECT message FROM input","output_kind":"json","input_contract":{"name":"synthetic-payload","representation":"json","root_shape":"object","fields":[{"name":"message","type":"string","required":true}]},"output_contract":{"name":"copied-payload","representation":"json","root_shape":"array","fields":[{"name":"message","type":"string","required":true}]}}}]}`)
	snapshot := &githubBackendSnapshot{files: map[string][]byte{"pipelines/synthetic.pipeline.json": pipelineJSON}}
	projectsService := projectssvc.New(txm, projects.NewRepository(), nil, nil, sourceService, store, nil, githubBackendManager{}, nil,
		projectssvc.WithRemotePipelineBackendExecution(snapshot, 1024*1024))
	params := model.SetProjectPipelineBackend{DataSourceSlug: source, TrackedBranch: "main", PipelineDir: "pipelines", Mode: model.SetProjectPipelineBackendModeAdoptExisting}
	_, err = projectsService.SetProjectPipelineBackend(ctx, org, defaultProjectSlug, params)
	require.NoError(t, err)
	bound, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.Equal(t, authority.Authority.ProviderCredentialID, bound.ProviderCredentialID)
	require.Equal(t, "77", bound.ExecutingPrincipalID)
	require.Equal(t, "42", bound.ProviderRepositoryID)
	require.Equal(t, "braidplane[bot]", bound.ExecutingPrincipalDisplay)

	// Reattaching another publisher mode to an unchanged backend does not revoke its grant.
	for _, mode := range []string{"delegated_user", "organization_service"} {
		_, err = pool.Exec(ctx, `UPDATE pipeline_proposal_authorities SET publication_mode=$2,authority_source='other_provider_connection' WHERE organization_id=$1`, orgID, mode)
		require.NoError(t, err)
		require.NoError(t, projects.NewRepository().RevokeChangedProposalAuthority(ctx, pool, projects.BindProposalAuthorityParams{
			OrgSlug: org, ProjectSlug: defaultProjectSlug, DataSourceSlug: source,
		}))
		var active bool
		require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at IS NULL FROM pipeline_proposal_authorities WHERE id=$1`, bound.ID).Scan(&active))
		require.True(t, active)
	}
	_, err = pool.Exec(ctx, `UPDATE pipeline_proposal_authorities SET publication_mode='organization_service',authority_source='github_app_installation' WHERE organization_id=$1`, orgID)
	require.NoError(t, err)
	// An unchanged attachment must not invalidate queued attempts.
	_, err = projectsService.SetProjectPipelineBackend(ctx, org, defaultProjectSlug, params)
	require.NoError(t, err)
	same, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.Equal(t, bound.ID, same.ID)

	// A rejected import rolls back the authority replacement along with the backend and specs.
	authority.Authority.ExecutingPrincipalID = "88"
	snapshot.files["pipelines/synthetic.pipeline.json"] = []byte(`{"metadata":{}}`)
	_, err = projectsService.SetProjectPipelineBackend(ctx, org, defaultProjectSlug, params)
	require.ErrorIs(t, err, projectssvc.ErrPipelineBackendReconcileFailed)
	unchanged, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
	require.NoError(t, err)
	require.Equal(t, bound, unchanged)
	authority.Authority.ExecutingPrincipalID = "77"
	snapshot.files["pipelines/synthetic.pipeline.json"] = pipelineJSON

	producer := &recordingPublishPipelineGitProducer{}
	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(), store, pool, txm, pipelineruns.New(), incrementalcheckpoints.New(), pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), driftimpactrepo.New(),
		noopQueryExecutor{}, allowAuthorizationService{}, &fakes.AdmissionService{}, sourceService, projectsService, &ownershipGitSourceResolver{}, clock.New(),
		noopPipelineRunProducer{}, producer, eventtest.NewProducer(nil), noopPipelineRuntimeResolver{}, testSinkEffectLedger(t, txm), newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	canonical, err := svc.Get(ctx, org, defaultProjectSlug, "synthetic", member)
	require.NoError(t, err)
	var draft pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(pipelineJSON, &draft))
	draft.Metadata.Name = "Draft"
	_, err = svc.SaveDraft(ctx, org, defaultProjectSlug, "synthetic", member, model.UserEventActor(member), &draft, canonical.Summary.CurrentVersionSpecHash)
	require.NoError(t, err)
	_, err = svc.PublishDraft(ctx, org, defaultProjectSlug, "synthetic", member, model.APIKeyEventActor("machine"), canonical.Summary.CurrentVersionSpecHash, false)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineProposalAuthorityDenied)
	_, err = svc.PublishDraft(ctx, org, defaultProjectSlug, "synthetic", member, model.UserEventActor(member), canonical.Summary.CurrentVersionSpecHash, false)
	require.NoError(t, err)
	require.Equal(t, 1, producer.publishCalls)
	queued := producer.publishArgs
	require.Equal(t, bound.Payload(), queued.Authority)
	require.NoError(t, svc.AuthorizeProposalPublish(ctx, queued))

	// Resolution rejects lifecycle and source mutations even before an executor can mint a token.
	for _, tc := range []struct{ name, change, restore string }{
		{"ReadOnly", `UPDATE data_sources SET read_only=true WHERE organization_id=$1`, `UPDATE data_sources SET read_only=false WHERE organization_id=$1`},
		{"Inactive", `UPDATE data_sources SET is_active=false WHERE organization_id=$1`, `UPDATE data_sources SET is_active=true WHERE organization_id=$1`},
		{"Suspended", `UPDATE github_app_installations SET suspended_at=now() WHERE organization_id=$1`, `UPDATE github_app_installations SET suspended_at=NULL WHERE organization_id=$1`},
		{"Uninstalled", `UPDATE github_app_installations SET deleted_at=now() WHERE organization_id=$1`, `UPDATE github_app_installations SET deleted_at=NULL WHERE organization_id=$1`},
		{"Deselected", `UPDATE github_app_repositories SET removed_at=now() WHERE organization_id=$1`, `UPDATE github_app_repositories SET removed_at=NULL WHERE organization_id=$1`},
		{"RevokedCredential", `UPDATE credentials SET status='revoked' WHERE organization_id=$1`, `UPDATE credentials SET status='active' WHERE organization_id=$1`},
		{"ChangedInstallation", `UPDATE github_app_installations SET installation_id=installation_id+1 WHERE organization_id=$1`, `UPDATE github_app_installations SET installation_id=installation_id-1 WHERE organization_id=$1`},
		{"ChangedAccount", `UPDATE github_app_installations SET account_id=3 WHERE organization_id=$1`, `UPDATE github_app_installations SET account_id=2 WHERE organization_id=$1`},
		{"ChangedRepository", `UPDATE data_sources SET config=jsonb_set(config,'{repository,id}','"43"') WHERE organization_id=$1`, `UPDATE data_sources SET config=jsonb_set(config,'{repository,id}','"42"') WHERE organization_id=$1`},
		{"RevokedAuthority", `UPDATE pipeline_proposal_authorities SET revoked_at=now() WHERE organization_id=$1`, `UPDATE pipeline_proposal_authorities SET revoked_at=NULL WHERE organization_id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.change, orgID)
			require.NoError(t, err)
			_, err = store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
			require.ErrorIs(t, err, pipelinesvc.ErrPipelineProposalAuthorityDenied)
			require.ErrorIs(t, svc.AuthorizeProposalPublish(ctx, queued), pipelinesvc.ErrPipelineProposalAuthorityDenied)
			_, err = pool.Exec(ctx, tc.restore, orgID)
			require.NoError(t, err)
		})
	}
	require.NoError(t, svc.AuthorizeProposalPublish(ctx, queued))
	require.NoError(t, store.CompleteProposal(ctx, pipelinegit.ProposalCompletion{
		OrganizationSlug: org, ProjectSlug: defaultProjectSlug, PipelineSlug: "synthetic", MemberSlug: member,
		Provider: "github", ProposalID: "12", ProposalURL: "https://github.com/acme/pipelines/pull/12", ProposalState: "open",
		BranchName: queued.BranchName, BranchHeadSHA: "def456", DraftSpecHash: queued.DraftSpecHash, PublishAttemptID: queued.PublishAttemptID, Authority: queued.Authority,
	}))
	published, err := svc.Get(ctx, org, defaultProjectSlug, "synthetic", member)
	require.NoError(t, err)
	require.Equal(t, "12", *published.Draft.ProposalID)
	require.Equal(t, "braidplane[bot]", *published.Draft.ProviderActor)
	require.Equal(t, canonical.Summary.CurrentVersionSpecHash, published.Summary.CurrentVersionSpecHash)

	// Cross-tenant selectors cannot bind this project's source or installation.
	otherOrg := must.Slug(fmt.Sprintf("other-authority-%d", unique))
	seedE2EOrg(t, pool, otherOrg)
	seedE2EDataSource(t, pool, otherOrg, source)
	_, err = pool.Exec(ctx, `UPDATE data_sources ds SET type='github',config=$2 FROM organizations o WHERE ds.organization_id=o.id AND o.slug=$1 AND ds.slug='github-pipelines'`, otherOrg, config)
	require.NoError(t, err)
	require.NoError(t, projects.NewRepository().SetPipelineBackend(ctx, pool, &projects.SetPipelineBackendParams{
		OrgSlug: otherOrg, Slug: defaultProjectSlug, PipelineBackendDataSourceSlug: source,
		PipelineBackendRepo: "github.com/acme/pipelines", PipelineBackendTrackedBranch: "main", PipelineBackendPipelineDir: "pipelines",
	}))
	require.ErrorIs(t, projects.NewRepository().BindGitHubProposalAuthority(ctx, pool, projects.BindProposalAuthorityParams{
		OrgSlug: otherOrg, ProjectSlug: defaultProjectSlug, DataSourceSlug: source, CredentialSlug: authority.CredentialSlug, Authority: authority.Authority,
	}), projects.ErrPipelineBackendDataSourceUnavailable)

	_, err = svc.PublishDraft(ctx, org, defaultProjectSlug, "synthetic", member, model.UserEventActor(member), canonical.Summary.CurrentVersionSpecHash, false)
	require.NoError(t, err)
	queued = producer.publishArgs
	require.NoError(t, svc.AuthorizeProposalPublish(ctx, queued))

	// A credential replacement followed by attachment creates a new authority; old queued work stays denied after switching back.
	_, err = pool.Exec(ctx, `INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) VALUES($1,'replacement','static_secret','github','active',''::bytea)`, orgID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET credential_id=(SELECT id FROM credentials WHERE organization_id=$1 AND slug='replacement') WHERE organization_id=$1`, orgID)
	require.NoError(t, err)
	require.ErrorIs(t, svc.AuthorizeProposalPublish(ctx, queued), pipelinesvc.ErrPipelineProposalAuthorityDenied)
	for _, credential := range []slug.Slug{"replacement", "github-pipelines-credential"} {
		_, err = pool.Exec(ctx, `UPDATE data_sources SET credential_id=(SELECT id FROM credentials WHERE organization_id=$1 AND slug=$2) WHERE organization_id=$1`, orgID, credential)
		require.NoError(t, err)
		authority.CredentialSlug = credential
		_, err = projectsService.SetProjectPipelineBackend(ctx, org, defaultProjectSlug, params)
		require.NoError(t, err)
		rebound, err := store.ResolveProposalAuthority(ctx, org, defaultProjectSlug)
		require.NoError(t, err)
		require.NotEqual(t, bound.ID, rebound.ID)
		require.ErrorIs(t, svc.AuthorizeProposalPublish(ctx, queued), pipelinesvc.ErrPipelineProposalAuthorityDenied)
		_, err = svc.PublishDraft(ctx, org, defaultProjectSlug, "synthetic", member, model.UserEventActor(member), canonical.Summary.CurrentVersionSpecHash, false)
		require.ErrorIs(t, err, pipelinesvc.ErrPipelineProposalAuthorityDenied)
	}
}

type githubBackendSnapshot struct{ files map[string][]byte }

func (s *githubBackendSnapshot) ExecuteGitSourceSnapshot(context.Context, pipelinegit.RemoteSourceRequest) (*pipelinegit.RemoteSourceSnapshot, error) {
	repo, commit := "https://github.com/acme/pipelines.git", "abc123"
	return &pipelinegit.RemoteSourceSnapshot{Files: s.files, SourceRepo: &repo, LastAppliedCommit: &commit}, nil
}

type githubBackendManager struct{}

func (githubBackendManager) ValidateRepository(context.Context, slug.Slug, slug.Slug, string) error {
	return nil
}

func (githubBackendManager) BootstrapRepository(context.Context, pipelinegit.RepositoryBootstrapOperation) (*pipelinegit.RepositoryBootstrapResult, error) {
	panic("unexpected bootstrap")
}

func (githubBackendManager) DeleteRepository(context.Context, pipelinegit.RepositoryBootstrapOperation) error {
	panic("unexpected delete")
}
