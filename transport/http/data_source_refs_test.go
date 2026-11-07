package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestCreateDataSourceRef(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		dataSourceRefsService: &fakeDataSourceRefsService{
			CreateDataSourceRefFn: func(
				_ context.Context,
				orgSlug, projectSlug, pipelineSlug slug.Slug,
				newRef model.NewDataSourceRef,
				_ model.EventActor,
			) (*model.DataSourceRef, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("acme-users-sync"), pipelineSlug)
				assert.Equal(t, must.Slug("users-source"), newRef.Slug)
				assert.Equal(t, model.DataSourceTypePostgres, newRef.DataSourceType)
				assert.Equal(t, []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}, newRef.RequiredCapabilities)
				return &model.DataSourceRef{
					PipelineSlug:         pipelineSlug,
					Slug:                 newRef.Slug,
					Name:                 newRef.Name,
					DataSourceType:       newRef.DataSourceType,
					RequiredCapabilities: newRef.RequiredCapabilities,
					Timestamps: model.Timestamps{
						CreatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
						UpdatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
					},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceDataSources, input.Resource)
				assert.Equal(t, model.Create, input.Action)
				return true, nil
			},
		},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs",
		api.NewDataSourceRef{
			Name:                 "Users Source",
			Slug:                 ptrAPIString("users-source"),
			DataSourceType:       api.DataSourceTypePostgres,
			RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
		},
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assertResponse(t, resp, api.DataSourceRef{
		PipelineSlug:         "acme-users-sync",
		Slug:                 "users-source",
		Name:                 "Users Source",
		DataSourceType:       ptrAPIDataSourceType(api.DataSourceTypePostgres),
		RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
		CreatedAt:            time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
	})
}

func TestDataSourceRefHandlersRejectInvalidRequiredCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		action model.Action
	}{
		{
			name:   "CreateEmptyCapabilities",
			method: http.MethodPost,
			path:   "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs",
			body: api.NewDataSourceRef{
				Name:                 "Users Source",
				Slug:                 ptrAPIString("users-source"),
				DataSourceType:       api.DataSourceTypePostgres,
				RequiredCapabilities: []api.DataSourceRefCapability{},
			},
			action: model.Create,
		},
		{
			name:   "CreateUnsupportedCapability",
			method: http.MethodPost,
			path:   "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs",
			body: map[string]any{
				"name":                  "Users Source",
				"slug":                  "users-source",
				"data_source_type":      "postgres",
				"required_capabilities": []string{"pipeline_source", "unsupported_capability"},
			},
			action: model.Create,
		},
		{
			name:   "UpdateEmptyCapabilities",
			method: http.MethodPut,
			path:   "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs/users-source",
			body: api.UpdateDataSourceRef{
				Name:                 "Users Source",
				DataSourceType:       api.DataSourceTypePostgres,
				RequiredCapabilities: []api.DataSourceRefCapability{},
			},
			action: model.Update,
		},
		{
			name:   "UpdateUnsupportedCapability",
			method: http.MethodPut,
			path:   "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs/users-source",
			body: map[string]any{
				"name":                  "Users Source",
				"data_source_type":      "postgres",
				"required_capabilities": []string{"pipeline_sink", "unsupported_capability"},
			},
			action: model.Update,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				dataSourceRefsService: &fakeDataSourceRefsService{
					CreateDataSourceRefFn: func(
						context.Context,
						slug.Slug,
						slug.Slug,
						slug.Slug,
						model.NewDataSourceRef,
						model.EventActor,
					) (*model.DataSourceRef, error) {
						t.Fatal("create service must not be called for invalid required_capabilities")
						return nil, errFakeDataSourceRefsServiceFnNotSet
					},
					UpdateDataSourceRefFn: func(
						context.Context,
						slug.Slug,
						slug.Slug,
						slug.Slug,
						slug.Slug,
						model.UpdateDataSourceRef,
						model.EventActor,
					) (*model.DataSourceRef, error) {
						t.Fatal("update service must not be called for invalid required_capabilities")
						return nil, errFakeDataSourceRefsServiceFnNotSet
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceDataSources, input.Resource)
						assert.Equal(t, tt.action, input.Action)
						return true, nil
					},
				},
			})

			resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
				t,
				tt.method,
				tt.path,
				tt.body,
			))
			defer func() { require.NoError(t, resp.Body.Close()) }()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

