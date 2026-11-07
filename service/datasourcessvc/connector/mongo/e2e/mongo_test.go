package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/as"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func connectorPort() int {
	return depsports.MongoConnectorPort()
}

func testMongoConfig() mongoconnector.Config {
	return mongoconnector.Config{
		Host:              "127.0.0.1",
		Port:              connectorPort(),
		Database:          "braidplane",
		AuthDatabase:      "admin",
		ConnectTimeout:    5,
		AllowPrivateHosts: true,
	}
}

func testMongoCredentials() mongoconnector.Credentials {
	return mongoconnector.Credentials{
		Username: "root",
		Password: "rootpass",
	}
}

func executeMongo(
	ctx context.Context,
	conn *mongoconnector.Connector,
	query map[string]any,
	params map[string]any,
) (any, error) {
	return executeMongoWithLimits(ctx, conn, query, params, types.ExecutionLimits{})
}

func executeMongoWithLimits(
	ctx context.Context,
	conn *mongoconnector.Connector,
	query map[string]any,
	params map[string]any,
	limits types.ExecutionLimits,
) (any, error) {
	queryData, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	return executeMongoRawWithLimits(ctx, conn, queryData, params, limits)
}

func executeMongoRaw(
	ctx context.Context,
	conn *mongoconnector.Connector,
	queryData []byte,
	params map[string]any,
) (any, error) {
	return executeMongoRawWithLimits(ctx, conn, queryData, params, types.ExecutionLimits{})
}

func executeMongoRawWithLimits(
	ctx context.Context,
	conn *mongoconnector.Connector,
	queryData []byte,
	params map[string]any,
	limits types.ExecutionLimits,
) (any, error) {
	// Convert plain params to TypedParam map (use "text" for E2E tests)
	typedParams := make(map[string]types.TypedParam, len(params))
	for name, value := range params {
		typedParams[name] = types.TypedParam{
			Value:    value,
			DataType: "text", // E2E tests don't test normalization
		}
	}

	// Pass the query statement (now polymorphic without envelope) directly to the connector
	execResult, err := conn.Execute(ctx, queryData, typedParams, limits)
	if err != nil {
		return nil, err
	}

	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	mongoResult, ok := execResult.MongoDB()
	if !ok {
		return nil, fmt.Errorf("expected MongoDB result, got: %s", execResult.Type())
	}

	// Convert MongoDBResult to a map for consistent test assertions
	resultMap := make(map[string]any)
	resultMap["operationType"] = mongoResult.OperationType

	if mongoResult.Documents != nil {
		resultMap["documents"] = mongoResult.Documents
	}
	if mongoResult.DocumentCount != nil {
		resultMap["documentCount"] = float64(
			*mongoResult.DocumentCount,
		) // Convert to float64 for test compatibility
	}
	if mongoResult.InsertedID != nil {
		resultMap["insertedId"] = mongoResult.InsertedID
	}
	if mongoResult.InsertedIDs != nil {
		resultMap["insertedIds"] = mongoResult.InsertedIDs
	}
	if mongoResult.InsertedCount != nil {
		resultMap["insertedCount"] = float64(
			*mongoResult.InsertedCount,
		) // Convert to float64 for test compatibility
	}
	if mongoResult.MatchedCount != nil {
		resultMap["matchedCount"] = float64(
			*mongoResult.MatchedCount,
		) // Convert to float64 for test compatibility
	}
	if mongoResult.ModifiedCount != nil {
		resultMap["modifiedCount"] = float64(
			*mongoResult.ModifiedCount,
		) // Convert to float64 for test compatibility
	}
	if mongoResult.DeletedCount != nil {
		resultMap["deletedCount"] = float64(
			*mongoResult.DeletedCount,
		) // Convert to float64 for test compatibility
	}
	if mongoResult.UpsertedID != nil {
		resultMap["upsertedId"] = mongoResult.UpsertedID
	}
	if mongoResult.UpsertedCount != nil {
		resultMap["upsertedCount"] = float64(
			*mongoResult.UpsertedCount,
		) // Convert to float64 for test compatibility
	}

	return resultMap, nil
}

