package pipelinegit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestProposalExecutorPublishesBranchAndProviderProposal(t *testing.T) {
	t.Parallel()

	authority := model.PipelineProposalAuthority{ID: 1, PublicationMode: "organization_service", ProviderHost: "github.com", ProviderAccountID: "2", ProviderRepositoryID: "42", ExecutingPrincipalID: "77", ExecutingPrincipalDisplay: "braidplane[bot]", AuthoritySource: "github_app_installation"}
	provider := &recordingProposalProvider{
		result: ProposalResult{
			Provider:      "github",
			ProposalID:    "42",
			ProposalURL:   "https://github.com/acme/pipelines/pull/42",
			ProposalState: "open",
			BranchName:    "braidplane/default/daily-sync/member-a",
			BranchHeadSHA: "def456",
		},
	}
	repo := &recordingOperationRepoStore{
		readErr: repostore.ErrFileNotFound.Detail("pipelines/daily-sync.pipeline.yaml"),
	}
	executor := &proposalExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("pipelines-git"), dsSlug)
				return &datasourcessvc.GitDataSourceAuth{
					Provider:                "github",
					RepoURL:                 "https://github.com/acme/pipelines.git",
					DefaultBranch:           "main",
					GitHubProposalAuthority: &datasourcessvc.GitHubProposalAuthority{Authority: authority},
				}, nil
			},
		},
		remoteGit: &recordingRemoteGit{
			repoStore: repo,
			repoRoot:  "/tmp/pipelines-repo",
			cloneErrByBranch: map[string]error{
				"braidplane/default/daily-sync/member-a": repostore.ErrFileNotFound,
			},
		},
		provider: provider,
	}
	baseRef := "refs/heads/main"
	baseCommitSHA := "abc123"

	result, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		BaseRef:          &baseRef,
		BaseCommitSHA:    &baseCommitSHA,
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "propose pipeline daily-sync",
		ProposalTitle:    "Propose pipeline daily-sync",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		Authority:        authority.Payload(),
	})

	require.NoError(t, err)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.writtenPath)
	require.Equal(t, repostore.PushOptions{
		Refspec: "HEAD:refs/heads/braidplane/default/daily-sync/member-a",
	}, executor.remoteGit.(*recordingRemoteGit).pushedOptions)
	require.Equal(t, ProviderProposalRequest{
		BaseRef:       "refs/heads/main",
		BaseCommitSHA: "abc123",
		BranchName:    "braidplane/default/daily-sync/member-a",
		Title:         "Propose pipeline daily-sync",
		Body:          "Pipeline draft proposed by Braidplane.",
	}, provider.request)
	require.Equal(t, &provider.result, result)
}

func TestProposalExecutorRejectsRepositoryOutsideBoundAuthority(t *testing.T) {
	t.Parallel()

	executor := newTestProposalExecutor(&recordingProposalProvider{}, &recordingOperationRepoStore{})
	authority := testProposalAuthority("github.com")
	authority.ProviderRepositoryID = "other/repository"

	_, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("new"),
		Authority:        authority,
	})

	require.ErrorIs(t, err, ErrProposalAuthorityInvalid)
	require.Zero(t, executor.remoteGit.(*recordingRemoteGit).pushCalls)
}

func TestProposalExecutorReusesExistingProposalOnRepeatPublish(t *testing.T) {
	t.Parallel()

	provider := &recordingProposalProvider{
		existing: &ProposalResult{
			Provider:      "gitlab",
			ProposalID:    "7",
			ProposalURL:   "https://gitlab.com/acme/pipelines/-/merge_requests/7",
			ProposalState: "open",
			BranchName:    "braidplane/default/daily-sync/member-a",
			BranchHeadSHA: "oldsha",
		},
		result: ProposalResult{
			Provider:      "gitlab",
			ProposalID:    "7",
			ProposalURL:   "https://gitlab.com/acme/pipelines/-/merge_requests/7",
			ProposalState: "open",
			BranchName:    "braidplane/default/daily-sync/member-a",
			BranchHeadSHA: "newsha",
		},
	}
	repo := &recordingOperationRepoStore{
		readContent: []byte("metadata:\n  slug: daily-sync\n  name: Old\n"),
	}
	executor := newProposalExecutorForTest(provider, repo)

	result, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n  name: New\n"),
		CommitMessage:    "propose pipeline daily-sync",
		ProposalTitle:    "Propose pipeline daily-sync",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		Authority:        testProposalAuthority("gitlab.com"),
	})

	require.NoError(t, err)
	require.Equal(t, 1, provider.upsertCalls)
	require.Equal(t, "7", result.ProposalID)
	require.Equal(t, "newsha", result.BranchHeadSHA)
}

