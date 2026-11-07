package queriessvc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	qrepo "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

func TestExecuteIncrementalInjectsCheckpointParamsForSavedQuery(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("postgres-main")
	querySlug := must.Slug("list-users-incremental")
	events := &fakeQueryEventRecorder{}
	queryRepo := incrementalQueryRepo(t, dsSlug, querySlug)
	dsService := incrementalDataSourceService(t, dsSlug, model.DataSourceTypePostgres)

	connectorFactory := func(
		_ context.Context,
		dsType model.DataSourceType,
		_,
		credentialsJSON []byte,
	) (connector.Connector, error) {
		require.Equal(t, model.DataSourceTypePostgres, dsType)
		require.JSONEq(t, `{"username":"svc-user","password":"secret-password"}`, string(credentialsJSON))

		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			_ context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			require.JSONEq(
				t,
				`{"sql":"SELECT id, updated_at FROM users WHERE updated_at > {{runtime.incremental.lower_bound.value}}"}`,
				string(queryData),
			)
			require.Equal(t, "active", params["status"].Value)
			require.Equal(t, "2026-05-24T10:00:00Z", params["runtime.incremental.lower_bound.value"].Value)
			require.Equal(t, "cursor-1", params["runtime.incremental.lower_bound.cursor"].Value)
			require.Equal(t, "2026-05-24T11:00:00Z", params["runtime.incremental.upper_bound.value"].Value)
			require.Equal(t, "cursor-2", params["runtime.incremental.upper_bound.cursor"].Value)

			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows: []map[string]any{{
						"id":         int64(1),
						"updated_at": "2026-05-24T10:30:00Z",
					}},
					ColumnNames: []string{"id", "updated_at"},
					RowCount:    1,
				},
				1,
				nil,
			), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestServiceWithEvents(queryRepo, dsService, nil, connectorFactory, events)
	require.NoError(t, err)

	_, observation, err := svc.ExecuteIncremental(
		context.Background(),
		orgSlug,
		dsSlug,
		querySlug,
		map[string]any{"status": "active"},
		incrementalServiceCheckpointInput(),
	)

	require.NoError(t, err)
	require.NotNil(t, observation)
	require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z","cursor":"cursor-2"}`, string(observation.ObservedUpperBound))
	require.Len(t, events.calls, 1)
	metadata := fmt.Sprint(events.calls[0].Metadata)
	require.Contains(t, metadata, "parameter_keys")
	require.NotContains(t, metadata, "secret-password")
	require.NotContains(t, metadata, "2026-05-24T10:00:00Z")
	require.NotContains(t, metadata, "2026-05-24T11:00:00Z")
	require.NotContains(t, metadata, "cursor-1")
	require.NotContains(t, metadata, "cursor-2")
}

func TestExecuteInlineIncrementalInjectsCheckpointParams(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	dsSlug := must.Slug("customer-api")
	dsService := incrementalDataSourceService(t, dsSlug, model.DataSourceTypeHTTP)

	connectorFactory := func(
		_ context.Context,
		dsType model.DataSourceType,
		_,
		credentialsJSON []byte,
	) (connector.Connector, error) {
		require.Equal(t, model.DataSourceTypeHTTP, dsType)
		require.JSONEq(t, `{"username":"svc-user","password":"secret-password"}`, string(credentialsJSON))

		mockConnector := testutil.NewFakeSQLConnector()
		mockConnector.ExecuteFn = func(
			_ context.Context,
			queryData json.RawMessage,
			params map[string]types.TypedParam,
		) (*result.ExecutionResult, error) {
			require.JSONEq(
				t,
				`{"method":"GET","path":"/users","query_params":{"cursor":"{{runtime.incremental.lower_bound.cursor}}"}}`,
				string(queryData),
			)
			require.Equal(t, "cursor-1", params["runtime.incremental.lower_bound.cursor"].Value)
			require.Nil(t, params["runtime.incremental.upper_bound.cursor"].Value)

			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       json.RawMessage(`{"items":[],"next_page_token":"cursor-3","secret_payload":"do-not-diagnose"}`),
			}, 1, nil), nil
		}
		return mockConnector, nil
	}

	svc, err := newTestService(&testutil.FakeQueryRepository{}, dsService, nil, connectorFactory)
	require.NoError(t, err)

	_, observation, err := svc.ExecuteInlineIncremental(
		context.Background(),
		orgSlug,
		dsSlug,
		map[string]any{
			"method":       "GET",
			"path":         "/users",
			"query_params": map[string]any{"cursor": "{{runtime.incremental.lower_bound.cursor}}"},
		},
		map[string]any{},
		func() *pipelinerun.IncrementalCheckpointInput {
			checkpoint := incrementalServiceCheckpointInput()
			checkpoint.Lookup.CheckpointKind = "cursor"
			checkpoint.Lookup.IncrementalCursor = "next_page_token"
			checkpoint.UpperBound = nil
			return checkpoint
		}(),
	)

	require.NoError(t, err)
	require.NotNil(t, observation)
	require.JSONEq(t, `{"cursor":"cursor-3"}`, string(observation.ObservedUpperBound))
	require.NotContains(t, string(observation.DisplayMetadata), "do-not-diagnose")
}

