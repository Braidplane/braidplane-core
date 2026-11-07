package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
)

var testEnvironmentDescription = "Protected production runtime target"

var testEnvironment = &model.Environment{
	Name:        "Production",
	Slug:        "prod",
	Description: &testEnvironmentDescription,
	Color:       model.EnvironmentColorRed,
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 25, 10, 30, 0, 0, time.UTC),
	},
}

var testEnvironmentAPI = api.Environment{
	Name:        "Production",
	Slug:        "prod",
	Description: ptr.Ptr(testEnvironmentDescription),
	Color:       api.EnvironmentColorRed,
	DeletedAt:   nil,
	CreatedAt:   testEnvironment.CreatedAt,
	UpdatedAt:   testEnvironment.UpdatedAt,
}

var testProjectEnvironment = &model.ProjectEnvironment{
	Environment: *testEnvironment,
	IsDefault:   true,
	EnabledAt:   time.Date(2026, 5, 25, 10, 35, 0, 0, time.UTC),
	UpdatedAt:   time.Date(2026, 5, 25, 10, 35, 0, 0, time.UTC),
}

var testProjectEnvironmentAPI = api.ProjectEnvironment{
	EnvironmentSlug:        "prod",
	EnvironmentName:        "Production",
	EnvironmentDescription: ptr.Ptr(testEnvironmentDescription),
	EnvironmentColor:       api.EnvironmentColorRed,
	IsDefault:              true,
	EnabledAt:              testProjectEnvironment.EnabledAt,
	UpdatedAt:              testProjectEnvironment.UpdatedAt,
}

var testRuntimeVariableDescription = "S3 object prefix"

var testRuntimeVariable = &model.RuntimeVariable{
	EnvironmentSlug: must.Slug("prod"),
	Key:             "s3_prefix",
	ValueType:       model.RuntimeVariableValueTypeText,
	Value:           "prd/orders/",
	Description:     &testRuntimeVariableDescription,
	CreatedAt:       time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
	UpdatedAt:       time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
}

var testRuntimeVariableAPI = api.RuntimeVariable{
	EnvironmentSlug: "prod",
	Key:             "s3_prefix",
	ValueType:       api.RuntimeVariableValueTypeText,
	Value:           "prd/orders/",
	Description:     ptr.Ptr(testRuntimeVariableDescription),
	CreatedAt:       testRuntimeVariable.CreatedAt,
	UpdatedAt:       testRuntimeVariable.UpdatedAt,
}

var testEnvironmentSecretDescription = "GitHub token"

var testEnvironmentSecret = &model.EnvironmentSecret{
	EnvironmentSlug: must.Slug("prod"),
	Key:             "API_TOKEN",
	Description:     &testEnvironmentSecretDescription,
	CreatedAt:       time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	UpdatedAt:       time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
}

var testEnvironmentSecretAPI = api.EnvironmentSecret{
	EnvironmentSlug: "prod",
	Key:             "API_TOKEN",
	Description:     ptr.Ptr(testEnvironmentSecretDescription),
	CreatedAt:       testEnvironmentSecret.CreatedAt,
	UpdatedAt:       testEnvironmentSecret.UpdatedAt,
}

func TestListEnvironments(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			ListEnvironmentsFn: func(_ context.Context, orgSlug slug.Slug, req model.ListEnvironmentsRequest) (model.ListResponse[model.Environment], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.True(t, req.IncludeDeleted)
				assert.Equal(t, 10, req.Limit)
				assert.Equal(t, model.ListSortName, req.SortBy)
				return model.ListResponse[model.Environment]{
					Items: []model.Environment{*testEnvironment},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/environments?limit=10&include_deleted=true&sort_by=environment_name",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.EnvironmentListResponse{
		Environments: []api.Environment{testEnvironmentAPI},
		HasMore:      false,
	})
}

func TestCreateEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        any
		authAllowed bool
		createFn    func(ctx context.Context, orgSlug slug.Slug, newEnvironment model.NewEnvironment, actor model.EventActor) (*model.Environment, error)
		wantStatus  int
		wantBody    *api.Environment
		wantErr     *apierror.Error
	}{
		{
			name: "CreatesEnvironmentAndReturns201",
			body: api.NewEnvironment{
				Name:        "Production",
				Slug:        ptr.Ptr(api.Slug("prod")),
				Description: ptr.Ptr(testEnvironmentDescription),
				Color:       api.EnvironmentColorRed,
			},
			authAllowed: true,
			createFn: func(_ context.Context, orgSlug slug.Slug, newEnvironment model.NewEnvironment, _ model.EventActor) (*model.Environment, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), newEnvironment.Slug)
				assert.Equal(t, "Production", newEnvironment.Name)
				require.NotNil(t, newEnvironment.Description)
				assert.Equal(t, testEnvironmentDescription, *newEnvironment.Description)
				assert.Equal(t, model.EnvironmentColorRed, newEnvironment.Color)
				return testEnvironment, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   &testEnvironmentAPI,
		},
		{
			name: "Returns409WhenEnvironmentSlugAlreadyExistsInOrganization",
			body: api.NewEnvironment{
				Name:  "Production",
				Slug:  ptr.Ptr(api.Slug("prod")),
				Color: api.EnvironmentColorRed,
			},
			authAllowed: true,
			createFn: func(context.Context, slug.Slug, model.NewEnvironment, model.EventActor) (*model.Environment, error) {
				return nil, environmentssvc.ErrSlugTaken
			},
			wantStatus: http.StatusConflict,
			wantErr: apierror.New(
				http.StatusConflict,
				"ENVIRONMENT_ALREADY_EXISTS",
				"environment already exists",
			),
		},
		{
			name: "Returns400WhenColorIsUnsupported",
			body: api.NewEnvironment{
				Name:  "Production",
				Slug:  ptr.Ptr(api.Slug("prod")),
				Color: api.EnvironmentColor("neon"),
			},
			authAllowed: true,
			createFn: func(context.Context, slug.Slug, model.NewEnvironment, model.EventActor) (*model.Environment, error) {
				t.Fatal("create should not reach service with invalid color")
				return nil, assert.AnError
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.ValidationFailed(
				`Invalid environment color: environment invalid input: unsupported environment color "neon"`,
			),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.NewEnvironment{Name: "Production"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("environments", "create"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				environmentsService: &fakeEnvironmentsService{
					CreateEnvironmentFn: tt.createFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceEnvironments, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/environments", tt.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestGetEnvironment(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			GetEnvironmentFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug) (*model.Environment, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				return testEnvironment, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/environments/prod", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testEnvironmentAPI)
}

func TestUpdateEnvironment(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpdateEnvironmentFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				update model.UpdateEnvironment,
				_ model.EventActor,
			) (*model.Environment, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "Production", update.Name)
				require.NotNil(t, update.Description)
				assert.Equal(t, testEnvironmentDescription, *update.Description)
				assert.Equal(t, model.EnvironmentColorRed, update.Color)
				assert.True(t, update.Protected)
				return testEnvironment, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod",
		api.UpdateEnvironment{
			Name:        "Production",
			Description: ptr.Ptr(testEnvironmentDescription),
			Color:       api.EnvironmentColorRed,
			Protected:   true,
		},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testEnvironmentAPI)
}

func TestUpdateEnvironmentRejectsUnsupportedColor(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpdateEnvironmentFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				model.UpdateEnvironment,
				model.EventActor,
			) (*model.Environment, error) {
				t.Fatal("update should not reach service with invalid color")
				return nil, assert.AnError
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod",
		api.UpdateEnvironment{
			Name:      "Production",
			Color:     api.EnvironmentColor("neon"),
			Protected: false,
		},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.ValidationFailed(
		`Invalid environment color: environment invalid input: unsupported environment color "neon"`,
	))
}

