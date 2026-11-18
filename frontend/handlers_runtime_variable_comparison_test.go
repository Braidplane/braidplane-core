package frontend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func TestRuntimeVariableComparisonUsesSelectedVersionAndSavesDefinition(t *testing.T) {
	t.Parallel()

	const org = "acme-hq"
	const pipeline = "orders"
	const prefix = "/api/organizations/" + org
	var spec api.PipelineSpec
	require.NoError(t, json.Unmarshal([]byte(`{
		"metadata":{"slug":"orders"},
		"steps":[{"kind":"query","slug":"source","query":{"parameters":{"limit":"{{runtime.env.batch_size}}"}}}]
	}`), &spec))
	comparisonURL := views.RuntimeVariableComparisonHref(org, "default", pipeline, bindingSetupHash)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case prefix + "/projects/default/pipelines/orders/versions/" + bindingSetupHash:
			assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{
				PipelineSlug: pipeline, SpecHash: bindingSetupHash, Spec: spec,
			}))
		case prefix + "/runtime-variable-comparison":
			assert.Equal(t, []string{"batch_size"}, r.URL.Query()["used_key"])
			writeJSON(w, `{"environments":["prod"],"rows":[{"key":"batch_size","used":true,"cells":[{"source":"missing","required":true,"type_mismatch":false,"blocked":true}]}]}`)
		case prefix + "/runtime-variable-definitions/batch_size":
			assert.Equal(t, http.MethodPut, r.Method)
			var definition api.UpsertRuntimeVariableDefinition
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&definition))
			assert.Equal(t, api.RuntimeVariableValueTypeInteger, definition.ValueType)
			assert.Equal(t, new("200"), definition.DefaultValue)
			assert.Equal(t, &[]api.Slug{"prod"}, definition.ProtectedEnvironmentConfirmations)
			writeJSON(w, `{"key":"batch_size","value_type":"integer","required":false,"default_value":"200"}`)
		default:
			writeProjectBindingReloadFixture(w, r)
		}
	}))
	defer server.Close()

	app, cookie := setupBindingActionTestApp(t, server.URL)
	get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, comparisonURL, nil)
	get.Header.Set("Cookie", cookie)
	response, err := app.Test(get)
	require.NoError(t, err)
	page, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(page), "Missing required value")
	require.Contains(t, string(page), "batch_size")

	form := url.Values{
		"key": {"batch_size"}, "value_type": {"integer"}, "has_default": {"1"}, "default_value": {"200"},
		"protected_environment_confirmations": {"prod"},
	}
	post := httptest.NewRequestWithContext(t.Context(), http.MethodPost, comparisonURL+"&define=1", strings.NewReader(form.Encode()))
	post.Header.Set("Cookie", cookie)
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addCSRFToken(t, app, post)
	response, err = app.Test(post)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Equal(t, comparisonURL, response.Header.Get("Location"))
}