func incrementalQueryRepo(
	t *testing.T,
	dsSlug, querySlug slug.Slug,
) *testutil.FakeQueryRepository {
	t.Helper()

	queryRepo := &testutil.FakeQueryRepository{}
	queryRepo.FindBySlugFn = func(
		context.Context,
		postgres.DBTX,
		slug.Slug,
		slug.Slug,
		slug.Slug,
	) (*qrepo.Query, []*qrepo.QueryParameter, error) {
		return &qrepo.Query{
				Slug: querySlug,
				Name: "List Users Incremental",
				Data: []byte(
					`{"statement":"{\"sql\":\"SELECT id, updated_at FROM users WHERE updated_at > {{runtime.incremental.lower_bound.value}}\"}"}`,
				),
				DataSourceSlug: dsSlug,
			}, []*qrepo.QueryParameter{
				{
					Slug:     must.Slug("status"),
					Name:     "status",
					DataType: "text",
				},
			}, nil
	}
	queryRepo.CreateExecutionFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_, _, _ slug.Slug,
		params *qrepo.CreateExecutionParams,
	) (*qrepo.QueryExecution, error) {
		return &qrepo.QueryExecution{Slug: params.Slug}, nil
	}
	return queryRepo
}

func incrementalDataSourceService(
	t *testing.T,
	dsSlug slug.Slug,
	dsType model.DataSourceType,
) *fakes.DataSourceService {
	t.Helper()

	dsService := &fakes.DataSourceService{}
	dsService.FindBySlugFn = func(
		context.Context,
		slug.Slug,
		slug.Slug,
	) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{
			Slug:                 dsSlug,
			Type:                 dsType,
			IsActive:             true,
			Config:               json.RawMessage(`{}`),
			EncryptedCredentials: mustEncryptTestCredentials(t, `{"username":"svc-user","password":"secret-password"}`),
		}, nil
	}
	return dsService
}

func incrementalServiceCheckpointInput() *pipelinerun.IncrementalCheckpointInput {
	return &pipelinerun.IncrementalCheckpointInput{
		Lookup: pipelinerun.IncrementalCheckpointLookup{
			PipelineSlug:        must.Slug("users-sync"),
			SourceStepSlug:      must.Slug("fetch-users"),
			CheckpointNamespace: "normal",
			CheckpointKind:      "watermark",
			IncrementalField:    "updated_at",
		},
		LowerBound: json.RawMessage(`{"value":"2026-05-24T10:00:00Z","cursor":"cursor-1"}`),
		UpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z","cursor":"cursor-2"}`),
	}
}

func TestHTTPPaginationCheckpointObservation(t *testing.T) {
	for _, failSecond := range []bool{false, true} {
		t.Run(fmt.Sprint("fail-second=", failSecond), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Query().Get("after") == "cursor-1":
					_, _ = fmt.Fprint(w, `{"items":[1],"next":"page-2","checkpoint":"must-not-commit"}`)
				case failSecond:
					w.WriteHeader(http.StatusTooManyRequests)
				default:
					_, _ = fmt.Fprint(w, `{"items":[2],"next":null,"checkpoint":"batch-complete"}`)
				}
			}))
			defer server.Close()
			dsSlug := must.Slug("customer-api")
			factory := func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (connector.Connector, error) {
				return httpconnector.New(transportsecurity.WithPlaintext(ctx), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true}, httpconnector.Credentials{})
			}
			svc, err := newTestService(&testutil.FakeQueryRepository{}, incrementalDataSourceService(t, dsSlug, model.DataSourceTypeHTTP), nil, factory)
			require.NoError(t, err)
			checkpoint := incrementalServiceCheckpointInput()
			checkpoint.Lookup.CheckpointKind = "cursor"
			checkpoint.Lookup.IncrementalCursor = "checkpoint"
			checkpoint.LowerBound = json.RawMessage(`{"cursor":"cursor-1"}`)
			checkpoint.UpperBound = nil
			_, observation, err := svc.ExecuteInlineIncremental(context.Background(), must.Slug("acme"), dsSlug, map[string]any{
				"method": "GET", "path": "/items", "query_params": map[string]any{"after": "{{runtime.incremental.lower_bound.cursor}}"},
				"pagination": map[string]any{"cursor_param": "after", "records_path": "items", "next_cursor_path": "next", "max_pages": 3, "timeout_seconds": 2},
			}, nil, checkpoint)
			if failSecond {
				require.Contains(t, xerror.PublicDetail(err), "status 429")
				require.Nil(t, observation)
			} else {
				require.NoError(t, err)
				require.NotNil(t, observation)
				require.JSONEq(t, `{"cursor":"batch-complete"}`, string(observation.ObservedUpperBound))
			}
			require.JSONEq(t, `{"cursor":"cursor-1"}`, string(checkpoint.LowerBound))
		})
	}
}