func TestCreationProposalCannotOverwriteExistingFileButCanRepeatExactBranch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"base-file-exists", "branch-file-changed", "exact-retry", "new-file"} {
		t.Run(scenario, func(t *testing.T) {
			provider := &recordingProposalProvider{result: ProposalResult{Provider: "gitlab", ProposalID: "42", ProposalState: "open"}}
			repo := &recordingOperationRepoStore{readContent: []byte("same")}
			executor := newProposalExecutorForTest(provider, repo)
			remote := executor.remoteGit.(*recordingRemoteGit)
			if scenario == "base-file-exists" {
				remote.cloneErrByBranch = map[string]error{"creation-branch": repostore.ErrFileNotFound}
			}
			if scenario == "branch-file-changed" {
				repo.readContent = []byte("changed outside Braidplane")
			}
			if scenario == "new-file" {
				repo.readContent = nil
				repo.readErr = repostore.ErrFileNotFound
			}
			_, err := executor.PublishProposal(t.Context(), ProposalOperation{CreateOnly: true, OrganizationSlug: "acme", DataSourceSlug: "git-source", PipelineSlug: "first", BranchName: "creation-branch", Path: "pipelines/first.pipeline.yaml", Content: []byte("same"), Authority: testProposalAuthority("gitlab.com")})
			if scenario == "base-file-exists" || scenario == "branch-file-changed" {
				require.ErrorContains(t, err, "cannot replace an existing repository file")
				require.Zero(t, remote.pushCalls)
				require.Zero(t, provider.upsertCalls)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, provider.upsertCalls)
				if scenario == "exact-retry" {
					require.Zero(t, remote.pushCalls)
				}
			}
		})
	}
}

func TestProposalExecutorRecordsPushedHeadWhenProviderResponseLags(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"old", "new"} {
		t.Run(content, func(t *testing.T) {
			provider := &recordingProposalProvider{result: ProposalResult{
				ProposalID: "42", BranchHeadSHA: "stale-provider-head",
			}}
			repo := &recordingOperationRepoStore{readContent: []byte("old")}
			executor := newTestProposalExecutor(provider, repo)
			executor.resolveHead = func(context.Context, string) *string {
				if repo.writtenPath != "" {
					return ptrString("pushed-head")
				}
				return ptrString("existing-head")
			}

			result, err := executor.PublishProposal(t.Context(), ProposalOperation{
				OrganizationSlug: "acme", DataSourceSlug: "pipelines-git",
				BaseRef: ptrString("main"), ExpectedHeadSHA: ptrString("existing-head"),
				BranchName: "braidplane/default/daily-sync/member-a", Path: "pipelines/daily-sync.pipeline.yaml",
				Content: []byte(content), Authority: testProposalAuthority("github.com"),
			})

			require.NoError(t, err)
			if content == "new" {
				require.Equal(t, 1, executor.remoteGit.(*recordingRemoteGit).pushCalls)
				require.Equal(t, "pushed-head", result.BranchHeadSHA)
			} else {
				require.Zero(t, executor.remoteGit.(*recordingRemoteGit).pushCalls)
				require.Equal(t, "existing-head", result.BranchHeadSHA)
			}
		})
	}
}

func TestProposalExecutorRejectsChangedProposalBranchBeforeWrite(t *testing.T) {
	t.Parallel()

	provider := &recordingProposalProvider{}
	repo := &recordingOperationRepoStore{readContent: []byte("old")}
	executor := newTestProposalExecutor(provider, repo)
	executor.resolveHead = func(context.Context, string) *string { return ptrString("new-head") }
	expectedHead := "remembered-head"

	_, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"), DataSourceSlug: slug.Slug("pipelines-git"),
		BaseRef: ptrString("main"), ExpectedHeadSHA: &expectedHead,
		BranchName: "braidplane/default/daily-sync/member-a", Path: "pipelines/daily-sync.pipeline.yaml",
		Content:   []byte("new"),
		Authority: testProposalAuthority("github.com"),
	})

	require.ErrorIs(t, err, ErrProposalBranchHeadConflict)
	require.Empty(t, repo.writtenPath)
	require.Zero(t, provider.upsertCalls)
	require.Zero(t, executor.remoteGit.(*recordingRemoteGit).pushCalls)
}

