package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type provider func(*http.Request) (*http.Response, error)

func (p provider) RoundTrip(req *http.Request) (*http.Response, error) { return p(req) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestRestrictedKeyAndAccountSelection(t *testing.T) {
	for _, tc := range []struct {
		mode, key string
		ok        bool
	}{
		{"test", "rk_test_fixture", true},
		{"live", "rk_live_fixture", true},
		{"live", "rk_test_fixture", false},
		{"test", "rk_live_fixture", false},
		{"test", "sk_test_fixture", false},
		{"live", "pk_live_fixture", false},
		{"test", "rk_test_", false},
		{"other", "rk_other_fixture", false},
		{"test", "rk_test_x\nsecret", false},
	} {
		_, err := New(t.Context(), Config{Mode: tc.mode}, Credentials{APIKey: tc.key})
		if tc.ok {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.key)
		}
	}
	for _, tenant := range []string{"a", "b"} {
		key := "rk_test_fixture_" + tenant
		c, err := New(t.Context(), Config{Mode: "test"}, Credentials{APIKey: key})
		require.NoError(t, err)
		c.transport = provider(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "Bearer "+key, req.Header.Get("Authorization"))
			require.Empty(t, req.Header.Get("Stripe-Account"))
			require.Empty(t, req.Header.Get("X-Stripe-Client-Telemetry"))
			require.NotContains(t, req.Header.Get("X-Stripe-Client-User-Agent"), "telemetry_id")
			require.Equal(t, "https://api.stripe.com/v1/customers?limit=1", req.URL.String())
			return response(200, `{"object":"list","data":[],"has_more":false}`), nil
		})
		require.NoError(t, c.TestConnection(t.Context()))
		require.NoError(t, c.Close())
	}
}

func TestReadsAndSchema(t *testing.T) {
	c, err := New(t.Context(), Config{Mode: "test"}, Credentials{APIKey: "rk_test_fixture"})
	require.NoError(t, err)
	schema, err := c.IntrospectSchema(t.Context())
	require.NoError(t, err)
	require.Len(t, schema.Objects, 4)
	for i, r := range resources {
		t.Run(r.name, func(t *testing.T) {
			var calls int
			c.transport = provider(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, "api.stripe.com", req.URL.Host)
				require.Equal(t, r.path, req.URL.Path)
				require.Equal(t, "2", req.URL.Query().Get("limit"))
				if r.name == "subscriptions" {
					require.Equal(t, "all", req.URL.Query().Get("status"))
				}
				switch calls {
				case 1:
					require.Empty(t, req.URL.Query().Get("starting_after"))
					body := fmt.Sprintf(`{"object":"list","data":[{"id":%q,"created":123,"livemode":false,"amount":9007199254740993,"client_secret":"synthetic-private","metadata":{"private":true}}],"has_more":true}`, r.prefix+"first")
					return response(200, body), nil
				case 2:
					require.Equal(t, r.prefix+"first", req.URL.Query().Get("starting_after"))
					return response(200, `{"object":"list","data":[],"has_more":false}`), nil
				default:
					t.Fatal("unexpected pagination or retry")
					return nil, xerror.Unexpected()
				}
			})
			raw := json.RawMessage(fmt.Sprintf(`{"type":"stripe","object_type":%q,"operation":"list","starting_after":"{{cursor}}","limit":3}`, r.name))
			require.True(t, IsReadStatement(raw))
			cursor := ""
			for page := 1; page <= 2; page++ {
				out, err := c.Execute(t.Context(), raw, map[string]types.TypedParam{"cursor": {Value: cursor}}, types.ExecutionLimits{MaxResultRows: 2})
				require.NoError(t, err)
				require.NoError(t, out.Error())
				rows, ok := out.ObjectRows()
				require.True(t, ok)
				require.Equal(t, schema.Objects[i].Name, rows.ObjectType)
				for j, f := range schema.Objects[i].Properties {
					require.Equal(t, f.Name, rows.ColumnNames[j])
				}
				if page == 1 {
					require.Equal(t, 1, rows.RowCount)
					require.Equal(t, int64(123), rows.Rows[0]["created"])
					require.NotContains(t, rows.Rows[0], "client_secret")
					require.NotContains(t, rows.Rows[0], "metadata")
					if r.name == "payments" {
						require.Equal(t, int64(9007199254740993), rows.Rows[0]["amount"])
					}
					require.True(t, rows.HasMore)
					require.NotNil(t, rows.NextCursor)
					cursor = *rows.NextCursor
				} else {
					require.Zero(t, rows.RowCount)
					require.False(t, rows.HasMore)
					require.Nil(t, rows.NextCursor)
				}
			}
			require.Equal(t, 2, calls)
		})
	}
	_, err = c.IntrospectSchema(schemaquota.WithQuota(t.Context(), 1, 100))
	require.Error(t, err)
}

