package googleworkspace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const metadata = `{"spreadsheetId":"book","properties":{"title":"Budget"},"sheets":[{"properties":{"sheetId":0,"title":"Renamed ' Tab","gridProperties":{"rowCount":100,"columnCount":10}}}]}`

func testConnector(t *testing.T, handle roundTripper) *Connector {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "www.googleapis.com" && strings.Contains(r.URL.Path, "/drive/v3/files/book") {
			return response(`{"id":"book","name":"Budget","mimeType":"application/vnd.google-apps.spreadsheet","isAppAuthorized":true,"capabilities":{"canEdit":true,"canDownload":true}}`), nil
		}
		return handle(r)
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	conn, err := New(t.Context(), workspace.Config{}, provider.Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
	require.NoError(t, err)
	return conn
}

func TestReadFreshWorksheetAndPaddedScalars(t *testing.T) {
	metadataCalls, readCalls := 0, 0
	conn := testConnector(t, func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/values/") {
			metadataCalls++
			return response(metadata), nil
		}
		readCalls++
		require.Equal(t, "/v4/spreadsheets/book/values/'Renamed '' Tab'!A1:C5", r.URL.Path)
		require.Equal(t, "UNFORMATTED_VALUE", r.URL.Query().Get("valueRenderOption"))
		require.Equal(t, "SERIAL_NUMBER", r.URL.Query().Get("dateTimeRenderOption"))
		return response(`{"values":[[" name ","amount","date"],["=literal",true,45200],[],[7,3.5]]}`), nil
	})
	raw := json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:C5"}`)
	for i := 0; i < 2; i++ {
		res, err := conn.Execute(t.Context(), raw, nil)
		require.NoError(t, err)
		require.NoError(t, res.Error())
		rows, ok := res.ObjectRows()
		require.True(t, ok)
		require.Equal(t, []string{"name", "amount", "date"}, rows.ColumnNames)
		require.Equal(t, []map[string]any{{"name": "=literal", "amount": true, "date": float64(45200)}, {"name": nil, "amount": nil, "date": nil}, {"name": float64(7), "amount": 3.5, "date": nil}, {"name": nil, "amount": nil, "date": nil}}, rows.Rows)
	}
	require.Equal(t, 2, metadataCalls)
	require.Equal(t, 2, readCalls)
}

func TestInvalidReadTargetsAndHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, meta, values, raw string
		limits                  types.ExecutionLimits
		want                    error
		publicDetail            string
	}{
		{name: "missing worksheet", meta: `{"spreadsheetId":"book","sheets":[]}`, want: xerror.ValidationFailed()},
		{name: "grid bound", meta: strings.Replace(metadata, `"rowCount":100`, `"rowCount":1`, 1), want: xerror.ValidationFailed()},
		{name: "blank header", values: `{"values":[["a"," "]]}`, want: xerror.ValidationFailed(), publicDetail: "Headers must be nonblank unique strings"},
		{name: "duplicate trimmed header", values: `{"values":[["a"," a "]]}`, want: xerror.ValidationFailed(), publicDetail: "Headers must be nonblank unique strings"},
		{name: "numeric header", values: `{"values":[["a",1]]}`, want: xerror.ValidationFailed(), publicDetail: "Headers must be nonblank unique strings"},
		{name: "missing header", values: `{"values":[["a"]]}`, want: xerror.ValidationFailed(), publicDetail: "Every selected column must have a string header"},
		{name: "empty", values: `{"values":[]}`, want: xerror.ValidationFailed(), publicDetail: "Every selected column must have a string header"},
		{name: "outside response", values: `{"values":[["a","b"],[1,2,3]]}`, want: xerror.ExecutionFailed()},
		{name: "row limit", limits: types.ExecutionLimits{MaxResultRows: 1}, raw: `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:B3"}`, want: xerror.ExecutionRowLimitExceeded()},
		{name: "padding limit before dispatch", limits: types.ExecutionLimits{MaxResponseBytes: 40}, raw: `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:B100"}`, want: xerror.ExecutionResponseSizeExceeded()},
		{name: "long headers padding limit", values: `{"values":[["long-header-one","long-header-two"]]}`, limits: types.ExecutionLimits{MaxResponseBytes: 400}, raw: `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:B20"}`, want: xerror.ExecutionResponseSizeExceeded()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := testConnector(t, func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/values/") {
					return response(tc.values), nil
				}
				m := tc.meta
				if m == "" {
					m = metadata
				}
				return response(m), nil
			})
			raw := tc.raw
			if raw == "" {
				raw = `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:B2"}`
			}
			res, err := conn.Execute(t.Context(), json.RawMessage(raw), nil, tc.limits)
			require.NoError(t, err)
			require.ErrorIs(t, res.Error(), tc.want)
			require.Equal(t, tc.publicDetail, xerror.PublicDetail(res.Error()))
			if tc.publicDetail != "" {
				require.ErrorContains(t, res.Error(), tc.publicDetail)
			}
		})
	}
}