func TestProposalExecutorRejectsUnrememberedExistingBranchThatAdvancedFromBase(t *testing.T) {
	t.Parallel()

	provider := &recordingProposalProvider{}
	repo := &recordingOperationRepoStore{readContent: []byte("old")}
	executor := newTestProposalExecutor(provider, repo)
	executor.resolveHead = func(context.Context, string) *string { return ptrString("existing-branch-head") }
	baseHead := "base-head"

	_, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"), DataSourceSlug: slug.Slug("pipelines-git"),
		BaseRef: ptrString("main"), BaseCommitSHA: &baseHead,
		BranchName: "braidplane/default/daily-sync/member-a", Path: "pipelines/daily-sync.pipeline.yaml",
		Content:   []byte("new"),
		Authority: testProposalAuthority("github.com"),
	})

	require.ErrorIs(t, err, ErrProposalBranchHeadConflict)
	require.Empty(t, repo.writtenPath)
	require.Zero(t, provider.upsertCalls)
	require.Zero(t, executor.remoteGit.(*recordingRemoteGit).pushCalls)
}

func TestProposalExecutorAllowsIdempotentRecoveryOfUnrememberedExistingBranch(t *testing.T) {
	t.Parallel()

	content := []byte("already proposed")
	provider := &recordingProposalProvider{result: ProposalResult{ProposalID: "42"}}
	repo := &recordingOperationRepoStore{readContent: content}
	executor := newTestProposalExecutor(provider, repo)
	executor.resolveHead = func(context.Context, string) *string { return ptrString("existing-branch-head") }
	baseHead := "base-head"

	result, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"), DataSourceSlug: slug.Slug("pipelines-git"),
		BaseRef: ptrString("main"), BaseCommitSHA: &baseHead,
		BranchName: "braidplane/default/daily-sync/member-a", Path: "pipelines/daily-sync.pipeline.yaml",
		Content:   content,
		Authority: testProposalAuthority("github.com"),
	})

	require.NoError(t, err)
	require.Equal(t, "42", result.ProposalID)
	require.Empty(t, repo.writtenPath)
	require.Equal(t, 1, provider.upsertCalls)
	require.Zero(t, executor.remoteGit.(*recordingRemoteGit).pushCalls)
}

func TestProposalExecutorRefreshesExactProposal(t *testing.T) {
	t.Parallel()

	provider := &recordingProposalProvider{result: ProposalResult{
		Provider: "github", ProposalID: "42", ProposalState: "merged", BranchHeadSHA: "head-42",
	}}
	executor := newTestProposalExecutor(provider, &recordingOperationRepoStore{})

	result, err := executor.RefreshProposal(context.Background(), ProposalRefreshOperation{
		OrganizationSlug: slug.Slug("acme"), DataSourceSlug: slug.Slug("pipelines-git"), ProposalID: "42",
	})

	require.NoError(t, err)
	require.Equal(t, "42", provider.getID)
	require.Equal(t, "merged", result.ProposalState)
}

func TestProposalExecutorRedactsCredentialValuesFromProviderFailures(t *testing.T) {
	t.Parallel()

	providerErr := xerror.ExecutionFailed().Detail("provider rejected token ghp_secret")
	executor := newProposalExecutorForTest(
		&recordingProposalProvider{err: providerErr},
		&recordingOperationRepoStore{},
	)

	_, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "propose pipeline daily-sync",
		ProposalTitle:    "Propose pipeline daily-sync",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		Authority:        testProposalAuthority("gitlab.com"),
	})

	require.ErrorIs(t, err, providerErr)
	require.NotContains(t, err.Error(), "ghp_secret")
	require.Contains(t, err.Error(), "provider rejected token")
}

func TestProposalExecutorRejectsGenericGitProposal(t *testing.T) {
	t.Parallel()

	executor := &proposalExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					Provider:      "git",
					RepoURL:       "https://git.example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: &recordingRemoteGit{},
	}

	_, err := executor.PublishProposal(context.Background(), ProposalOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		BranchName:       "braidplane/default/daily-sync/member-a",
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "propose pipeline daily-sync",
		ProposalTitle:    "Propose pipeline daily-sync",
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		Authority:        testProposalAuthority("git.example.com"),
	})

	require.ErrorIs(t, err, ErrProviderProposalUnsupported)
	require.ErrorContains(t, err, "generic git remotes can push branches but cannot create pull or merge requests")
}

