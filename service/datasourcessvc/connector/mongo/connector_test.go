package mongo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	coredriver "go.mongodb.org/mongo-driver/v2/x/mongo/driver"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestMapConnectionErrorClassifiesMongoFailuresAndPreservesCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cause error
		want  error
	}{
		{name: "invalid credentials", cause: mongodriver.CommandError{Code: 18, Message: "provider diagnostic"}, want: xerror.InvalidCredentials()},
		{name: "driver authentication failure", cause: coredriver.Error{Code: 18, Message: "provider diagnostic"}, want: xerror.InvalidCredentials()},
		{name: "forbidden", cause: mongodriver.CommandError{Code: 13, Message: "provider diagnostic"}, want: xerror.Forbidden(xerror.DataSource)},
		{name: "timeout", cause: context.DeadlineExceeded, want: xerror.Timeout(xerror.DataSource)},
		{name: "connection failed", cause: fmt.Errorf("network: %w", context.Canceled), want: xerror.ConnectionFailed(xerror.DataSource)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := mapConnectionError(tt.cause)

			require.True(t, xerror.Is(err, tt.want))
			var wantCommandErr mongodriver.CommandError
			var wantDriverErr coredriver.Error
			switch {
			case xerror.As(tt.cause, &wantCommandErr):
				var gotCommandErr mongodriver.CommandError
				require.True(t, xerror.As(err, &gotCommandErr))
				require.Equal(t, wantCommandErr.Code, gotCommandErr.Code)
			case xerror.As(tt.cause, &wantDriverErr):
				var gotDriverErr coredriver.Error
				require.True(t, xerror.As(err, &gotDriverErr))
				require.Equal(t, wantDriverErr.Code, gotDriverErr.Code)
			default:
				require.True(t, xerror.Is(err, tt.cause))
			}
			require.Empty(t, xerror.PublicDetail(err))
		})
	}
}

func TestNewRejectsMissingRequiredConnectionFields(t *testing.T) {
	t.Parallel()

	validConfig := Config{Host: "localhost", Port: 27017, Database: "app"}
	validCredentials := Credentials{Username: "root", Password: "secret"}

	tests := []struct {
		name          string
		config        Config
		credentials   Credentials
		wantErrSubstr string
	}{
		{
			name:          "host",
			config:        Config{Port: 27017, Database: "app"},
			credentials:   validCredentials,
			wantErrSubstr: "host is required",
		},
		{
			name:          "port",
			config:        Config{Host: "localhost", Database: "app"},
			credentials:   validCredentials,
			wantErrSubstr: "port is required",
		},
		{
			name:          "database",
			config:        Config{Host: "localhost", Port: 27017},
			credentials:   validCredentials,
			wantErrSubstr: "database is required",
		},
		{
			name:          "username",
			config:        validConfig,
			credentials:   Credentials{Password: "secret"},
			wantErrSubstr: "username is required",
		},
		{
			name:          "password",
			config:        validConfig,
			credentials:   Credentials{Username: "root"},
			wantErrSubstr: "password is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn, err := New(context.Background(), tt.config, tt.credentials)
			require.Nil(t, conn)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestNewRejectsPrivateHostWithoutOptIn(t *testing.T) {
	t.Parallel()

	conn, err := New(context.Background(), Config{
		Host:           "127.0.0.1",
		Port:           27017,
		Database:       "app",
		AuthDatabase:   "admin",
		ConnectTimeout: 1,
	}, Credentials{
		Username: "root",
		Password: "secret",
	})

	require.Nil(t, conn)
	require.ErrorContains(t, err, "non-public ip address")
}

func TestCloseIsIdempotentWhenClientIsNil(t *testing.T) {
	t.Parallel()

	conn := &Connector{}

	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close())
}

func TestExecuteReturnsResultErrorsBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		queryData     json.RawMessage
		params        map[string]types.TypedParam
		wantErrSubstr string
	}{
		{
			name:          "invalid integer parameter",
			queryData:     json.RawMessage(`{"collection":"users","operation":"find"}`),
			params:        map[string]types.TypedParam{"age": {Value: uint64(math.MaxUint64), DataType: "integer"}},
			wantErrSubstr: "parameter normalization failed",
		},
		{
			name:          "invalid json",
			queryData:     json.RawMessage(`{`),
			wantErrSubstr: "invalid MongoDB query data",
		},
		{
			name:          "missing substituted parameter",
			queryData:     json.RawMessage(`{"collection":"users","operation":"find","filter":{"age":"{{age}}"}}`),
			wantErrSubstr: "failed to substitute statement parameters",
		},
		{
			name:          "structured field resolves to null",
			queryData:     json.RawMessage(`{"collection":"users","operation":"find","filter":"{{filter}}"}`),
			params:        map[string]types.TypedParam{"filter": {Value: nil, DataType: "json"}},
			wantErrSubstr: `MongoDB field "filter" must not resolve to null`,
		},
		{
			name:          "missing collection",
			queryData:     json.RawMessage(`{"operation":"find"}`),
			wantErrSubstr: "must specify a collection",
		},
		{
			name:          "unknown operation",
			queryData:     json.RawMessage(`{"collection":"users","operation":"unknown"}`),
			wantErrSubstr: "must specify an operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := &Connector{}
			result, err := conn.Execute(context.Background(), tt.queryData, tt.params)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.IsSuccess())
			require.Contains(t, result.Error().Error(), tt.wantErrSubstr)
		})
	}
}