func TestUpdateEnvironmentRejectsMissingProtectedField(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpdateEnvironmentFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				model.UpdateEnvironment,
				model.EventActor,
			) (*model.Environment, error) {
				t.Fatal("update should not reach service without protected")
				return nil, assert.AnError
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod",
		map[string]any{
			"name": "Production",
		},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assertResponse(t, resp, apierror.ErrorResponse{
		Error: &apierror.Error{
			Code:    "VALIDATION_FAILED",
			Message: "protected is required",
		},
	})
}

func TestDeleteEnvironment(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			DeleteEnvironmentFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				deleted model.DeleteEnvironment,
				_ model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				require.NotNil(t, deleted.Reason)
				assert.Equal(t, "Superseded by production", *deleted.Reason)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Delete, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodDelete,
		"/organizations/acme/environments/prod?delete_reason=Superseded+by+production",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestListProjectEnvironments(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			ListProjectEnvironmentsFn: func(_ context.Context, orgSlug, projectSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("ops"), projectSlug)
				assert.Equal(t, 10, req.Limit)
				assert.Equal(t, model.ListSortName, req.SortBy)
				return model.ListResponse[model.ProjectEnvironment]{
					Items: []model.ProjectEnvironment{*testProjectEnvironment},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/projects/ops/environments?limit=10&sort_by=environment_name",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.ProjectEnvironmentListResponse{
		ProjectEnvironments: []api.ProjectEnvironment{testProjectEnvironmentAPI},
		HasMore:             false,
	})
}

func TestListProjectEnvironmentsRejectsPipelineReadWithoutEnvironmentRead(t *testing.T) {
	t.Parallel()

	serviceCalled := false
	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			ListProjectEnvironmentsFn: func(context.Context, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error) {
				serviceCalled = true
				return model.ListResponse[model.ProjectEnvironment]{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Read, input.Action)
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				return false, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/projects/ops/environments?limit=1",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, serviceCalled)
}

func TestEnableProjectEnvironment(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			EnableProjectEnvironmentFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug slug.Slug,
				params model.EnableProjectEnvironment,
				_ model.EventActor,
			) (*model.ProjectEnvironment, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("ops"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.True(t, params.IsDefault)
				return testProjectEnvironment, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/projects/ops/environments/prod",
		api.EnableProjectEnvironmentRequest{IsDefault: ptr.Ptr(true)},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testProjectEnvironmentAPI)
}

func TestDisableProjectEnvironment(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			DisableProjectEnvironmentFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug slug.Slug,
				_ *slug.Slug,
				_ model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("ops"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Delete, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodDelete, "/organizations/acme/projects/ops/environments/prod", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestListRuntimeVariables(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			ListRuntimeVariablesFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.RuntimeVariable], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, 10, req.Limit)
				assert.Equal(t, "page-2", req.Cursor)
				assert.Equal(t, model.ListSortUpdatedAt, req.SortBy)
				assert.Equal(t, model.ListDirectionPrev, req.Direction)
				return model.ListResponse[model.RuntimeVariable]{
					Items: []model.RuntimeVariable{*testRuntimeVariable},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/environments/prod/runtime-variables?limit=10&cursor=page-2&direction=prev",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.RuntimeVariableListResponse{
		RuntimeVariables: []api.RuntimeVariable{testRuntimeVariableAPI},
		HasMore:          false,
	})
}

func TestGetRuntimeVariable(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			GetRuntimeVariableFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug, key string) (*model.RuntimeVariable, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "s3_prefix", key)
				return testRuntimeVariable, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/environments/prod/runtime-variables/s3_prefix", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testRuntimeVariableAPI)
}

func TestUpsertRuntimeVariable(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpsertRuntimeVariableFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				params model.UpsertRuntimeVariable,
				_ model.EventActor,
			) (*model.RuntimeVariable, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "s3_prefix", params.Key)
				assert.Equal(t, model.RuntimeVariableValueTypeText, params.ValueType)
				assert.Equal(t, "prd/orders/", params.Value)
				require.NotNil(t, params.Description)
				assert.Equal(t, testRuntimeVariableDescription, *params.Description)
				return testRuntimeVariable, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod/runtime-variables/s3_prefix",
		api.UpsertRuntimeVariable{
			ValueType:   api.RuntimeVariableValueTypeText,
			Value:       "prd/orders/",
			Description: ptr.Ptr(testRuntimeVariableDescription),
		},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testRuntimeVariableAPI)
}

