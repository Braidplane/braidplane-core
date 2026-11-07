package pipelinesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestService_ExecuteGoogleWorkspaceTransformedPipeline(t *testing.T) {
	for _, direction := range []string{"sheets-to-sheets", "postgres-to-sheets", "sheets-to-postgres"} {
		t.Run(direction, func(t *testing.T) {
			// The provider constructor captures http.DefaultTransport. This test must stay serial.
			originalTransport := http.DefaultTransport
			defer func() { http.DefaultTransport = originalTransport }()

			grid := [][]any{{"=1+1", "B1", "C1", "D1", "E1"}, {"=SUM(B2:C2)", "B2", "C2", "old D2", "old E2"}, {"A3", "B3", "C3", "old D3", "old E3"}, {"A4", "B4", "C4", "D4", "E4"}}
			var updateCalls, sqlCalls int
			before := make([][]any, len(grid))
			for i := range grid {
				before[i] = append([]any(nil), grid[i]...)
			}
			http.DefaultTransport = roundTripper(func(request *http.Request) (*http.Response, error) {
				response := func(status int, body string) (*http.Response, error) {
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body)), Request: request}, nil
				}
				switch request.URL.Path {
				case "/drive/v3/files/source-book", "/drive/v3/files/sink-book":
					return response(http.StatusOK, `{"id":"`+strings.TrimPrefix(request.URL.Path, "/drive/v3/files/")+`","mimeType":"application/vnd.google-apps.spreadsheet","isAppAuthorized":true}`)
				case "/v4/spreadsheets/source-book", "/v4/spreadsheets/sink-book":
					return response(http.StatusOK, `{"spreadsheetId":"`+strings.TrimPrefix(request.URL.Path, "/v4/spreadsheets/")+`","properties":{"title":"Sheet"},"sheets":[{"properties":{"sheetId":0,"title":"Sheet","gridProperties":{"rowCount":10,"columnCount":10}}}]}`)
				case "/v4/spreadsheets/source-book/values/'Sheet'!A1:B3":
					require.Equal(t, http.MethodGet, request.Method)
					return response(http.StatusOK, `{"values":[["name","score"],["Ada",3],["Ben",null]]}`)
				case "/v4/spreadsheets/sink-book/values/'Sheet'!D2:E3":
					require.Equal(t, http.MethodPut, request.Method)
					require.Equal(t, "RAW", request.URL.Query().Get("valueInputOption"))
					require.Equal(t, "false", request.URL.Query().Get("includeValuesInResponse"))
					var body struct {
						Values [][]any `json:"values"`
					}
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					require.Equal(t, [][]any{{"Ada", float64(3)}, {"Ben", ""}}, body.Values)
					grid[1][3], grid[1][4] = body.Values[0][0], body.Values[0][1]
					grid[2][3], grid[2][4] = body.Values[1][0], body.Values[1][1]
					updateCalls++
					return response(http.StatusOK, `{}`)
				default:
					t.Fatalf("unexpected Google request: %s %s", request.Method, request.URL.String())
					return nil, xerror.Unexpected()
				}
			})

			tempDir := t.TempDir()
			pipeline := pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{Slug: "sheet-copy", Name: "Sheet Copy"},
				Steps: []pipelinespec.Step{
					{Kind: "query", Slug: "read-source", Query: &pipelinespec.QueryRef{
						DataSourceSlug: "source-sheet", OperationType: "google_workspace",
						OperationInline: map[string]any{"type": "google_workspace", "resource": "spreadsheet", "spreadsheet_id": "source-book", "operation": "read", "worksheet_id": 0, "range": "A1:B3"},
						OutputContract:  &payload.Contract{Fields: []payload.ContractField{{Name: "name", Type: model.ContractFieldTypeValueString, Required: true}, {Name: "score", Type: model.ContractFieldTypeValueNumber, Required: false, Nullable: true}}},
					}},
					{Kind: "transform", Slug: "rename-customer", Transform: &pipelinespec.TransformSpec{
						Inputs:         map[string]pipelinespec.StepInput{"input": {FromStep: "read-source"}},
						SQL:            "SELECT name AS customer, score FROM input ORDER BY customer",
						OutputKind:     model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{Name: "customer", Type: model.ContractFieldTypeValueString, Required: true}, {Name: "score", Type: model.ContractFieldTypeValueNumber, Required: false, Nullable: true}}},
					}},
					{Kind: "sink", Slug: "write-sink", Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
						Input:            &pipelinespec.StepInput{FromStep: "rename-customer"}, DataSourceSlug: "sink-sheet", OperationType: "google_workspace",
						OperationInline: map[string]any{"type": "google_workspace", "resource": "spreadsheet", "spreadsheet_id": "sink-book", "operation": "update", "worksheet_id": 0, "range": "D2:E3", "columns": []any{"customer", "score"}, "rows": "{{input}}"},
					}},
				},
			}
			if direction == "postgres-to-sheets" {
				query := pipeline.Steps[0].Query
				query.DataSourceSlug, query.OperationType, query.OperationRef, query.OperationInline = "acme-postgres", "sql", "synthetic-rows", nil
			}
			if direction == "sheets-to-postgres" {
				sink := pipeline.Steps[2].Sink
				sink.DataSourceSlug, sink.OperationType, sink.OperationRef, sink.OperationInline = "acme-postgres", "sql", "upsert-synthetic", nil
				sink.IdempotencyClass = pipelinespec.SinkIdempotencyClassDeduplicatedByLedger
			}
			require.NoError(t, pipelinevalidate.Validate(&pipeline))
			encoded, err := json.Marshal(pipeline)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(tempDir, "sheet-copy.pipeline.json"), encoded, 0o600))

			queryExec := &fakeQueryExecutor{executeInlineFn: func(ctx context.Context, _, dsSlug slug.Slug, operation, params map[string]any) (*result.ExecutionResult, error) {
				limits := connectortypes.ExecutionLimits{ReadOnly: true}
				if dsSlug == must.Slug("sink-sheet") {
					limits = connectortypes.ExecutionLimits{}
				}
				client, err := connector.NewConnector(ctx, model.DataSourceTypeGoogleWorkspace, []byte(`{"access_mode":"selected_files"}`), []byte(`{"access_token":"test-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`))
				if err != nil {
					return nil, err
				}
				defer func() { _ = client.Close() }()
				raw, err := json.Marshal(operation)
				if err != nil {
					return nil, err
				}
				typed := make(map[string]connectortypes.TypedParam, len(params))
				for name, value := range params {
					typed[name] = connectortypes.TypedParam{Value: value, DataType: "text"}
				}
				return client.Execute(ctx, raw, typed, limits)
			}}

			// PostgreSQL is faked at the query-executor boundary; Google uses the real
			// adapter with deterministic HTTP. Live acceptance proves provider effects.
			queryExec.executeFn = func(_ context.Context, org, ds, query slug.Slug, _ model.EventActor, params map[string]any) (*result.ExecutionResult, error) {
				require.Equal(t, must.Slug("acme"), org)
				require.Equal(t, must.Slug("acme-postgres"), ds)
				sqlCalls++
				if query == must.Slug("synthetic-rows") {
					require.Empty(t, params)
					return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"name": "Ada", "score": float64(3)}, {"name": "Ben", "score": nil}}, ColumnNames: []string{"name", "score"}, RowCount: 2}, 1, nil), nil
				}
				require.Equal(t, must.Slug("upsert-synthetic"), query)
				rows, ok := params[sinkbinding.InternalInputRows].([]map[string]any)
				require.True(t, ok)
				encoded, err := json.Marshal(rows)
				require.NoError(t, err)
				require.JSONEq(t, `[{"customer":"Ada","score":3},{"customer":"Ben","score":null}]`, string(encoded))
				return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 2}, 1, nil), nil
			}
			schemas := testCachedSchemas()
			schemas[must.Slug("acme-postgres")] = datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeSQL, Type: "postgres", Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "synthetic", Columns: []datasourcessvc.SchemaColumn{{Name: "name", Type: "text"}, {Name: "score", Type: "double precision"}}}}}}

			for _, dataSource := range []slug.Slug{must.Slug("source-sheet"), must.Slug("sink-sheet")} {
				schemas[dataSource] = datasourcessvc.Schema[datasourcessvc.SpreadsheetSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSpreadsheet, Type: "google_workspace",
					Payload: workspace.Metadata{SpreadsheetID: "book", Title: "Sheet", Worksheets: []workspace.Worksheet{{WorksheetID: 0, Title: "Sheet", RowCount: 10, ColumnCount: 10}}},
				}
			}
			svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(), newDirectoryPipelineStore(tempDir), &fakeDBTX{}, testutil.FakeTransactionManager{Tx: &fakeDBTX{}}, &fakeRunRepository{}, incrementalcheckpoints.New(), &fakeRunStore{}, &fakeDriftImpactRepository{}, queryExec, allowAuthorizationService{}, testAdmissionService(), &fakeDataSourceService{schemas: schemas}, fakeProjectBackendService{}, fakeGitSourceResolver{}, clock.New(), &recordingPipelineRunProducer{}, &recordingPipelineGitProducer{}, eventtest.NewProducer(&fakePipelineEventRecorder{}), &fakeDataSourceRefResolver{}, noopSinkEffectLedger{}, newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
			require.NoError(t, err)
			run, err := svc.Execute(context.Background(), must.Slug("acme"), defaultProjectSlug, must.Slug("prod"), must.Slug("sheet-copy"), nil, nil, model.UserEventActor(must.Slug("alice")), testRunSource(model.UserEventActor(must.Slug("alice"))), nil)
			require.NoError(t, err)
			assert.Equal(t, "succeeded", run.Metadata.Status)
			if direction == "sheets-to-postgres" {
				require.Equal(t, 0, updateCalls)
				require.Equal(t, before, grid)
			} else {
				require.Equal(t, 1, updateCalls)
				for row := range grid {
					for col := range grid[row] {
						if (row == 1 || row == 2) && (col == 3 || col == 4) {
							continue
						}
						require.Equal(t, before[row][col], grid[row][col], "outside cell row=%d column=%d", row, col)
					}
				}
				require.Equal(t, []any{"Ada", float64(3)}, grid[1][3:])
				require.Equal(t, []any{"Ben", ""}, grid[2][3:])
			}
			if direction == "sheets-to-sheets" {
				require.Zero(t, sqlCalls)
			} else {
				require.Equal(t, 1, sqlCalls)
			}
		})
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
