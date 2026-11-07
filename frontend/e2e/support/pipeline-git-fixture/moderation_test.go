package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestModerationCasesResetEvidenceAndKeepPermissionsReadOnly(t *testing.T) {
	previousModeration, previousImported, previousBound, previousDeployed := moderationFixture, imported, bound, deployed
	previousEnabled, previousStatus := runInspectionEnabled, runInspectionStatus
	previousFailure, previousPhase := runInspectionFailure, runInspectionPhase
	previousHistory, previousChanged := runInspectionHistory, runInspectionDeploymentChanged
	t.Cleanup(func() {
		moderationFixture, imported, bound, deployed = previousModeration, previousImported, previousBound, previousDeployed
		runInspectionEnabled, runInspectionStatus = previousEnabled, previousStatus
		runInspectionFailure, runInspectionPhase = previousFailure, previousPhase
		runInspectionHistory, runInspectionDeploymentChanged = previousHistory, previousChanged
	})
	moderationFixture = false
	rolePath := "/api/organizations/acme/roles/admin"
	require.False(t, handleModerationAPI(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, rolePath, nil)))
	app := fiber.New()
	registerModerationFixture(app)
	for _, tc := range []struct {
		state      string
		configured bool
		status     string
	}{
		{"healthy", true, "succeeded"},
		{"failed", true, "failed"},
		{"unconfigured", false, "succeeded"},
		{"healthy", true, "succeeded"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			runInspectionFailure, runInspectionPhase = "previous failure", "previous phase"
			runInspectionHistory, runInspectionDeploymentChanged = true, true
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/_test/moderation/"+tc.state, nil))
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusNoContent, response.StatusCode)
			require.True(t, moderationFixture)
			require.True(t, imported)
			require.Equal(t, tc.configured, bound)
			require.Equal(t, tc.configured, deployed)
			require.Equal(t, tc.configured, runInspectionEnabled)
			require.Equal(t, tc.status, runInspectionStatus)
			require.Empty(t, runInspectionFailure)
			require.Empty(t, runInspectionPhase)
			require.False(t, runInspectionHistory)
			require.False(t, runInspectionDeploymentChanged)

			role := httptest.NewRecorder()
			handleAPI(role, httptest.NewRequestWithContext(t.Context(), http.MethodGet, rolePath, nil))
			require.JSONEq(t, `{"slug":"admin","permissions":[{"resource":"*","action":"read"}]}`, role.Body.String())
			deployment := httptest.NewRecorder()
			handleAPI(deployment, httptest.NewRequestWithContext(t.Context(), http.MethodGet, prefix+"/environments/dev/pipeline-deployments", nil))
			want := `{"deployments":[]}`
			if tc.configured {
				want = `{"deployments":[{"pipeline_slug":"daily-sync","environment_slug":"dev","current_version_spec_hash":"` + version + `","current_version_short_spec_hash":"` + version[:12] + `"}]}`
			}
			require.JSONEq(t, want, deployment.Body.String())
		})
	}

	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/_test/moderation/unknown", nil))
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.True(t, deployed)
	require.Equal(t, "succeeded", runInspectionStatus)
	require.False(t, handleModerationAPI(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/organizations/other/roles/admin", nil)))
}