func TestUpsertRuntimeVariableRejectsMissingValue(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpsertRuntimeVariableFn: func(
				_ context.Context,
				_, _ slug.Slug,
				_ model.UpsertRuntimeVariable,
				_ model.EventActor,
			) (*model.RuntimeVariable, error) {
				require.FailNow(t, "runtime variable service should not be called")
				return nil, environmentssvc.ErrInvalidRuntimeVariable
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod/runtime-variables/s3_prefix",
		map[string]any{"value_type": "text"},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestDeleteRuntimeVariable(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			DeleteRuntimeVariableFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				key string,
				_ *slug.Slug,
				_ model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "s3_prefix", key)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Delete, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodDelete, "/organizations/acme/environments/prod/runtime-variables/s3_prefix", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestListEnvironmentSecretsReturnsSafeMetadata(t *testing.T) {
	t.Parallel()

	const plaintext = "ghp_super_secret"

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			ListEnvironmentSecretsFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.EnvironmentSecret], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, 10, req.Limit)
				assert.Equal(t, "page-2", req.Cursor)
				assert.Equal(t, model.ListSortUpdatedAt, req.SortBy)
				assert.Equal(t, model.ListDirectionPrev, req.Direction)
				return model.ListResponse[model.EnvironmentSecret]{
					Items: []model.EnvironmentSecret{*testEnvironmentSecret},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/environments/prod/secrets?limit=10&cursor=page-2&direction=prev",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(body), plaintext)
	var got api.EnvironmentSecretListResponse
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, api.EnvironmentSecretListResponse{
		EnvironmentSecrets: []api.EnvironmentSecret{testEnvironmentSecretAPI},
		HasMore:            false,
	}, got)
}

func TestGetEnvironmentSecretReturnsSafeMetadata(t *testing.T) {
	t.Parallel()

	const plaintext = "ghp_super_secret"

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			GetEnvironmentSecretFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug, key string) (*model.EnvironmentSecret, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "API_TOKEN", key)
				return testEnvironmentSecret, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/environments/prod/secrets/API_TOKEN", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(body), plaintext)
	var got api.EnvironmentSecret
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, testEnvironmentSecretAPI, got)
}

func TestUpsertEnvironmentSecretAcceptsPlaintextButReturnsSafeMetadata(t *testing.T) {
	t.Parallel()

	const plaintext = "ghp_super_secret"

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			UpsertEnvironmentSecretFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				params model.UpsertEnvironmentSecret,
				_ model.EventActor,
			) (*model.EnvironmentSecret, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "API_TOKEN", params.Key)
				assert.Equal(t, plaintext, params.Value)
				require.NotNil(t, params.Description)
				assert.Equal(t, testEnvironmentSecretDescription, *params.Description)
				return testEnvironmentSecret, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/environments/prod/secrets/API_TOKEN",
		api.UpsertEnvironmentSecret{
			Value:       ptr.Ptr(plaintext),
			Description: ptr.Ptr(testEnvironmentSecretDescription),
		},
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotContains(t, string(body), plaintext)
	var got api.EnvironmentSecret
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, testEnvironmentSecretAPI, got)
}

func TestDeleteEnvironmentSecretPassesProtectedConfirmation(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		environmentsService: &fakeEnvironmentsService{
			DeleteEnvironmentSecretFn: func(
				_ context.Context,
				orgSlug, environmentSlug slug.Slug,
				key string,
				confirmation *slug.Slug,
				_ model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, "API_TOKEN", key)
				require.NotNil(t, confirmation)
				assert.Equal(t, must.Slug("prod"), *confirmation)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Delete, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodDelete,
		"/organizations/acme/environments/prod/secrets/API_TOKEN?protected_environment_confirmation=prod",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
