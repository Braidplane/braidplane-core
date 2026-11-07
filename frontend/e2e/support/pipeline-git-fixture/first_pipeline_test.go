package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestFirstPipelineFixtureKeepsRetryAndImportStatesTruthful(t *testing.T) {
	previousEnabled, previousRestricted, previousImported := firstPipelineEnabled, firstPipelineRestricted, firstPipelineImported
	previousStatus, previousSpec, previousRequests := firstPipelineStatus, firstPipelineSpec, firstPipelineRequests
	t.Cleanup(func() {
		firstPipelineEnabled, firstPipelineRestricted, firstPipelineImported = previousEnabled, previousRestricted, previousImported
		firstPipelineStatus, firstPipelineSpec, firstPipelineRequests = previousStatus, previousSpec, previousRequests
	})
	firstPipelineEnabled, firstPipelineRestricted, firstPipelineImported = false, false, false
	firstPipelineStatus, firstPipelineSpec, firstPipelineRequests = "publish_queued", api.PipelineSpec{}, nil
	app := fiber.New()
	registerFirstPipelineFixture(app)
	setState := func(state string) {
		t.Helper()
		response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/_test/first-pipeline/"+state, nil))
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		require.True(t, handleFirstPipelineAPI(response, httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))))
		return response
	}
	collection := prefix + "/pipeline-creations"
	progress := collection + "/creation-one"
	versionPath := prefix + "/pipelines/recent-orders-pipeline/versions/" + version
	input := `{"metadata":{"slug":"recent-orders-pipeline","name":"First pipeline"},"steps":[{"kind":"query","slug":"fetch","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"orders"}}]}`
	setState("start")
	empty := request(http.MethodGet, collection, "")
	var page api.PipelineCreationProposalListResponse
	require.NoError(t, json.Unmarshal(empty.Body.Bytes(), &page))
	require.Empty(t, page.PipelineCreations)
	created := request(http.MethodPost, collection, input)
	require.Equal(t, http.StatusOK, created.Code)
	var proposal api.PipelineCreationProposal
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &proposal))
	require.Equal(t, api.CreationPublishQueued, proposal.Status)
	setState("failed")
	failed := request(http.MethodGet, progress, "")
	require.NoError(t, json.Unmarshal(failed.Body.Bytes(), &proposal))
	require.Equal(t, api.CreationPublishFailed, proposal.Status)
	retried := request(http.MethodPost, collection, input)
	require.Equal(t, http.StatusOK, retried.Code)
	require.NoError(t, json.Unmarshal(retried.Body.Bytes(), &proposal))
	require.Equal(t, api.CreationPublishQueued, proposal.Status)

	changed := request(http.MethodPost, collection, strings.Replace(input, "First pipeline", "Changed definition", 1))
	require.Equal(t, http.StatusConflict, changed.Code)
	listed := request(http.MethodGet, collection, "")
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &page))
	require.Len(t, page.PipelineCreations, 1)
	require.Equal(t, "First pipeline", page.PipelineCreations[0].Spec.Metadata.Name)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, versionPath, "").Code)
	setState("open")
	opened := request(http.MethodGet, progress, "")
	require.NoError(t, json.Unmarshal(opened.Body.Bytes(), &proposal))
	require.NotNil(t, proposal.ProposalUrl)
	require.Equal(t, api.CreationProposalOpen, proposal.Status)
	setState("imported")
	imported := request(http.MethodGet, versionPath, "")
	require.Equal(t, http.StatusOK, imported.Code)
	var retained api.PipelineVersion
	require.NoError(t, json.Unmarshal(imported.Body.Bytes(), &retained))
	require.Equal(t, version, retained.SpecHash)
	require.Equal(t, proposal.PipelineSlug, retained.PipelineSlug)
	alreadyOpen := request(http.MethodPost, collection, input)
	require.NoError(t, json.Unmarshal(alreadyOpen.Body.Bytes(), &proposal))
	require.Equal(t, api.CreationProposalOpen, proposal.Status)
	setState("restricted")
	require.Equal(t, http.StatusForbidden, request(http.MethodPost, collection, input).Code)
	otherProject := httptest.NewRecorder()
	require.False(t, handleFirstPipelineAPI(otherProject, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/organizations/other/projects/ops/pipeline-creations", nil)))
}