func testProposalAuthority(host string) model.PipelineProposalAuthorityPayload {
	return model.PipelineProposalAuthorityPayload{
		ID: 1, PublicationMode: model.PipelineProposalModeDelegatedUser,
		ProviderHost: host, ProviderAccountID: "account-1", ProviderRepositoryID: "acme/pipelines",
		ExecutingPrincipalID:      "user-1",
		ExecutingPrincipalDisplay: "acme-user", AuthoritySource: "delegated grant",
	}
}

func newProposalExecutorForTest(
	provider *recordingProposalProvider,
	repo *recordingOperationRepoStore,
) *proposalExecutor {
	return &proposalExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					Provider:      "gitlab",
					RepoURL:       "https://oauth2:ghp_secret@gitlab.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: &recordingRemoteGit{
			repoStore: repo,
			repoRoot:  "/tmp/pipelines-repo",
		},
		provider: provider,
	}
}

type recordingProposalProvider struct {
	existing *ProposalResult
	result   ProposalResult
	err      error

	upsertCalls int
	request     ProviderProposalRequest
	getID       string
}

func newTestProposalExecutor(
	provider proposalProvider,
	repo repostore.RepoStore,
) *proposalExecutor {
	return &proposalExecutor{
		gitAuthResolver: fakeGitAuthResolver{resolveFn: func(
			context.Context, slug.Slug, slug.Slug,
		) (*datasourcessvc.GitDataSourceAuth, error) {
			return &datasourcessvc.GitDataSourceAuth{
				Provider: "github", RepoURL: "https://github.com/acme/pipelines.git", DefaultBranch: "main",
			}, nil
		}},
		remoteGit: &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"},
		provider:  provider,
	}
}

func (p *recordingProposalProvider) UpsertProposal(
	_ context.Context,
	req ProviderProposalRequest,
) (*ProposalResult, error) {
	p.upsertCalls++
	p.request = req
	if p.err != nil {
		return nil, p.err
	}
	return &p.result, nil
}

func (p *recordingProposalProvider) GetProposal(
	_ context.Context,
	proposalID string,
) (*ProposalResult, error) {
	p.getID = proposalID
	if p.err != nil {
		return nil, p.err
	}
	result := p.result
	return &result, nil
}

func TestProposalExecutorRevalidatesGitHubAppIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*datasourcessvc.GitHubProposalAuthority)
	}{
		{"Account", func(a *datasourcessvc.GitHubProposalAuthority) { a.Authority.ProviderAccountID = "other" }},
		{"Repository", func(a *datasourcessvc.GitHubProposalAuthority) { a.Authority.ProviderRepositoryID = "43" }},
		{"Principal", func(a *datasourcessvc.GitHubProposalAuthority) { a.Authority.ExecutingPrincipalID = "88" }},
		{"Missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expected := model.PipelineProposalAuthority{ID: 1, PublicationMode: "organization_service", ProviderHost: "github.com", ProviderAccountID: "2", ProviderRepositoryID: "42", ExecutingPrincipalID: "77", ExecutingPrincipalDisplay: "braidplane[bot]", AuthoritySource: "github_app_installation"}
			current := &datasourcessvc.GitHubProposalAuthority{Authority: expected}
			if tc.change != nil {
				tc.change(current)
			} else {
				current = nil
			}
			provider := &recordingProposalProvider{}
			executor := &proposalExecutor{gitAuthResolver: fakeGitAuthResolver{resolveFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{Provider: "github", RepoURL: "https://github.com/acme/pipelines.git", GitHubProposalAuthority: current}, nil
			}}, provider: provider}
			_, err := executor.PublishProposal(t.Context(), ProposalOperation{Authority: expected.Payload()})
			require.ErrorIs(t, err, ErrProposalAuthorityInvalid)
			require.Equal(t, ProviderProposalRequest{}, provider.request)
		})
	}
}

