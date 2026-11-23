package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinediff "github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"

	"github.com/stretchr/testify/require"
)

func TestServer_ComparePipelineDeployment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		authorized bool
		serviceErr error
		status     int
	}{
		{"reader sees comparison", true, nil, http.StatusOK},
		{"reader sees missing deployment", true, pipelinesvc.ErrPipelineDeploymentNotFound, http.StatusNotFound},
		{"reader sees missing version", true, pipelinesvc.ErrPipelineVersionNotFound, http.StatusNotFound},
		{"reader sees invalid candidate", true, xerror.InvalidInput(xerror.Pipeline), http.StatusBadRequest},
		{"denied before lookup", false, nil, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := json.RawMessage(`{"metadata":{"slug":"sync","name":"Sync"},"steps":[]}`)
			now := time.Now().UTC()
			app, session := newTestApp(t, testServerConfig{
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					require.Equal(t, model.ResourcePipelines, input.Resource)
					require.Equal(t, model.Read, input.Action)
					return tc.authorized, nil
				}},
				admissionService: &fakes.AdmissionService{CheckWriteAccessFn: func(context.Context, slug.Slug) error {
					t.Error("comparison checked write admission")
					return nil
				}},
				pipelinesService: &fakePipelinesService{CompareDeploymentFn: func(
					_ context.Context, org, project, environment, pipeline slug.Slug, raw json.RawMessage,
				) (*pipelinesvc.PipelineDeploymentComparison, error) {
					require.True(t, tc.authorized)
					require.Equal(t, []slug.Slug{"acme", "default", "dev", "sync"}, []slug.Slug{org, project, environment, pipeline})
					require.JSONEq(t, string(body), string(raw))
					if tc.serviceErr != nil {
						return nil, tc.serviceErr
					}
					return &pipelinesvc.PipelineDeploymentComparison{
						DeploymentRevision: 2, DeployedAt: now, BaselineSpecHash: "deployed-hash", CandidateSpecHash: "candidate-hash",
						Impact: &pipelinediff.CheckResult{
							Severity: pipelinediff.SeverityBreaking,
							Findings: []pipelinediff.Finding{{Severity: pipelinediff.SeverityBreaking, Message: "required field removed"}},
						},
					}, nil
				}},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/environments/dev/pipelines/sync/deployment:compare", body)
			resp := authenticatedRequest(t, app, session, "test-user", req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, tc.status, resp.StatusCode)
			if tc.status == http.StatusOK {
				var result api.PipelineDeploymentComparison
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
				require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
				require.Equal(t, "dev", result.EnvironmentSlug)
				require.Equal(t, int64(2), result.DeploymentRevision)
				require.Equal(t, "deployed-hash", result.BaselineSpecHash)
				require.Equal(t, "candidate-hash", result.CandidateSpecHash)
				require.Equal(t, api.PipelineComparisonSeverityBreaking, result.Severity)
				require.Len(t, result.Findings, 1)
			}
		})
	}
}