func TestUpdateLiteralAndBoundRows(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "literal", true: "bound"}[bound], func(t *testing.T) {
			writes := 0
			conn := testConnector(t, func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "/values/") {
					return response(metadata), nil
				}
				writes++
				require.Equal(t, http.MethodPut, r.Method)
				require.Equal(t, "RAW", r.URL.Query().Get("valueInputOption"))
				require.Equal(t, "/v4/spreadsheets/book/values/'Renamed '' Tab'!B2:C3", r.URL.Path)
				var body struct {
					Range          string
					MajorDimension string
					Values         [][]any
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, "ROWS", body.MajorDimension)
				require.Equal(t, [][]any{{"=SUM(A1:A3)", ""}, {true, float64(3)}}, body.Values)
				return response(`{}`), nil
			})
			rows := `[{"a":"=SUM(A1:A3)","b":null,"ignored":{"nested":1}},{"a":true,"b":3}]`
			params := map[string]types.TypedParam{}
			if bound {
				var input []map[string]any
				require.NoError(t, json.Unmarshal([]byte(rows), &input))
				params["input"] = types.TypedParam{Value: input}
				rows = `"{{input}}"`
			}
			res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":0,"range":"B2:C3","columns":["a","b"],"rows":`+rows+`}`), params)
			require.NoError(t, err)
			require.NoError(t, res.Error())
			require.Equal(t, 1, writes)
			output, _ := res.ObjectRows()
			require.Equal(t, "update", output.Operation)
			require.Equal(t, 2, output.RowCount)
		})
	}
}

func TestUpdateRejectsBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, rows string
		params     map[string]types.TypedParam
		limits     types.ExecutionLimits
		want       error
	}{
		{name: "missing mapped key", rows: `[{"a":1}, {"a":2,"b":3}]`, want: xerror.ValidationFailed()},
		{name: "nested", rows: `[{"a":{},"b":1},{"a":2,"b":3}]`, want: xerror.ValidationFailed()},
		{name: "array", rows: `[{"a":[],"b":1},{"a":2,"b":3}]`, want: xerror.ValidationFailed()},
		{name: "height", rows: `[{"a":1,"b":2}]`, want: xerror.ValidationFailed()},
		{name: "missing input", rows: `"{{input}}"`, want: xerror.ValidationFailed()},
		{name: "input wrong height", rows: `"{{input}}"`, params: map[string]types.TypedParam{"input": {Value: []any{map[string]any{"a": 1, "b": 2}}}}, want: xerror.ValidationFailed()},
		{name: "read only selected grant", rows: `[{"a":1,"b":2},{"a":2,"b":3}]`, limits: types.ExecutionLimits{ReadOnly: true}, want: xerror.DataSourceReadOnly()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			conn := testConnector(t, func(*http.Request) (*http.Response, error) { calls++; return response(metadata), nil })
			res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":0,"range":"B2:C3","columns":["a","b"],"rows":`+tc.rows+`}`), tc.params, tc.limits)
			require.NoError(t, err)
			require.ErrorIs(t, res.Error(), tc.want)
			require.Zero(t, calls)
		})
	}
}

func TestExecutionDeadlineCoversMetadataAndValues(t *testing.T) {
	conn := testConnector(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/values/") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return response(metadata), nil
	})
	res, err := conn.Execute(context.Background(), json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:A2"}`), nil, types.ExecutionLimits{ConnectorTimeout: time.Millisecond})
	require.NoError(t, err)
	require.ErrorIs(t, res.Error(), xerror.Timeout(xerror.DataSource))
}