func TestMongoDBConnector(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	tests := []struct {
		name        string
		config      mongoconnector.Config
		credentials mongoconnector.Credentials
		assertErr   func(t *testing.T, err error)
	}{
		{
			name:        "ValidConfigCreatesConnector",
			config:      testMongoConfig(),
			credentials: testMongoCredentials(),
		},
		{
			name: "MissingHostReturnsError",
			config: mongoconnector.Config{
				Host:         "",
				Port:         connectorPort(),
				Database:     "braidplane",
				AuthDatabase: "admin",
			},
			credentials: testMongoCredentials(),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "host is required")
			},
		},
		{
			name: "MissingPortReturnsError",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              0,
				Database:          "braidplane",
				AuthDatabase:      "admin",
				AllowPrivateHosts: true,
			},
			credentials: testMongoCredentials(),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "port is required")
			},
		},
		{
			name: "MissingDatabaseReturnsError",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              connectorPort(),
				Database:          "",
				AuthDatabase:      "admin",
				AllowPrivateHosts: true,
			},
			credentials: testMongoCredentials(),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "database is required")
			},
		},
		{
			name: "MissingUsernameReturnsError",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              connectorPort(),
				Database:          "braidplane",
				AuthDatabase:      "admin",
				AllowPrivateHosts: true,
			},
			credentials: mongoconnector.Credentials{Username: "", Password: "rootpass"},
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "username is required")
			},
		},
		{
			name: "MissingPasswordReturnsError",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              connectorPort(),
				Database:          "braidplane",
				AuthDatabase:      "admin",
				AllowPrivateHosts: true,
			},
			credentials: mongoconnector.Credentials{Username: "root", Password: ""},
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "password is required")
			},
		},
		{
			name: "InvalidCredentialsReturnsError",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              connectorPort(),
				Database:          "braidplane",
				AuthDatabase:      "admin",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			credentials: mongoconnector.Credentials{Username: "root", Password: "wrongpassword"},
			assertErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, xerror.InvalidCredentials())
			},
		},
		{
			name: "DefaultAuthDatabaseUsesAdmin",
			config: mongoconnector.Config{
				Host:              "127.0.0.1",
				Port:              connectorPort(),
				Database:          "braidplane",
				AuthDatabase:      "",
				ConnectTimeout:    5,
				AllowPrivateHosts: true,
			},
			credentials: testMongoCredentials(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), tt.config, tt.credentials)

			if tt.assertErr != nil {
				tt.assertErr(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, conn, "connector should be created")

			err = conn.TestConnection(ctx)
			require.NoError(t, err)

			err = conn.Close()
			require.NoError(t, err)
		})
	}
}

func TestMongoDBConnectorExecuteLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")
	defer func() { _ = conn.Close() }()

	collection := fmt.Sprintf("e2e_execute_%d", time.Now().UnixNano())

	// Clean up any existing documents in the collection before starting the test
	_, _ = executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_many",
		"filter":     map[string]any{},
	}, nil)

	seedManyResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "insert_many",
		"documents": []map[string]any{
			{"sku": "A-1", "category": "tools", "status": "draft", "qty": 10},
			{"sku": "A-2", "category": "tools", "status": "pending", "qty": 20},
			{"sku": "A-3", "category": "office", "status": "pending", "qty": 30},
		},
	}, nil)
	require.NoError(t, err)
	seedManyMap := as.Must[map[string]any](t, seedManyResult)
	assert.InEpsilon(t, 3.0, seedManyMap["insertedCount"], 0.000001)
	insertedIDs := as.Must[[]any](t, seedManyMap["insertedIds"])
	assert.Len(t, insertedIDs, 3)

	seedOneResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "insert_one",
		"document": map[string]any{
			"sku":      "A-4",
			"category": "tools",
			"status":   "archived",
			"qty":      40,
		},
	}, nil)
	require.NoError(t, err)
	seedOneMap := as.Must[map[string]any](t, seedOneResult)
	_, insertedIDExists := seedOneMap["insertedId"]
	assert.True(t, insertedIDExists)

	updateOneResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "update_one",
		"filter":     map[string]any{"sku": "A-1"},
		"update":     map[string]any{"$set": map[string]any{"qty": 15}},
	}, nil)
	require.NoError(t, err)
	updateOneMap := as.Must[map[string]any](t, updateOneResult)
	assert.InEpsilon(t, 1.0, updateOneMap["matchedCount"], 0.000001)
	assert.InEpsilon(t, 1.0, updateOneMap["modifiedCount"], 0.000001)

	updateManyResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "update_many",
		"filter":     map[string]any{"category": "tools"},
		"update":     map[string]any{"$set": map[string]any{"status": "active"}},
	}, nil)
	require.NoError(t, err)
	updateManyMap := as.Must[map[string]any](t, updateManyResult)
	assert.InEpsilon(t, 3.0, updateManyMap["matchedCount"], 0.000001)
	assert.InEpsilon(t, 3.0, updateManyMap["modifiedCount"], 0.000001)

	findResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "find",
		"filter":     map[string]any{"status": "active"},
		"projection": map[string]any{"_id": 0, "sku": 1, "status": 1, "qty": 1},
		"sort":       map[string]any{"sku": 1},
	}, nil)
	require.NoError(t, err)
	findResultMap := as.Must[map[string]any](t, findResult)
	findDocs := as.Must[[]map[string]any](t, findResultMap["documents"])
	assert.Len(t, findDocs, 3)
	firstFind := as.Must[map[string]any](t, findDocs[0])
	assert.Equal(t, "A-1", firstFind["sku"])
	assert.Equal(t, "active", firstFind["status"])
	assert.InEpsilon(t, float64(15), firstFind["qty"], 0.000001)

	aggregateResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "aggregate",
		"pipeline": []any{
			map[string]any{
				"$group": map[string]any{"_id": "$status", "count": map[string]any{"$sum": 1}},
			},
			map[string]any{"$sort": map[string]any{"_id": 1}},
		},
	}, nil)
	require.NoError(t, err)
	aggResultMap := as.Must[map[string]any](t, aggregateResult)
	aggDocs := as.Must[[]map[string]any](t, aggResultMap["documents"])
	assert.Len(t, aggDocs, 2)
	firstAgg := as.Must[map[string]any](t, aggDocs[0])
	secondAgg := as.Must[map[string]any](t, aggDocs[1])
	assert.Equal(t, "active", firstAgg["_id"])
	assert.InEpsilon(t, float64(3), firstAgg["count"], 0.000001)
	assert.Equal(t, "pending", secondAgg["_id"])
	assert.InEpsilon(t, float64(1), secondAgg["count"], 0.000001)

	countResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "count_documents",
		"filter":     map[string]any{"status": "active"},
	}, nil)
	require.NoError(t, err)
	countMap := as.Must[map[string]any](t, countResult)
	assert.InEpsilon(t, float64(3), countMap["documentCount"], 0.000001)

	deleteOneResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_one",
		"filter":     map[string]any{"sku": "A-2"},
	}, nil)
	require.NoError(t, err)
	deleteOneMap := as.Must[map[string]any](t, deleteOneResult)
	assert.InEpsilon(t, float64(1), deleteOneMap["deletedCount"], 0.000001)

	deleteManyResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_many",
		"filter":     map[string]any{"status": "pending"},
	}, nil)
	require.NoError(t, err)
	deleteManyMap := as.Must[map[string]any](t, deleteManyResult)
	assert.InEpsilon(t, float64(1), deleteManyMap["deletedCount"], 0.000001)

	finalCountResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "count_documents",
		"filter":     map[string]any{},
	}, nil)
	require.NoError(t, err)
	finalCountMap := as.Must[map[string]any](t, finalCountResult)
	assert.InEpsilon(t, float64(2), finalCountMap["documentCount"], 0.000001)
}

