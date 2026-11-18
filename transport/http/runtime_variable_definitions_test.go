package http

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

type runtimeVariableComparisonService struct {
	fakeEnvironmentsService
	compare func(slug.Slug, []string) model.RuntimeVariableComparison
}

func (s *runtimeVariableComparisonService) CompareRuntimeVariables(_ context.Context, org slug.Slug, keys []string) (model.RuntimeVariableComparison, error) {
	return s.compare(org, keys), nil
}

func TestCompareRuntimeVariablesScopesAndReturnsBlockers(t *testing.T) {
	t.Parallel()
	var authorized atomic.Bool
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceEnvironments, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				assert.Equal(t, new(slug.Slug("acme")), input.OrgSlug)
				authorized.Store(true)
				return true, nil
			},
		},
		environmentsService: &runtimeVariableComparisonService{
			compare: func(org slug.Slug, keys []string) model.RuntimeVariableComparison {
				assert.Equal(t, slug.Slug("acme"), org)
				assert.Equal(t, []string{"batch_size", "region"}, keys)
				return model.CompareRuntimeVariables([]slug.Slug{"dev", "prod"}, nil, nil, keys)
			},
		},
	})
	req := jsonRequest(t, http.MethodGet, "/organizations/acme/runtime-variable-comparison?used_key=batch_size&used_key=region", nil)
	resp := authenticatedRequest(t, app, store, "johndoe", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, authorized.Load())
	missing := api.RuntimeVariableComparisonCell{
		Source: api.RuntimeVariableSourceMissing, Required: true, Blocked: true,
	}
	assertResponse(t, resp, api.RuntimeVariableComparison{
		Environments: []string{"dev", "prod"},
		Rows: []api.RuntimeVariableComparisonRow{
			{Key: "batch_size", Used: true, Cells: []api.RuntimeVariableComparisonCell{missing, missing}},
			{Key: "region", Used: true, Cells: []api.RuntimeVariableComparisonCell{missing, missing}},
		},
	})
}