func TestIncrementalWatermarkBoundsSubstituteIntoFindAndAggregateStatements(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"runtime.incremental.lower_bound.value": "2026-05-24T10:00:00Z",
		"runtime.incremental.upper_bound.value": "2026-05-24T11:00:00Z",
	}

	tests := []struct {
		name      string
		statement any
		wantJSON  string
	}{
		{
			name: "find filter",
			statement: map[string]any{
				"collection": "users",
				"operation":  "find",
				"filter": map[string]any{
					"updated_at": map[string]any{
						"$gt":  "{{runtime.incremental.lower_bound.value}}",
						"$lte": "{{runtime.incremental.upper_bound.value}}",
					},
				},
			},
			wantJSON: `{
				"collection": "users",
				"operation": "find",
				"filter": {
					"updated_at": {
						"$gt": "2026-05-24T10:00:00Z",
						"$lte": "2026-05-24T11:00:00Z"
					}
				}
			}`,
		},
		{
			name: "aggregate pipeline",
			statement: map[string]any{
				"collection": "users",
				"operation":  "aggregate",
				"pipeline": []any{
					map[string]any{
						"$match": map[string]any{
							"updated_at": map[string]any{
								"$gt":  "{{runtime.incremental.lower_bound.value}}",
								"$lte": "{{runtime.incremental.upper_bound.value}}",
							},
						},
					},
				},
			},
			wantJSON: `{
				"collection": "users",
				"operation": "aggregate",
				"pipeline": [{
					"$match": {
						"updated_at": {
							"$gt": "2026-05-24T10:00:00Z",
							"$lte": "2026-05-24T11:00:00Z"
						}
					}
				}]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := param.SubstituteValue(tt.statement, params)

			require.NoError(t, err)
			resolvedJSON, err := json.Marshal(resolved)
			require.NoError(t, err)
			require.JSONEq(t, tt.wantJSON, string(resolvedJSON))
		})
	}
}

func TestDecodeMongoQueryDataPreservesBoundIntegerForBSON(t *testing.T) {
	t.Parallel()
	statement, err := decodeMongoQueryData([]byte(`{"collection":"orders","operation":"find","filter":{"id":{"$gt":9007199254740993}}}`))
	require.NoError(t, err)
	encoded, err := bson.Marshal(statement.Filter)
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), bson.Raw(encoded).Lookup("id", "$gt").Int64())
}

func TestReadDocumentsStopsBeforeDecodingOverBudget(t *testing.T) {
	t.Parallel()
	first := bson.M{"value": strings.Repeat("x", 80)}
	cursor, err := mongodriver.NewCursorFromDocuments([]any{first, bson.M{"value": strings.Repeat("y", 80)}, bson.M{"unread": true}}, nil, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, cursor.Close(t.Context())) }()
	docs, err := readDocuments(t.Context(), cursor, types.ExecutionLimits{MaxResponseBytes: 128})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.Nil(t, docs)
	require.True(t, cursor.Next(t.Context()), "iteration consumed a document beyond the rejected document")
	var unread bson.M
	require.NoError(t, cursor.Decode(&unread))
	require.Equal(t, true, unread["unread"])
}

func TestReadDocumentsRejectsOversizedJSONBeforeDecode(t *testing.T) {
	t.Parallel()
	for _, value := range []any{strings.Repeat("x", 1024), strings.Repeat("<", 30), bson.Binary{Subtype: 0, Data: make([]byte, 80)}} {
		raw, err := bson.Marshal(bson.M{"value": value})
		require.NoError(t, err)
		decoded := false
		registry := bson.NewRegistry()
		registry.RegisterTypeDecoder(reflect.TypeFor[bson.M](), bson.ValueDecoderFunc(func(bson.DecodeContext, bson.ValueReader, reflect.Value) error {
			decoded = true
			return xerror.Unexpected().Detail("oversized BSON reached decoder")
		}))
		cursor, err := mongodriver.NewCursorFromDocuments([]any{bson.Raw(raw)}, nil, registry)
		require.NoError(t, err)
		defer func() { require.NoError(t, cursor.Close(t.Context())) }()
		docs, err := readDocuments(t.Context(), cursor, types.ExecutionLimits{MaxResponseBytes: 128})
		require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
		require.Nil(t, docs)
		require.False(t, decoded)

	}
}

func TestReadDocumentsRetainsOrdinaryDocumentsAndRowLimits(t *testing.T) {
	t.Parallel()
	for _, maxRows := range []int{0, 1} {
		cursor, err := mongodriver.NewCursorFromDocuments([]any{bson.M{"name": "alice"}, bson.M{"name": "bob"}}, nil, nil)
		require.NoError(t, err)
		docs, err := readDocuments(t.Context(), cursor, types.ExecutionLimits{MaxResultRows: maxRows, MaxResponseBytes: 1024})
		if maxRows == 1 {
			require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
			require.Nil(t, docs)
		} else {
			require.NoError(t, err)
			require.Equal(t, []map[string]any{{"name": "alice"}, {"name": "bob"}}, docs)
		}
		require.NoError(t, cursor.Close(t.Context()))
	}
}

func TestReadDocumentsChargesJSONExpansion(t *testing.T) {
	t.Parallel()
	cursor, err := mongodriver.NewCursorFromDocuments([]any{bson.M{"value": strings.Repeat("<", 30)}}, nil, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, cursor.Close(t.Context())) }()
	docs, err := readDocuments(t.Context(), cursor, types.ExecutionLimits{MaxResponseBytes: 128})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.Nil(t, docs)
}