func TestMongoDBConnectorExecute_ReturnsRowLimitViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	collection := fmt.Sprintf("e2e_row_limit_%d", time.Now().UnixNano())
	_, _ = executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_many",
		"filter":     map[string]any{},
	}, nil)

	_, err = executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "insert_many",
		"documents": []map[string]any{
			{"sku": "RL-1", "status": "active"},
			{"sku": "RL-2", "status": "active"},
		},
	}, nil)
	require.NoError(t, err)

	_, err = executeMongoWithLimits(
		ctx,
		conn,
		map[string]any{
			"collection": collection,
			"operation":  "find",
			"filter":     map[string]any{"status": "active"},
			"projection": map[string]any{"_id": 0, "sku": 1},
			"sort":       map[string]any{"sku": 1},
		},
		nil,
		types.ExecutionLimits{MaxResultRows: 1},
	)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestMongoDBConnectorExecute_SupportsExactPlaceholderDocumentsPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	collection := fmt.Sprintf("e2e_execute_placeholder_%d", time.Now().UnixNano())

	_, _ = executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_many",
		"filter":     map[string]any{},
	}, nil)

	insertResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "insert_many",
		"documents":  "{{payload}}",
	}, map[string]any{
		"payload": []map[string]any{
			{"sku": "B-1", "status": "active", "qty": 10},
			{"sku": "B-2", "status": "active", "qty": 20},
		},
	})
	require.NoError(t, err)
	insertMap := as.Must[map[string]any](t, insertResult)
	assert.InEpsilon(t, 2.0, insertMap["insertedCount"], 0.000001)

	findResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "find",
		"filter":     map[string]any{"status": "active"},
		"projection": map[string]any{"_id": 0, "sku": 1, "qty": 1},
		"sort":       map[string]any{"sku": 1},
	}, nil)
	require.NoError(t, err)
	findMap := as.Must[map[string]any](t, findResult)
	findDocs := as.Must[[]map[string]any](t, findMap["documents"])
	assert.Len(t, findDocs, 2)
	assert.Equal(t, "B-1", findDocs[0]["sku"])
	assert.InEpsilon(t, float64(10), findDocs[0]["qty"], 0.000001)
	assert.Equal(t, "B-2", findDocs[1]["sku"])
	assert.InEpsilon(t, float64(20), findDocs[1]["qty"], 0.000001)
}

func TestMongoDBConnectorExecute_SupportsExactPlaceholderFilterUpdateAndPipelinePayloads(
	t *testing.T,
) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	collection := fmt.Sprintf("e2e_execute_placeholder_ops_%d", time.Now().UnixNano())

	_, _ = executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "delete_many",
		"filter":     map[string]any{},
	}, nil)

	seedResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "insert_many",
		"documents": []map[string]any{
			{"sku": "C-1", "category": "tools", "status": "pending", "qty": 10},
			{"sku": "C-2", "category": "tools", "status": "pending", "qty": 20},
			{"sku": "C-3", "category": "office", "status": "pending", "qty": 30},
		},
	}, nil)
	require.NoError(t, err)
	seedMap := as.Must[map[string]any](t, seedResult)
	assert.InEpsilon(t, 3.0, seedMap["insertedCount"], 0.000001)

	findResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "find",
		"filter":     "{{filter}}",
		"projection": map[string]any{"_id": 0, "sku": 1, "category": 1},
		"sort":       map[string]any{"sku": 1},
	}, map[string]any{
		"filter": map[string]any{"category": "tools"},
	})
	require.NoError(t, err)
	findMap := as.Must[map[string]any](t, findResult)
	findDocs := as.Must[[]map[string]any](t, findMap["documents"])
	assert.Len(t, findDocs, 2)
	assert.Equal(t, "C-1", findDocs[0]["sku"])
	assert.Equal(t, "tools", findDocs[0]["category"])
	assert.Equal(t, "C-2", findDocs[1]["sku"])
	assert.Equal(t, "tools", findDocs[1]["category"])

	updateResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "update_many",
		"filter":     "{{filter}}",
		"update":     "{{update}}",
	}, map[string]any{
		"filter": map[string]any{"category": "tools"},
		"update": map[string]any{"$set": map[string]any{"status": "active"}},
	})
	require.NoError(t, err)
	updateMap := as.Must[map[string]any](t, updateResult)
	assert.InEpsilon(t, 2.0, updateMap["matchedCount"], 0.000001)
	assert.InEpsilon(t, 2.0, updateMap["modifiedCount"], 0.000001)

	aggregateResult, err := executeMongo(ctx, conn, map[string]any{
		"collection": collection,
		"operation":  "aggregate",
		"pipeline":   "{{pipeline}}",
	}, map[string]any{
		"pipeline": []any{
			map[string]any{"$match": map[string]any{"status": "active"}},
			map[string]any{
				"$group": map[string]any{"_id": "$status", "count": map[string]any{"$sum": 1}},
			},
		},
	})
	require.NoError(t, err)
	aggregateMap := as.Must[map[string]any](t, aggregateResult)
	aggregateDocs := as.Must[[]map[string]any](t, aggregateMap["documents"])
	assert.Len(t, aggregateDocs, 1)
	assert.Equal(t, "active", aggregateDocs[0]["_id"])
	assert.InEpsilon(t, float64(2), aggregateDocs[0]["count"], 0.000001)
}

