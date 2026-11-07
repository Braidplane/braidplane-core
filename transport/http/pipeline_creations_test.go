package http

import (
	"context"
	"net/http"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

type fakeCreationService struct {
	list   func(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*pipelinesvc.CreationProposalPage, error)
	create func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, *pipelinespec.Pipeline) (*pipelinesvc.CreationProposal, error)
	get    func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesvc.CreationProposal, error)
}

func (f fakeCreationService) List(ctx context.Context, org, project, after slug.Slug, limit int) (*pipelinesvc.CreationProposalPage, error) {
	return f.list(ctx, org, project, after, limit)
}

func (f fakeCreationService) Create(ctx context.Context, o, p, m slug.Slug, a model.EventActor, spec *pipelinespec.Pipeline) (*pipelinesvc.CreationProposal, error) {
	return f.create(ctx, o, p, m, a, spec)
}

func (f fakeCreationService) Get(ctx context.Context, o, p, c slug.Slug) (*pipelinesvc.CreationProposal, error) {
	return f.get(ctx, o, p, c)
}

func TestPipelineCreationListReadPermissionAndPagination(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.ResourcePipelines, input.Resource)
					require.Equal(t, model.Read, input.Action)
					return allowed, nil
				}},
				pipelineCreationsService: fakeCreationService{list: func(_ context.Context, org, project, after slug.Slug, limit int) (*pipelinesvc.CreationProposalPage, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("project"), project)
					require.Equal(t, slug.Slug("creation-before"), after)
					require.Equal(t, 1, limit)
					return &pipelinesvc.CreationProposalPage{HasMore: true, Items: []*pipelinesvc.CreationProposal{{Slug: "creation-first", ProjectSlug: project, PipelineSlug: "first", Status: "publish_queued", Spec: &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "first"}}}}}, nil
				}},
			})
			response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/project/pipeline-creations?limit=1&cursor=creation-before", nil))
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, allowed, called)
			if !allowed {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				return
			}
			require.Equal(t, http.StatusOK, response.StatusCode)
			page := parseResponse[api.PipelineCreationProposalListResponse](t, response)
			require.True(t, page.HasMore)
			require.Equal(t, "creation-first", *page.NextCursor)
			require.Len(t, page.PipelineCreations, 1)
		})
	}
}

func TestPipelineCreationRequiresCreatePermissionAndReturnsPendingIdentity(t *testing.T) {
	t.Parallel()
	for _, allowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
					require.Equal(t, model.Create, in.Action)
					require.Equal(t, model.ResourcePipelines, in.Resource)
					return allowed, nil
				}},
				pipelineCreationsService: fakeCreationService{create: func(_ context.Context, o, p, m slug.Slug, actor model.EventActor, spec *pipelinespec.Pipeline) (*pipelinesvc.CreationProposal, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), o)
					require.Equal(t, slug.Slug("project"), p)
					require.Equal(t, slug.Slug("alice"), m)
					require.Equal(t, model.UserEventActor("alice"), actor)
					require.Equal(t, slug.Slug("first"), spec.Metadata.Slug)
					require.Equal(t, int64(9007199254740993), spec.Steps[0].Query.Parameters["id"])
					return &pipelinesvc.CreationProposal{Slug: "creation-first", ProjectSlug: p, PipelineSlug: "first", MemberSlug: m, Status: "publish_queued", SourcePath: "pipelines/first.pipeline.yaml", SpecHash: "proposed-hash", Spec: spec}, nil
				}},
			})
			request := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/project/pipeline-creations", map[string]any{"metadata": map[string]any{"slug": "first", "name": "First read"}, "steps": []any{map[string]any{"slug": "fetch", "kind": "query", "query": map[string]any{"data_source_ref": "source", "operation_type": "sql", "operation_ref": "list", "parameters": map[string]any{"id": int64(9007199254740993)}}}}})
			response := authenticatedRequest(t, app, store, "alice", request)
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, allowed, called)
			if !allowed {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				return
			}
			require.Equal(t, http.StatusOK, response.StatusCode)
			body := parseResponse[api.PipelineCreationProposal](t, response)
			require.Equal(t, "creation-first", body.Slug)
			require.Equal(t, api.CreationPublishQueued, body.Status)
			require.Equal(t, "first", body.Spec.Metadata.Slug)
			require.Nil(t, body.ProposalUrl)
		})
	}
}

func TestPipelineCreationInspectionDeniesUnreadableDefinitions(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
		require.Equal(t, model.Read, in.Action)
		require.Equal(t, model.ResourcePipelines, in.Resource)
		return false, nil
	}}, pipelineCreationsService: fakeCreationService{get: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinesvc.CreationProposal, error) {
		t.Error("an unreadable proposed definition must not be loaded")
		return nil, pipelinesvc.ErrPipelineProposalAuthorityDenied
	}}})
	response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/project/pipeline-creations/creation-first", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}

func TestPipelineCreationInspectionScopesProjectAndReadPermission(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
		require.Equal(t, model.Read, in.Action)
		require.Equal(t, model.ResourcePipelines, in.Resource)
		return true, nil
	}}, pipelineCreationsService: fakeCreationService{get: func(_ context.Context, o, p, c slug.Slug) (*pipelinesvc.CreationProposal, error) {
		require.Equal(t, slug.Slug("acme"), o)
		require.Equal(t, slug.Slug("project"), p)
		require.Equal(t, slug.Slug("creation-first"), c)
		return &pipelinesvc.CreationProposal{Slug: c, ProjectSlug: p, PipelineSlug: "first", MemberSlug: "alice", Status: "publish_failed", FailureCode: "provider_unavailable", Spec: &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "first", Name: "Proposed first"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "list"}}}}}, nil
	}}})
	response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, "/organizations/acme/projects/project/pipeline-creations/creation-first", nil))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body := parseResponse[api.PipelineCreationProposal](t, response)
	require.Equal(t, "creation-first", body.Slug)
	require.Equal(t, "Proposed first", body.Spec.Metadata.Name)
	require.NotNil(t, body.FailureCode)
	require.Equal(t, "provider_unavailable", *body.FailureCode)
}

func TestPlainGitCreationOmitsProviderReviewFields(t *testing.T) {
	spec := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "first", Name: "First"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "read"}}}}
	result, err := pipelineCreationToHTTP(&pipelinesvc.CreationProposal{Slug: "creation-first", ProjectSlug: "project", PipelineSlug: "first", MemberSlug: "alice", Status: "branch_pushed", Spec: spec, Proposal: &pipelinegit.ProposalResult{Provider: "git", BranchName: "braidplane/create/creation-first", BranchHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	require.NoError(t, err)
	require.Equal(t, api.CreationBranchPushed, result.Status)
	require.Equal(t, "git", *result.ProposalProvider)
	require.Equal(t, "braidplane/create/creation-first", *result.BranchName)
	require.Nil(t, result.ProposalUrl)
	require.Nil(t, result.ProposalState)
}
