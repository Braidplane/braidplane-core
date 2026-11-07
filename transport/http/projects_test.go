package http

import (
	"context"
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
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
)

var testProject = &model.Project{
	Name: "Default project",
	Slug: "default",
	PipelineBackend: &model.ProjectPipelineBackend{
		DataSourceSlug: must.Slug("pipelines-git"),
		Repo:           "https://example.com/acme/pipelines.git",
		TrackedBranch:  "main",
	},
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC),
	},
}

var testProjectAPI = api.Project{
	Name: "Default project",
	Slug: "default",
	PipelineBackend: &api.ProjectPipelineBackend{
		DataSourceSlug: "pipelines-git",
		Repo:           "https://example.com/acme/pipelines.git",
		TrackedBranch:  "main",
	},
	CreatedAt: testProject.CreatedAt,
	UpdatedAt: testProject.UpdatedAt,
}

func TestListProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authAllowed bool
		listFn      func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Project], error)
		wantStatus  int
		wantBody    *api.ProjectListResponse
		wantErr     *apierror.Error
	}{
		{
			name:        "ReturnsProjectsForOrganization",
			authAllowed: true,
			listFn: func(_ context.Context, orgSlug slug.Slug, _ model.ListRequest) (model.ListResponse[model.Project], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				return model.ListResponse[model.Project]{
					Items: []model.Project{*testProject},
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.ProjectListResponse{
				Projects: []api.Project{testProjectAPI},
				HasMore:  false,
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("projects", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					ListProjectsFn: tt.listFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects", nil)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, *tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestCreateProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        any
		authAllowed bool
		createFn    func(ctx context.Context, orgSlug slug.Slug, newProject model.NewProject, actor model.EventActor) (*model.Project, error)
		wantStatus  int
		wantBody    *api.Project
		wantErr     *apierror.Error
	}{
		{
			name:        "CreatesProjectAndReturns201",
			body:        api.NewProject{Name: "Default project", Slug: ptr.Ptr(api.Slug("default"))},
			authAllowed: true,
			createFn: func(_ context.Context, orgSlug slug.Slug, newProject model.NewProject, _ model.EventActor) (*model.Project, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), newProject.Slug)
				return testProject, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   &testProjectAPI,
		},
		{
			name:        "Returns409WhenProjectSlugAlreadyExistsInOrganization",
			body:        api.NewProject{Name: "Default project", Slug: ptr.Ptr(api.Slug("default"))},
			authAllowed: true,
			createFn: func(context.Context, slug.Slug, model.NewProject, model.EventActor) (*model.Project, error) {
				return nil, projectssvc.ErrSlugTaken
			},
			wantStatus: http.StatusConflict,
			wantErr: apierror.New(
				http.StatusConflict,
				"PROJECT_ALREADY_EXISTS",
				"project already exists",
			),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.NewProject{Name: "Default project", Slug: ptr.Ptr(api.Slug("default"))},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("projects", "create"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					CreateProjectFn: tt.createFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects", tt.body)
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

func TestCreateProjectRejectsEntitlementLimit(t *testing.T) {
	t.Parallel()

	createCalled := false
	app, store := newTestApp(t, testServerConfig{
		admissionService: &fakes.AdmissionService{CheckProjectAdmissionFn: func(
			_ context.Context,
			orgSlug slug.Slug,
		) error {
			require.Equal(t, must.Slug("acme"), orgSlug)
			return admissionsvc.ErrAdmissionDenied.WithPublicDetail("The project limit has been reached.")
		}},
		projectsService: &fakeProjectsService{CreateProjectFn: func(
			context.Context,
			slug.Slug,
			model.NewProject,
			model.EventActor,
		) (*model.Project, error) {
			createCalled = true
			return testProject, nil
		}},
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) {
			return true, nil
		}},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects", api.NewProject{Name: "Second project"})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.False(t, createCalled)
}

func TestCreateProjectRejectsLegacyPipelineBackendField(t *testing.T) {
	t.Parallel()

	called := false
	app, store := newTestApp(t, testServerConfig{
		projectsService: &fakeProjectsService{
			CreateProjectFn: func(context.Context, slug.Slug, model.NewProject, model.EventActor) (*model.Project, error) {
				called = true
				return testProject, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceProjects, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects", map[string]any{
		"name":                              "Default project",
		"pipeline_backend_data_source_slug": "pipelines-git",
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, called, "create project service must not be called when legacy pipeline backend ownership field is provided")
}

func TestGetProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authAllowed bool
		getFn       func(ctx context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error)
		wantStatus  int
		wantBody    *api.Project
		wantErr     *apierror.Error
	}{
		{
			name:        "ReturnsProjectBySlugWithinOrganization",
			authAllowed: true,
			getFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), projectSlug)
				return testProject, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   &testProjectAPI,
		},
		{
			name:        "Returns404WhenProjectNotFoundInOrganization",
			authAllowed: true,
			getFn: func(context.Context, slug.Slug, slug.Slug) (*model.Project, error) {
				return nil, projectssvc.ErrProjectNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("projects", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					GetProjectFn: tt.getFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default", nil)
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

func TestGetProjectUsesNestedPipelineBackendObject(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		projectsService: &fakeProjectsService{
			GetProjectFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), projectSlug)
				return testProject, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceProjects, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() { _ = resp.Body.Close() }()

	body := parseResponse[map[string]any](t, resp)
	backend, ok := body["pipeline_backend"].(map[string]any)
	require.True(t, ok, "project response must expose nested pipeline_backend metadata")
	assert.Equal(t, "pipelines-git", backend["data_source_slug"])
	_, hasRepo := backend["repo"]
	assert.True(t, hasRepo, "project pipeline backend must expose normalized repo")
	_, hasTrackedBranch := backend["tracked_branch"]
	assert.True(t, hasTrackedBranch, "project pipeline backend must expose tracked branch")
	_, hasLegacyField := body["pipeline_backend_data_source_slug"]
	assert.False(t, hasLegacyField, "project response must stop exposing the legacy flat pipeline backend owner field")
}

func TestUpdateProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        any
		authAllowed bool
		updateFn    func(ctx context.Context, orgSlug, projectSlug slug.Slug, update model.UpdateProject, actor model.EventActor) (*model.Project, error)
		wantStatus  int
		wantBody    *api.Project
		wantErr     *apierror.Error
	}{
		{
			name:        "UpdatesProjectAndReturnsProject",
			body:        api.UpdateProject{Name: "Renamed project"},
			authAllowed: true,
			updateFn: func(_ context.Context, orgSlug, projectSlug slug.Slug, update model.UpdateProject, _ model.EventActor) (*model.Project, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), projectSlug)
				assert.Equal(t, "Renamed project", update.Name)
				return &model.Project{
					Slug:            projectSlug,
					Name:            update.Name,
					PipelineBackend: testProject.PipelineBackend,
					Timestamps:      testProject.Timestamps,
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.Project{
				Slug:            "default",
				Name:            "Renamed project",
				PipelineBackend: projectPipelineBackendToHTTP(testProject.PipelineBackend),
				CreatedAt:       testProject.CreatedAt,
				UpdatedAt:       testProject.UpdatedAt,
			},
		},
		{
			name:        "Returns404WhenProjectNotFoundInOrganization",
			body:        api.UpdateProject{Name: "Renamed project"},
			authAllowed: true,
			updateFn: func(context.Context, slug.Slug, slug.Slug, model.UpdateProject, model.EventActor) (*model.Project, error) {
				return nil, projectssvc.ErrProjectNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.UpdateProject{Name: "Renamed project"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("projects", "update"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					UpdateProjectFn: tt.updateFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPut, "/organizations/acme/projects/default", tt.body)
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

func TestUpdateProjectRejectsLegacyPipelineBackendField(t *testing.T) {
	t.Parallel()

	called := false
	app, store := newTestApp(t, testServerConfig{
		projectsService: &fakeProjectsService{
			UpdateProjectFn: func(context.Context, slug.Slug, slug.Slug, model.UpdateProject, model.EventActor) (*model.Project, error) {
				called = true
				return testProject, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceProjects, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/organizations/acme/projects/default", map[string]any{
		"name":                              "Renamed project",
		"pipeline_backend_data_source_slug": "pipelines-git",
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.False(t, called, "update project service must not be called when legacy pipeline backend ownership field is provided")
}

func TestSetProjectPipelineBackend(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		projectsService: &fakeProjectsService{
			SetProjectPipelineBackendFn: func(
				_ context.Context,
				orgSlug, projectSlug slug.Slug,
				params model.SetProjectPipelineBackend,
			) (*model.Project, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), projectSlug)
				assert.Equal(t, must.Slug("pipelines-git"), params.DataSourceSlug)
				assert.Equal(t, "main", params.TrackedBranch)
				assert.Equal(t, "pipelines", params.PipelineDir)
				assert.Equal(t, model.SetProjectPipelineBackendModeAdoptExisting, params.Mode)
				return testProject, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceProjects, input.Resource)
				assert.Equal(t, model.Update, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/organizations/acme/projects/default/pipeline-backend", map[string]any{
		"data_source_slug": "pipelines-git",
		"tracked_branch":   " main ",
		"pipeline_dir":     " pipelines ",
		"mode":             "adopt_existing",
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, testProjectAPI)
}

func TestSetProjectPipelineBackendRejectsInvalidMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode string
	}{
		{name: "UnknownMode", mode: "replace_current"},
		{name: "EmptyMode", mode: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			called := false
			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					SetProjectPipelineBackendFn: func(
						context.Context,
						slug.Slug,
						slug.Slug,
						model.SetProjectPipelineBackend,
					) (*model.Project, error) {
						called = true
						return testProject, nil
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						assert.Equal(t, model.Update, input.Action)
						return true, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPut, "/organizations/acme/projects/default/pipeline-backend", map[string]any{
				"data_source_slug": "pipelines-git",
				"tracked_branch":   "main",
				"mode":             tt.mode,
			})
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.False(t, called, "invalid pipeline backend mode must not reach the service")
		})
	}
}

func TestDeleteProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authAllowed bool
		deleteFn    func(ctx context.Context, orgSlug, projectSlug slug.Slug, options model.DeleteProjectOptions, actor model.EventActor) error
		wantStatus  int
		wantErr     *apierror.Error
	}{
		{
			name:        "DeletesProjectAndReturns204",
			authAllowed: true,
			deleteFn: func(_ context.Context, orgSlug, projectSlug slug.Slug, options model.DeleteProjectOptions, _ model.EventActor) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("default"), projectSlug)
				assert.False(t, options.CleanupRemote)
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns404WhenProjectNotFoundInOrganization",
			authAllowed: true,
			deleteFn: func(context.Context, slug.Slug, slug.Slug, model.DeleteProjectOptions, model.EventActor) error {
				return projectssvc.ErrProjectNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("projects", "delete"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				projectsService: &fakeProjectsService{
					DeleteProjectFn: tt.deleteFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceProjects, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodDelete, "/organizations/acme/projects/default", nil)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}