// Sequential execution required: shares ctx and conn across subtests
func TestMongoDBConnectorExecuteErrorPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")
	defer func() { _ = conn.Close() }()

	collection := fmt.Sprintf("e2e_execute_errors_%d", time.Now().UnixNano())

	tests := []struct {
		name      string
		queryData []byte
		assertErr func(t *testing.T, err error)
	}{
		{
			name:      "InvalidJSON",
			queryData: []byte(`{"collection":"x",`),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "invalid MongoDB query data")
			},
		},
		{
			name:      "MissingCollection",
			queryData: []byte(`{"operation":"find"}`),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "must specify a collection")
			},
		},
		{
			name:      "MissingOperation",
			queryData: fmt.Appendf(nil, `{"collection":"%s"}`, collection),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "must specify an operation")
			},
		},
		{
			name:      "UnsupportedNumericOperation",
			queryData: fmt.Appendf(nil, `{"collection":"%s","operation":999}`, collection),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "invalid MongoDB query data")
			},
		},
		{
			name: "FindInvalidFilter",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"find","filter":{"x":{"$bad":1}}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "MongoDB find failed")
			},
		},
		{
			name: "AggregateInvalidStage",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"aggregate","pipeline":[{"$badStage":{}}]}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "MongoDB aggregate failed")
			},
		},
		{
			name: "CountInvalidFilter",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"count_documents","filter":{"x":{"$bad":1}}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "MongoDB countDocuments failed")
			},
		},
		{
			name:      "InsertOneMissingDocument",
			queryData: fmt.Appendf(nil, `{"collection":"%s","operation":"insert_one"}`, collection),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "insertOne operation requires a document field")
			},
		},
		{
			name: "InsertManyMissingDocuments",
			queryData: fmt.Appendf(
				nil,
				`{"collection":"%s","operation":"insert_many"}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "insertMany operation requires a documents field")
			},
		},
		{
			name: "UpdateOneMissingUpdate",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"update_one","filter":{"k":"v"}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "updateOne operation requires an update field")
			},
		},
		{
			name: "UpdateManyMissingUpdate",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"update_many","filter":{"k":"v"}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "updateMany operation requires an update field")
			},
		},
		{
			name: "DeleteOneInvalidFilter",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"delete_one","filter":{"x":{"$bad":1}}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "MongoDB deleteOne failed")
			},
		},
		{
			name: "DeleteManyInvalidFilter",
			queryData: fmt.Appendf(nil,
				`{"collection":"%s","operation":"delete_many","filter":{"x":{"$bad":1}}}`,
				collection,
			),
			assertErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "MongoDB deleteMany failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, execErr := executeMongoRaw(ctx, conn, tt.queryData, nil)
			tt.assertErr(t, execErr)
		})
	}
}

func TestMongoDBConnectorExecute_ReturnsErrWhenExactPlaceholderResolvesToUnsupportedValue(
	t *testing.T,
) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = executeMongoRaw(
		ctx,
		conn,
		[]byte(`{"collection":"users","operation":"aggregate","pipeline":"{{pipeline}}"}`),
		map[string]any{"pipeline": make(chan int)},
	)
	require.ErrorContains(t, err, "unsupported type: chan int")
}

func TestMongoDBConnectorExecute_ReturnsErrWhenStructuredFieldResolvesToNull(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = executeMongoRaw(
		ctx,
		conn,
		[]byte(`{"collection":"users","operation":"aggregate","pipeline":"{{pipeline}}"}`),
		map[string]any{"pipeline": nil},
	)
	require.ErrorContains(t, err, `MongoDB field "pipeline" must not resolve to null`)
}

func TestMongoDBConnectorConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")
	defer func() { _ = conn.Close() }()

	done := make(chan error, 10)
	for range 10 {
		go func() {
			testCtx, testCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer testCancel()
			done <- conn.TestConnection(testCtx)
		}()
	}

	for range 10 {
		err := <-done
		require.NoError(t, err)
	}
}

func TestMongoDBConnectorCloseIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")

	err = conn.Close()
	require.NoError(t, err)

	err = conn.Close()
	require.NoError(t, err)
}

func TestMongoDBConnectorIntrospectSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := mongoconnector.New(transportsecurity.WithPlaintext(ctx), testMongoConfig(), testMongoCredentials())
	require.NoError(t, err)
	require.NotNil(t, conn, "connector should be created")
	defer func() { _ = conn.Close() }()

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.NotNil(t, schema, "schema should be returned")
	assert.GreaterOrEqual(
		t,
		len(schema.Collections),
		4,
		"schema should expose at least seeded collections",
	)

	collectionMap := make(map[string]*mongoconnector.Collection)
	for i := range schema.Collections {
		collectionMap[schema.Collections[i].Name] = &schema.Collections[i]
	}

	expectedCollections := []string{"users", "products", "orders", "analytics"}
	for _, collectionName := range expectedCollections {
		collection, exists := collectionMap[collectionName]
		assert.True(t, exists, "collection %s should exist", collectionName)
		if exists {
			assert.NotEmpty(
				t,
				collection.Fields,
				"collection %s should expose fields",
				collectionName,
			)
		}
	}

	usersCollection, usersExists := collectionMap["users"]
	require.True(t, usersExists, "users collection should exist")
	userFields := make(map[string]mongoconnector.FieldType)
	for _, field := range usersCollection.Fields {
		userFields[field.Name] = field.Type
	}
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["_id"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["email"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["fullName"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["age"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["isActive"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["balance"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["preferences"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, userFields["tags"])

	productsCollection, productsExists := collectionMap["products"]
	require.True(t, productsExists, "products collection should exist")
	productFields := make(map[string]mongoconnector.FieldType)
	for _, field := range productsCollection.Fields {
		productFields[field.Name] = field.Type
	}
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, productFields["_id"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, productFields["sku"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, productFields["name"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, productFields["price"])
	assert.NotEqual(t, mongoconnector.FieldTypeUnknown, productFields["metadata"])

	analyticsCollection, analyticsExists := collectionMap["analytics"]
	require.True(t, analyticsExists, "analytics collection should exist")
	analyticsFields := make(map[string]mongoconnector.FieldType)
	for _, field := range analyticsCollection.Fields {
		analyticsFields[field.Name] = field.Type
	}
	assert.Equal(t, mongoconnector.FieldTypeMixed, analyticsFields["value"])

	for _, collection := range schema.Collections {
		assert.NotEmpty(t, collection.Name, "collection name should not be empty")
		assert.NotEmpty(
			t,
			collection.Fields,
			"collection %s should contain fields",
			collection.Name,
		)
		for _, field := range collection.Fields {
			assert.NotEmpty(t, field.Name, "field name should not be empty")
			assert.NotEqual(t, mongoconnector.FieldTypeUnknown, field.Type)
		}
	}
}
