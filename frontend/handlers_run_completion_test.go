package frontend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletionPreviewLoadsAllCurrentSubscriptions(t *testing.T) {
	t.Parallel()
	for _, brokenCursor := range []bool{false, true} {
		t.Run(fmt.Sprint(brokenCursor), func(t *testing.T) {
			t.Parallel()
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/completion") {
					_, _ = w.Write([]byte(`{"root_run_slug":"root","closed":true,"outcome":"succeeded","attempts":[],"children":[]}`))
					return
				}
				assert.True(t, strings.HasSuffix(r.URL.Path, "/pipeline-triggers"))
				pages++
				if pages == 1 {
					if brokenCursor {
						_, _ = w.Write([]byte(`{"pipeline_triggers":[],"has_more":true}`))
						return
					}
					_, _ = w.Write([]byte(`{"pipeline_triggers":[],"has_more":true,"next_cursor":"page-2"}`))
					return
				}
				assert.Equal(t, "page-2", r.URL.Query().Get("cursor"))
				_, _ = w.Write([]byte(`{"pipeline_triggers":[{"slug":"after-root","type":"completion","pipeline_slug":"child","completion":{"upstream_pipeline_slug":"upstream","condition":"success","execution_grant_enabled":true}},{"slug":"unrelated","type":"completion","pipeline_slug":"other","completion":{"upstream_pipeline_slug":"other","condition":"success","execution_grant_enabled":true}}],"has_more":false}`))
			}))
			t.Cleanup(server.Close)
			client, err := api.NewClientWithResponses(server.URL)
			require.NoError(t, err)
			router := &Router{apiClient: client}
			result := router.loadPipelineRunCompletion(context.Background(), "", "acme", "etl", "dev", "root", "upstream")
			require.NotNil(t, result.Evidence)
			if brokenCursor {
				require.False(t, result.CurrentSubscriptionsLoaded)
				require.Empty(t, result.CurrentSubscriptions)
				return
			}
			require.Equal(t, 2, pages)
			require.True(t, result.CurrentSubscriptionsLoaded)
			require.Len(t, result.CurrentSubscriptions, 1)
			require.Equal(t, "after-root", result.CurrentSubscriptions[0].Slug)
		})
	}
}
