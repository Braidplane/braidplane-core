package googleworkspace

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeReadChecksIdentityAndBounds(t *testing.T) {
	client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"documentId":"other","tabs":[]}`))
	})
	client.docs.BasePath = server.URL + "/docs/"
	_, err := client.ReadNative(t.Context(), "document", "native", Limits{})
	require.ErrorIs(t, err, xerror.ExecutionFailed())
	_, err = client.ReadNative(t.Context(), "document", "native", Limits{MaxResponseBytes: 8})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestNativeEmptyReplacementIsSentLiterally(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		t.Run(resource, func(t *testing.T) {
			client, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					http.Error(w, "invalid request", 400)
					return
				}
				requests := body["requests"].([]any)
				if !assert.Len(t, requests, 1) {
					http.Error(w, "invalid requests", 400)
					return
				}
				replace := requests[0].(map[string]any)["replaceAllText"].(map[string]any)
				value, exists := replace["replaceText"]
				assert.True(t, exists)
				assert.Empty(t, value)
				idKey := "documentId"
				if resource == "presentation" {
					idKey = "presentationId"
				}
				_, _ = w.Write([]byte(`{"` + idKey + `":"native","replies":[{"replaceAllText":{}}]}`))
			})
			client.docs.BasePath, client.slides.BasePath = server.URL+"/docs/", server.URL+"/slides/"
			changed, err := client.ReplaceNative(t.Context(), resource, "native", "{{literal}}", "", Limits{})
			require.NoError(t, err)
			require.EqualValues(t, 0, changed)
		})
	}
}
