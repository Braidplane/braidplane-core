package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordUpdateWithholdsUnconfirmedResponses(t *testing.T) {
	const operation = `{"type":"http","method":"PATCH","path":"/crm/v3/objects/companies/{{record_id}}","body":{"properties":"{{input}}"}}`
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"wrong company", 200, `{"id":"other","properties":{"name":"sensitive"}}`},
		{"missing property", 200, `{"id":"record/one","properties":{}}`},
		{"conflicting acknowledgement", 200, `{"id":"record/one","properties":{"name":"other"}}`},
		{"excess bytes", 200, strings.Repeat("a", 151)},
		{"credential echo", 200, `{"id":"record/one","properties":{"name":"sensitive"},"token":"fixture-secret"}`},
		{"redirect", 307, ""},
		{"transient failure", 503, "sensitive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/base/crm/v3/objects/companies/record%2Fone", r.URL.EscapedPath())
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			c, err := New(ctx, Config{BaseURL: server.URL + "/base", AllowPrivateHosts: true}, Credentials{BearerToken: "fixture-secret"})
			require.NoError(t, err)
			ctx = sinkbinding.WithHTTPRecordUpdate(sinkbinding.WithSinkExecution(ctx), "record/one", map[string]string{"name": "sensitive"}, false)
			response, err := c.Execute(ctx, []byte(operation), nil, types.ExecutionLimits{HTTPMaxResponseBytes: 150, HTTPRetryAttempts: 3})
			require.NoError(t, err)
			require.ErrorIs(t, response.Error(), xerror.HTTPAcknowledgementUncertain())
			require.NotContains(t, response.Error().Error(), "sensitive")
			require.NotContains(t, response.Error().Error(), "record/one")
			require.Equal(t, 1, calls)
		})
	}
}