func TestHTTPWatermarkPaginationStableWindow(t *testing.T) {
	for _, traversal := range []string{"cursor", "next_link"} {
		for _, execution := range []string{"inline", "saved", "snapshot"} {
			for _, outcome := range []string{"success", "retry", "empty", "invalid", "failure", "cancel"} {
				t.Run(fmt.Sprintf("%s/%s/%s", traversal, execution, outcome), func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					requests := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests++
						assert.Equal(t, "2026-05-24T09:55:00Z", r.URL.Query().Get("updated_since"))
						assert.Equal(t, "2026-05-24T11:00:00Z", r.URL.Query().Get("updated_until"))
						w.Header().Set("Content-Type", "application/json")
						if outcome == "empty" {
							_, _ = fmt.Fprint(w, `{"items":[],"next":null}`)
							return
						}
						if requests == 1 {
							next := "page-2"
							if traversal == "next_link" {
								query := r.URL.Query()
								query.Set("after", "page-2")
								next = "?" + query.Encode()
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"updated_at": "2026-05-24T10:30:00Z"}}, "next": next})
							return
						}
						assert.Equal(t, "page-2", r.URL.Query().Get("after"))
						if outcome == "retry" && requests == 2 {
							w.Header().Set("Retry-After", "0")
							w.WriteHeader(http.StatusTooManyRequests)
							return
						}
						switch outcome {
						case "failure":
							w.WriteHeader(http.StatusServiceUnavailable)
						case "cancel":
							cancel()
							<-r.Context().Done()
						case "invalid":
							_, _ = fmt.Fprint(w, `{"items":[{"updated_at":"bad"}],"next":null}`)
						default:
							_, _ = fmt.Fprint(w, `{"items":[{"updated_at":"2026-05-24T10:30:00Z"}],"next":null}`)
						}
					}))
					defer server.Close()
					ds := must.Slug("customer-api")
					operation := map[string]any{"method": "GET", "path": "/items", "query_params": map[string]any{"updated_since": "{{runtime.incremental.lower_bound.value}}", "updated_until": "{{runtime.incremental.upper_bound.value}}"}, "pagination": map[string]any{"cursor_param": "after", "records_path": "items", "next_cursor_path": "next", "max_pages": 3, "timeout_seconds": 2}}
					if traversal == "next_link" {
						pagination := operation["pagination"].(map[string]any)
						delete(pagination, "cursor_param")
						delete(pagination, "next_cursor_path")
						pagination["next_link_path"] = "next"
					}
					repo := incrementalQueryRepo(t, ds, must.Slug("list-items"))
					find := repo.FindBySlugFn
					repo.FindBySlugFn = func(ctx context.Context, db postgres.DBTX, org, source, query slug.Slug) (*qrepo.Query, []*qrepo.QueryParameter, error) {
						q, _, err := find(ctx, db, org, source, query)
						statement, marshalErr := json.Marshal(operation)
						require.NoError(t, marshalErr)
						q.Data, marshalErr = json.Marshal(map[string]any{"statement": string(statement)})
						require.NoError(t, marshalErr)
						return q, nil, err
					}
					factory := func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (connector.Connector, error) {
						return httpconnector.New(transportsecurity.WithPlaintext(ctx), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true}, httpconnector.Credentials{})
					}
					svc, err := newTestServiceWithLimits(repo, incrementalDataSourceService(t, ds, model.DataSourceTypeHTTP), nil, factory, queriessvc.ExecutionLimits{HTTPRetryAttempts: 2})
					require.NoError(t, err)
					checkpoint := incrementalServiceCheckpointInput()
					checkpoint.Lookup.OperationType = "http"
					checkpoint.Lookup.IncrementalField = "items[].updated_at"
					checkpoint.Lookup.IncrementalLookback = "5m"
					checkpoint.LowerBound = json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)
					checkpoint.UpperBound = json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)
					var observation *pipelinerun.IncrementalCheckpointObservation
					switch execution {
					case "saved":
						_, observation, err = svc.ExecuteIncremental(ctx, must.Slug("acme"), ds, must.Slug("list-items"), nil, checkpoint)
					case "snapshot":
						statement, marshalErr := json.Marshal(operation)
						require.NoError(t, marshalErr)
						data, marshalErr := json.Marshal(map[string]any{"statement": string(statement)})
						require.NoError(t, marshalErr)
						_, observation, err = svc.ExecuteSnapshot(ctx, must.Slug("acme"), ds, contractplan.OperationContent{Data: data}, nil, checkpoint)
					default:
						_, observation, err = svc.ExecuteInlineIncremental(ctx, must.Slug("acme"), ds, operation, nil, checkpoint)
					}
					if outcome == "success" || outcome == "retry" || outcome == "empty" {
						require.NoError(t, err)
						require.NotNil(t, observation)
						require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(observation.ObservedUpperBound))
					} else {
						require.Error(t, err)
						require.Nil(t, observation)
					}
				})
			}
		}
	}
}