func TestDataSourceRefReadUpdateAndDeleteHandlers(t *testing.T) {
	t.Parallel()

	ref := testHTTPDataSourceRef()
	app, store := newTestApp(t, testServerConfig{
		dataSourceRefsService: &fakeDataSourceRefsService{
			ListDataSourceRefsFn: func(
				_ context.Context,
				orgSlug, projectSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.DataSourceRef], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, 25, req.Limit)
				return model.ListResponse[model.DataSourceRef]{Items: []model.DataSourceRef{*ref}}, nil
			},
			GetDataSourceRefFn: func(
				_ context.Context,
				orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
			) (*model.DataSourceRef, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("acme-users-sync"), pipelineSlug)
				assert.Equal(t, must.Slug("users-source"), refSlug)
				return ref, nil
			},
			UpdateDataSourceRefFn: func(
				_ context.Context,
				orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
				update model.UpdateDataSourceRef,
				_ model.EventActor,
			) (*model.DataSourceRef, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("acme-users-sync"), pipelineSlug)
				assert.Equal(t, must.Slug("users-source"), refSlug)
				assert.Equal(t, "Users Source Updated", update.Name)
				updated := *ref
				updated.Name = update.Name
				updated.RequiredCapabilities = update.RequiredCapabilities
				return &updated, nil
			},
			DeleteDataSourceRefFn: func(
				_ context.Context,
				orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
				_ model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("acme-users-sync"), pipelineSlug)
				assert.Equal(t, must.Slug("users-source"), refSlug)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceDataSources, input.Resource)
				return true, nil
			},
		},
	})

	listResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/operations/data-source-refs?limit=25", nil))
	defer func() { require.NoError(t, listResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	assertResponse(t, listResp, api.DataSourceRefListResponse{
		DataSourceRefs: []api.DataSourceRef{dataSourceRefToAPI(ref)},
		HasMore:        false,
	})

	getResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs/users-source", nil))
	defer func() { require.NoError(t, getResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	assertResponse(t, getResp, dataSourceRefToAPI(ref))

	updateResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs/users-source",
		api.UpdateDataSourceRef{
			Name:                 "Users Source Updated",
			DataSourceType:       api.DataSourceTypePostgres,
			RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
		},
	))
	defer func() { require.NoError(t, updateResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	deleteResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodDelete, "/organizations/acme/projects/operations/pipelines/acme-users-sync/data-source-refs/users-source", nil))
	defer func() { require.NoError(t, deleteResp.Body.Close()) }()
	require.Equal(t, http.StatusNoContent, deleteResp.StatusCode)
}

func TestUpsertEnvironmentBindingDoesNotExposeConcreteConfigOrCredentials(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		dataSourceRefsService: &fakeDataSourceRefsService{
			UpsertEnvironmentBindingFn: func(
				_ context.Context,
				orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
				binding model.UpsertDataSourceBinding,
				_ model.EventActor,
			) (*model.DataSourceBinding, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("acme-users-sync"), pipelineSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("users-source"), refSlug)
				assert.Equal(t, must.Slug("prod-users-postgres"), binding.DataSourceSlug)
				return &model.DataSourceBinding{
					PipelineSlug:       pipelineSlug,
					DataSourceRefSlug:  must.Slug("users-source"),
					DataSourceRefName:  "Users Source",
					EnvironmentSlug:    must.Slug("prod"),
					DataSourceSlug:     must.Slug("prod-users-postgres"),
					DataSourceName:     "Prod Users Postgres",
					DataSourceType:     model.DataSourceTypePostgres,
					DataSourceActive:   true,
					DataSourceReadOnly: false,
					BoundAt:            time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceDataSources, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/projects/operations/pipelines/acme-users-sync/environments/prod/data-source-bindings/users-source",
		api.UpsertDataSourceBinding{DataSourceSlug: "prod-users-postgres"},
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.DataSourceBinding{
		PipelineSlug:       "acme-users-sync",
		DataSourceRefSlug:  "users-source",
		DataSourceRefName:  "Users Source",
		EnvironmentSlug:    "prod",
		DataSourceSlug:     "prod-users-postgres",
		DataSourceName:     "Prod Users Postgres",
		DataSourceType:     api.DataSourceTypePostgres,
		DataSourceActive:   true,
		DataSourceReadOnly: false,
		BoundAt:            time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
	})
}

func testHTTPDataSourceRef() *model.DataSourceRef {
	return &model.DataSourceRef{
		PipelineSlug:         must.Slug("acme-users-sync"),
		Slug:                 must.Slug("users-source"),
		Name:                 "Users Source",
		DataSourceType:       model.DataSourceTypePostgres,
		RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource},
		Timestamps: model.Timestamps{
			CreatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		},
	}
}

func ptrAPIString(value string) *string {
	return &value
}

func ptrAPIDataSourceType(value api.DataSourceType) *api.DataSourceType {
	return &value
}