func TestPlainGitProposalPublishesOnlyTheBoundBranch(t *testing.T) {
	for _, scenario := range []string{"publish", "repeat", "repository changed", "credential changed", "read only", "provider changed", "tracked branch", "base changed", "source changed", "configuration changed", "target exists", "push denied"} {
		t.Run(scenario, func(t *testing.T) {
			head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			base := "refs/heads/main"
			authority := model.PipelineProposalAuthorityPayload{ID: 7, PublicationMode: model.PipelineProposalModeRepositoryCredential, ExecutingPrincipalDisplay: "Configured Git connection", AuthoritySource: "git_data_source_credential", GitConfigurationHash: "configuration-one", GitRepository: "ssh://git.example.test/acme/pipelines.git", GitBaseRef: base, GitDataSourceSlug: "pipelines-git", GitCredentialSlug: "ssh-key", GitCredentialRevision: "revision-one"}
			auth := datasourcessvc.GitDataSourceAuth{Provider: "git", GitConfigurationHash: "configuration-one", GitWritable: true, RepoURL: "ssh://git@git.example.test/acme/pipelines.git", DefaultBranch: "main", GitDataSourceSlug: "pipelines-git", GitCredentialSlug: "ssh-key", GitCredentialRevision: "revision-one"}
			op := ProposalOperation{CreateOnly: true, OrganizationSlug: "acme", DataSourceSlug: "pipelines-git", PipelineSlug: "new-pipeline", BaseRef: &base, BranchName: "braidplane/create/new-pipeline", Path: "pipelines/new-pipeline.pipeline.yaml", Content: []byte("new definition"), Authority: authority}
			repo := &recordingOperationRepoStore{readErr: repostore.ErrFileNotFound}
			remote := &recordingRemoteGit{repoStore: repo, repoRoot: t.TempDir(), cloneErrByBranch: map[string]error{op.BranchName: repostore.ErrFileNotFound}}
			provider := &recordingProposalProvider{}
			switch scenario {
			case "repeat":
				repo.readErr, repo.readContent, remote.cloneErrByBranch = nil, op.Content, nil
			case "repository changed":
				auth.RepoURL = "ssh://git@git.example.test/acme/other.git"
			case "credential changed":
				auth.GitCredentialRevision = "revision-two"
			case "read only":
				auth.GitWritable = false
			case "provider changed":
				auth.Provider = "github"
			case "tracked branch":
				op.BranchName = "main"
			case "base changed":
				op.Authority.GitBaseRef = "refs/heads/other"
			case "configuration changed":
				auth.GitConfigurationHash = "configuration-two"
			case "source changed":
				auth.GitDataSourceSlug = "other-source"
			case "target exists":
				repo.readErr, repo.readContent = nil, []byte("existing definition")
			case "push denied":
				remote.pushErr = xerror.Forbidden(xerror.RepoFile)
			}
			executor := &proposalExecutor{gitAuthResolver: fakeGitAuthResolver{resolveFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
				return &auth, nil
			}}, remoteGit: remote, provider: provider, resolveHead: func(context.Context, string) *string { return &head }}
			result, err := executor.PublishProposal(t.Context(), op)
			if scenario == "publish" || scenario == "repeat" {
				require.NoError(t, err)
				require.NoError(t, result.ValidatePublication(authority, op.BranchName))
				require.Equal(t, "git", result.Provider)
				require.Equal(t, head, result.BranchHeadSHA)
				if scenario == "publish" {
					require.Equal(t, 1, remote.pushCalls)
					require.Equal(t, repostore.PushOptions{Refspec: "HEAD:refs/heads/" + op.BranchName}, remote.pushedOptions)
				} else {
					require.Zero(t, remote.pushCalls)
				}
			} else {
				require.Error(t, err)
				if scenario != "push denied" {
					require.Zero(t, remote.pushCalls)
				}
			}
			require.Zero(t, provider.upsertCalls)
		})
	}
}

func TestPlainGitResultCannotClaimProviderReviewOrAnotherBranch(t *testing.T) {
	authority := model.PipelineProposalAuthorityPayload{PublicationMode: model.PipelineProposalModeRepositoryCredential}
	valid := ProposalResult{Provider: "git", BranchName: "braidplane/create/new", BranchHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, change := range []func(*ProposalResult){func(r *ProposalResult) { r.ProposalID = "42" }, func(r *ProposalResult) { r.ProposalURL = "https://example.test/pr/42" }, func(r *ProposalResult) { r.ProposalState = "open" }, func(r *ProposalResult) { r.BranchName = "main" }, func(r *ProposalResult) { r.BranchHeadSHA = "unverified" }} {
		result := valid
		change(&result)
		require.Error(t, result.ValidatePublication(authority, valid.BranchName))
	}
	require.Error(t, valid.ValidatePublication(model.PipelineProposalAuthorityPayload{PublicationMode: model.PipelineProposalModeDelegatedUser}, valid.BranchName))
}