func TestProviderErrorsAndLimits(t *testing.T) {
	const secret = "synthetic-sensitive-body"
	for _, tc := range []struct {
		status int
		body   string
		budget int64
		want   error
	}{
		{401, secret, 100, xerror.InvalidCredentials()},
		{403, secret, 100, xerror.Forbidden(xerror.DataSource)},
		{429, secret, 100, xerror.Unavailable(xerror.DataSource)},
		{302, secret, 100, xerror.ConnectionFailed(xerror.DataSource)},
		{500, secret, 100, xerror.ConnectionFailed(xerror.DataSource)},
		{200, strings.Repeat(secret, 10), 16, xerror.ExecutionResponseSizeExceeded()},
		{200, `{"object":"list","has_more":false,"data":[{},{}]}`, 100, xerror.ExecutionRowLimitExceeded()},
		{200, `{"object":"list","has_more":false,"data":[{"id":"cus_x","livemode":true}]}`, 100, xerror.ConnectionFailed(xerror.DataSource)},
		{200, `{"object":"list","has_more":true,"data":[]}`, 100, xerror.ConnectionFailed(xerror.DataSource)},
		{200, secret, 100, xerror.ConnectionFailed(xerror.DataSource)},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			calls := 0
			c := &Connector{key: "rk_test_fixture", transport: provider(func(*http.Request) (*http.Response, error) { calls++; return response(tc.status, tc.body), nil })}
			out, err := c.Execute(t.Context(), json.RawMessage(`{"object_type":"customers","limit":1}`), nil, types.ExecutionLimits{HTTPMaxResponseBytes: tc.budget})
			require.NoError(t, err)
			require.ErrorIs(t, out.Error(), tc.want)
			require.NotContains(t, out.Error().Error(), secret)
			require.NotContains(t, out.Error().Error(), c.key)
			require.Equal(t, 1, calls)
		})
	}
}

func TestStatementAndCancellation(t *testing.T) {
	for _, raw := range []string{
		`{"object_type":"refunds"}`, `{"object_type":"customers","operation":"delete"}`,
		`{"object_type":"customers","limit":101}`, `{"object_type":"customers","limit":0}`,
		`{"object_type":"customers","starting_after":"pi_wrong"}`, `{"object_type":"customers","account":"acct_other"}`,
		`{"object_type":"customers"}{}`, `{"type":"http","object_type":"customers"}`,
	} {
		require.False(t, IsReadStatement(json.RawMessage(raw)))
	}
	ctx, cancel := context.WithCancel(t.Context())
	c := &Connector{key: "rk_test_fixture", transport: provider(func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	out, err := c.Execute(ctx, json.RawMessage(`{"object_type":"customers"}`), nil)
	require.NoError(t, err)
	require.ErrorIs(t, out.Error(), context.Canceled)
	_, err = c.IntrospectSchema(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRetainedBudgetAndInvalidCursorBindings(t *testing.T) {
	calls := 0
	c := &Connector{key: "rk_test_fixture", transport: provider(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"object":"list","has_more":false,"data":[]}`), nil
	})}
	raw := json.RawMessage(`{"object_type":"customers","starting_after":"{{cursor}}"}`)
	for _, params := range []map[string]types.TypedParam{
		nil, {"cursor": {Value: 1}}, {"cursor": {Value: "pi_wrong"}},
	} {
		out, err := c.Execute(t.Context(), raw, params)
		require.NoError(t, err)
		require.ErrorIs(t, out.Error(), xerror.InvalidInput(xerror.Query))
	}
	require.Zero(t, calls)
	out, err := c.Execute(t.Context(), raw, map[string]types.TypedParam{"cursor": {Value: ""}}, types.ExecutionLimits{MaxResponseBytes: 64})
	require.NoError(t, err)
	require.ErrorIs(t, out.Error(), xerror.ExecutionResponseSizeExceeded())
	require.Equal(t, 1, calls)
}
