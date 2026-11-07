package mongo

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// Config holds configuration for MongoDB connections.
type Config struct {
	Host              string `json:"host"                      validate:"required"`
	Port              int    `json:"port"                      validate:"required,min=1,max=65535"`
	Database          string `json:"database"                  validate:"required"`
	AuthDatabase      string `json:"auth_database,omitempty"   validate:"omitempty"`
	ConnectTimeout    int    `json:"connect_timeout,omitempty"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds MongoDB authentication values.
type Credentials struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// Connector connects to MongoDB databases.
type Connector struct {
	client   *mongo.Client
	database string
}

// New creates a new MongoDB connector with the provided configuration and credentials.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	if config.Host == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("host is required")
	}
	if config.Port == 0 {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("port is required")
	}
	if config.Database == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("database is required")
	}
	if creds.Username == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("username is required")
	}
	if creds.Password == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("password is required")
	}

	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 5
	}

	authDB := config.AuthDatabase
	if authDB == "" {
		authDB = "admin"
	}

	uri := url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(creds.Username, creds.Password),
		Host:   net.JoinHostPort(config.Host, strconv.Itoa(config.Port)),
		Path:   "/",
	}
	query := uri.Query()
	query.Set("authSource", authDB)
	uri.RawQuery = query.Encode()

	connectTimeout := time.Duration(config.ConnectTimeout) * time.Second
	opts := options.Client().
		ApplyURI(uri.String()).
		SetServerSelectionTimeout(connectTimeout).
		SetConnectTimeout(connectTimeout)
	if !transportsecurity.PlaintextAllowed(ctx) {
		// The driver binds certificate verification to each discovered replica hostname.
		opts.SetTLSConfig(transportsecurity.TLS(""))
	}
	if !config.AllowPrivateHosts {
		opts.SetDialer(hostguard.PublicDialer{})
	}

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, mapConnectionError(err)
	}

	// Test the connection by pinging the server
	testCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	if err := client.Ping(testCtx, nil); err != nil {
		_ = client.Disconnect(testCtx)
		return nil, mapConnectionError(err)
	}

	return &Connector{
		client:   client,
		database: config.Database,
	}, nil
}

// TestConnection verifies that a connection to MongoDB can be established.
func (c *Connector) TestConnection(ctx context.Context) error {
	if err := c.client.Ping(ctx, nil); err != nil {
		return mapConnectionError(err)
	}
	return nil
}

func mapConnectionError(err error) error {
	var commandErr mongo.CommandError
	if xerror.As(err, &commandErr) {
		return mapConnectionErrorCode(err, commandErr.Code)
	}
	var driverErr driver.Error
	if xerror.As(err, &driverErr) {
		return mapConnectionErrorCode(err, driverErr.Code)
	}
	if mongo.IsTimeout(err) {
		return xerror.Timeout(xerror.DataSource).With(err)
	}

	return xerror.ConnectionFailed(xerror.DataSource).With(err)
}

func mapConnectionErrorCode(err error, code int32) error {
	switch code {
	case 18:
		return xerror.InvalidCredentials().With(err)
	case 13:
		return xerror.Forbidden(xerror.DataSource).With(err)
	default:
		return xerror.ConnectionFailed(xerror.DataSource).With(err)
	}
}

// Close disconnects from MongoDB and releases resources.
func (c *Connector) Close() error {
	if c.client == nil {
		return nil
	}

	err := c.client.Disconnect(context.Background())
	c.client = nil // Mark as closed to make Close() idempotent
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// mongoQueryData is the query definition stored in a query's Data field for MongoDB data sources.
type mongoQueryData struct {
	Collection string           `json:"collection"`
	Operation  Operation        `json:"operation"` // OperationFind, OperationAggregate, OperationCountDocuments, etc.
	Filter     map[string]any   `json:"filter,omitempty"`
	Pipeline   []any            `json:"pipeline,omitempty"`
	Projection map[string]any   `json:"projection,omitempty"`
	Sort       map[string]any   `json:"sort,omitempty"`
	Limit      int64            `json:"limit,omitempty"`
	Document   map[string]any   `json:"document,omitempty"`  // For insertOne
	Documents  []map[string]any `json:"documents,omitempty"` // For insertMany
	Update     map[string]any   `json:"update,omitempty"`    // For updateOne/updateMany
}

// Execute executes a MongoDB operation described by queryData with the provided parameters.
//
// queryData must be a JSON object with at least "collection" and "operation" fields.
// Supported operations:
//   - Read: OperationFind, OperationAggregate, OperationCountDocuments
//   - Write: OperationInsertOne, OperationInsertMany, OperationUpdateOne, OperationDeleteOne,
//     OperationUpdateMany, OperationDeleteMany
//
// params are merged into the filter for read operations and write operations that use filters.
// Write operations require additional fields (document for insertOne, update for
// updateOne/updateMany).
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	execLimits := types.ResolveExecutionLimits(limits...)
	start := time.Now()

	// Normalize typed parameters for MongoDB BSON encoding (e.g., int64 for filter matching)
	normalized, err := normalizeParams(params)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Wrapf("parameter normalization failed: %w", err),
		), nil
	}

	var rawStatement any
	if err := json.Unmarshal(queryData, &rawStatement); err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detailf("invalid MongoDB query data: %w", err),
		), nil
	}

	// Substitute parameters in the parsed statement so exact placeholders can resolve to native
	// objects and arrays rather than forcing raw JSON string injection.
	resolvedStatement, err := param.SubstituteValue(rawStatement, normalized)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Wrapf("failed to substitute statement parameters: %w", err),
		), nil
	}
	if err := validateResolvedStructuredFields(resolvedStatement); err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(nil, durationMs, err), nil
	}

	resolvedStatementJSON, err := json.Marshal(resolvedStatement)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().With(err),
		), nil
	}
	// Parse the polymorphic MongoDB query statement after substitution
	qd, err := decodeMongoQueryData(resolvedStatementJSON)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detailf("invalid MongoDB query data: %w", err),
		), nil
	}
	if qd.Collection == "" {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detail("MongoDB query data must specify a collection"),
		), nil
	}

	if qd.Operation == OperationUnknown {
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detail("MongoDB query data must specify an operation"),
		), nil
	}

	// Parameters are now pre-substituted in the raw JSON before unmarshaling,
	// so no need for field-by-field substitution here

	db := c.client.Database(c.database)
	coll := db.Collection(qd.Collection)

	// Use resolved filter from query data (no implicit runtime merge).
	filter := qd.Filter
	if filter == nil {
		filter = make(map[string]any)
	}

	var resultData *result.MongoDBResult
	var execErr error

	switch qd.Operation {
	case OperationFind:
		resultData, execErr = c.executeFind(ctx, coll, filter, qd, execLimits)
	case OperationAggregate:
		resultData, execErr = c.executeAggregate(ctx, coll, qd.Pipeline, execLimits)
	case OperationCountDocuments:
		resultData, execErr = c.executeCount(ctx, coll, filter)
	case OperationInsertOne:
		resultData, execErr = c.executeInsertOne(ctx, coll, qd)
	case OperationInsertMany:
		resultData, execErr = c.executeInsertMany(ctx, coll, qd)
	case OperationUpdateOne:
		resultData, execErr = c.executeUpdateOne(ctx, coll, filter, qd)
	case OperationDeleteOne:
		resultData, execErr = c.executeDeleteOne(ctx, coll, filter)
	case OperationUpdateMany:
		resultData, execErr = c.executeUpdateMany(ctx, coll, filter, qd)
	case OperationDeleteMany:
		resultData, execErr = c.executeDeleteMany(ctx, coll, filter)
	case OperationUnknown:
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detail("unknown MongoDB operation"),
		), nil
	default:
		durationMs := time.Since(start).Milliseconds()
		return result.NewMongoDBResult(
			nil,
			durationMs,
			xerror.Unexpected().Detailf("unsupported MongoDB operation: %s", qd.Operation.String()),
		), nil
	}

	if resultData != nil {
		// Add operation type to result
		resultData.OperationType = qd.Operation.String()
	}

	durationMs := time.Since(start).Milliseconds()
	return result.NewMongoDBResult(resultData, durationMs, execErr), nil
}

func validateResolvedStructuredFields(statement any) error {
	statementMap, ok := statement.(map[string]any)
	if !ok {
		return nil
	}

	for _, field := range []string{"filter", "projection", "sort", "pipeline", "document", "documents", "update"} {
		value, exists := statementMap[field]
		if exists && value == nil {
			return xerror.Unexpected().Detailf(
				"MongoDB field %q must not resolve to null",
				field,
			)
		}
	}

	return nil
}

// IntrospectSchema retrieves the MongoDB schema by sampling documents from each collection.
func (c *Connector) IntrospectSchema(ctx context.Context) (*Schema, error) {
	db := c.client.Database(c.database)

	// A batch of one bounds prefetched collection metadata so quota is charged
	// before retaining more than the current collection.
	collectionCursor, err := db.ListCollections(ctx, bson.D{}, options.ListCollections().SetNameOnly(true).SetBatchSize(1))
	if err != nil {
		return nil, ErrSchemaIntrospection.With(err)
	}
	defer func() { _ = collectionCursor.Close(ctx) }()

	collections := make([]Collection, 0)

	for collectionCursor.Next(ctx) {
		var info struct {
			Name string `bson:"name"`
		}
		if err := collectionCursor.Decode(&info); err != nil {
			return nil, ErrSchemaIntrospection.With(err)
		}
		collName := info.Name
		if err := schemaquota.Charge(ctx, 1, len(collName)); err != nil {
			return nil, err
		}
		coll := db.Collection(collName)

		// Get document count
		count, err := coll.CountDocuments(ctx, bson.D{})
		if err != nil {
			return nil, ErrSchemaIntrospection.With(err)
		}

		// Sample documents using $sample aggregation stage for random sampling
		const sampleSize = 100
		pipeline := mongo.Pipeline{
			bson.D{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}},
		}

		// A batch of one similarly bounds document prefetch while fields are charged.
		cursor, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetBatchSize(1))
		if err != nil {
			return nil, ErrSchemaIntrospection.With(err)
		}

		// Infer schema from sampled documents
		fields, err := inferFieldsFromDocuments(ctx, cursor)
		_ = cursor.Close(ctx)
		if err != nil {
			return nil, ErrSchemaIntrospection.With(err)
		}

		collections = append(collections, Collection{
			Name:          collName,
			Fields:        fields,
			DocumentCount: count,
		})
	}
	if err := collectionCursor.Err(); err != nil {
		return nil, ErrSchemaIntrospection.With(err)
	}

	return &Schema{
		Collections: collections,
	}, nil
}

func (c *Connector) executeFind(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
	qd mongoQueryData,
	limits types.ExecutionLimits,
) (*result.MongoDBResult, error) {
	opts := options.Find().SetBatchSize(1)

	if qd.Projection != nil {
		opts.SetProjection(qd.Projection)
	}

	if qd.Sort != nil {
		opts.SetSort(qd.Sort)
	}

	if qd.Limit > 0 {
		opts.SetLimit(qd.Limit)
	}
	if limits.MaxResultRows > 0 {
		limit := int64(limits.MaxResultRows + 1)
		if qd.Limit <= 0 || qd.Limit > limit {
			opts.SetLimit(limit)
		}
	}

	cursor, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("MongoDB find failed: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	resultDocs, err := readDocuments(ctx, cursor, limits)
	if err != nil {
		return nil, err
	}

	count := len(resultDocs)
	return &result.MongoDBResult{
		Documents:     resultDocs,
		DocumentCount: &count,
	}, nil
}

func (c *Connector) executeAggregate(
	ctx context.Context,
	coll *mongo.Collection,
	pipeline []any,
	limits types.ExecutionLimits,
) (*result.MongoDBResult, error) {
	if pipeline == nil {
		pipeline = []any{}
	}
	if limits.MaxResultRows > 0 {
		pipeline = append(pipeline, bson.M{"$limit": limits.MaxResultRows + 1})
	}

	cursor, err := coll.Aggregate(ctx, pipeline, options.Aggregate().SetBatchSize(1))
	if err != nil {
		return nil, xerror.Unexpected().Detailf("MongoDB aggregate failed: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	resultDocs, err := readDocuments(ctx, cursor, limits)
	if err != nil {
		return nil, err
	}

	count := len(resultDocs)
	return &result.MongoDBResult{
		Documents:     resultDocs,
		DocumentCount: &count,
	}, nil
}

func (c *Connector) executeCount(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
) (*result.MongoDBResult, error) {
	count, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("MongoDB countDocuments failed: %w", err)
	}

	intCount := int(count)
	return &result.MongoDBResult{
		DocumentCount: &intCount,
	}, nil
}

func (c *Connector) executeInsertOne(
	ctx context.Context,
	coll *mongo.Collection,
	qd mongoQueryData,
) (*result.MongoDBResult, error) {
	if qd.Document == nil {
		return nil, xerror.Unexpected().Detail("insertOne operation requires a document field")
	}

	opResult, err := coll.InsertOne(ctx, qd.Document)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &result.MongoDBResult{
		InsertedID: opResult.InsertedID,
	}, nil
}

func (c *Connector) executeInsertMany(
	ctx context.Context,
	coll *mongo.Collection,
	qd mongoQueryData,
) (*result.MongoDBResult, error) {
	if len(qd.Documents) == 0 {
		return nil, xerror.Unexpected().Detail("insertMany operation requires a documents field")
	}

	documents := make([]any, 0, len(qd.Documents))
	for _, document := range qd.Documents {
		documents = append(documents, document)
	}

	opResult, err := coll.InsertMany(ctx, documents)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	insertedIDs := append([]any{}, opResult.InsertedIDs...)
	count := int64(len(insertedIDs))

	return &result.MongoDBResult{
		InsertedIDs:   insertedIDs,
		InsertedCount: &count,
	}, nil
}

func (c *Connector) executeUpdateOne(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
	qd mongoQueryData,
) (*result.MongoDBResult, error) {
	if qd.Update == nil {
		return nil, xerror.Unexpected().Detail("updateOne operation requires an update field")
	}

	opResult, err := coll.UpdateOne(ctx, filter, qd.Update)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &result.MongoDBResult{
		MatchedCount:  &opResult.MatchedCount,
		ModifiedCount: &opResult.ModifiedCount,
		UpsertedCount: &opResult.UpsertedCount,
		UpsertedID:    opResult.UpsertedID,
	}, nil
}

func (c *Connector) executeDeleteOne(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
) (*result.MongoDBResult, error) {
	opResult, err := coll.DeleteOne(ctx, filter)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("MongoDB deleteOne failed: %w", err)
	}

	return &result.MongoDBResult{
		DeletedCount: &opResult.DeletedCount,
	}, nil
}

func (c *Connector) executeUpdateMany(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
	qd mongoQueryData,
) (*result.MongoDBResult, error) {
	if qd.Update == nil {
		return nil, xerror.Unexpected().Detail("updateMany operation requires an update field")
	}

	opResult, err := coll.UpdateMany(ctx, filter, qd.Update)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &result.MongoDBResult{
		MatchedCount:  &opResult.MatchedCount,
		ModifiedCount: &opResult.ModifiedCount,
		UpsertedCount: &opResult.UpsertedCount,
		UpsertedID:    opResult.UpsertedID,
	}, nil
}

func (c *Connector) executeDeleteMany(
	ctx context.Context,
	coll *mongo.Collection,
	filter map[string]any,
) (*result.MongoDBResult, error) {
	opResult, err := coll.DeleteMany(ctx, filter)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("MongoDB deleteMany failed: %w", err)
	}

	return &result.MongoDBResult{
		DeletedCount: &opResult.DeletedCount,
	}, nil
}

// inferFieldsFromDocuments extracts field names and types from sampled documents.
func inferFieldsFromDocuments(
	ctx context.Context,
	cursor *mongo.Cursor,
) ([]Field, error) {
	// Track field metadata across all sampled documents
	fieldInfo := make(map[string]*fieldTracker)
	docCount := 0

	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, xerror.Unexpected().With(err)
		}

		docCount++

		// Extract fields from this document
		for fieldName, fieldValue := range doc {
			tracker, exists := fieldInfo[fieldName]
			if !exists {
				if err := schemaquota.Charge(ctx, 0, len(fieldName)+16); err != nil {
					return nil, err
				}
				tracker = &fieldTracker{
					types: make(map[FieldType]int),
				}
				fieldInfo[fieldName] = tracker
			}

			tracker.count++
			bsonType := inferBSONType(fieldValue)
			tracker.types[bsonType]++
		}
	}

	if err := cursor.Err(); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Convert tracked field info to Field structs
	fields := make([]Field, 0, len(fieldInfo))
	for fieldName, tracker := range fieldInfo {
		field := Field{
			Name:     fieldName,
			Type:     determineFieldType(tracker.types),
			Nullable: tracker.count < docCount, // Field missing in some documents
		}
		fields = append(fields, field)
	}

	return fields, nil
}

// fieldTracker tracks metadata about a field across multiple documents.
type fieldTracker struct {
	count int               // Number of documents containing this field
	types map[FieldType]int // Map of type -> count of documents with that type
}

// determineFieldType selects the final type for a field based on observed types.
// If multiple types are observed, returns FieldTypeMixed.
func determineFieldType(types map[FieldType]int) FieldType {
	if len(types) == 0 {
		return FieldTypeUnknown
	}

	if len(types) == 1 {
		// Only one type observed
		for typeName := range types {
			return typeName
		}
	}

	// Multiple types observed - mark as mixed
	return FieldTypeMixed
}

// inferBSONType determines the FieldType for a BSON value.
func inferBSONType(value any) FieldType {
	if value == nil {
		return FieldTypeNull
	}

	switch value.(type) {
	case string:
		return FieldTypeStr
	case int, int32, int64:
		return FieldTypeInt
	case float32, float64:
		return FieldTypeDouble
	case bool:
		return FieldTypeBoolean
	case bson.ObjectID:
		return FieldTypeObjectID
	case bson.DateTime, time.Time:
		return FieldTypeDate
	case bson.Timestamp:
		return FieldTypeTimestamp
	case bson.D, bson.M, map[string]any:
		return FieldTypeObject
	case bson.A, []any:
		return FieldTypeArray
	case bson.Binary:
		return FieldTypeBinary
	case bson.Decimal128:
		return FieldTypeDecimal
	case bson.Regex:
		return FieldTypeRegex
	default:
		return FieldTypeUnknown
	}
}

func decodeMongoQueryData(raw []byte) (mongoQueryData, error) {
	var statement mongoQueryData
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&statement)
	return statement, err
}

func readDocuments(ctx context.Context, cursor *mongo.Cursor, limits types.ExecutionLimits) ([]map[string]any, error) {
	docs := make([]map[string]any, 0)
	budget := result.NewResponseBudget(limits.MaxResponseBytes)
	if err := budget.Charge(2); err != nil {
		return nil, err
	}
	for cursor.Next(ctx) {
		if limits.MaxResultRows > 0 && len(docs) >= limits.MaxResultRows {
			return nil, xerror.ExecutionRowLimitExceeded().Detailf("MongoDB operation returned more than %d documents", limits.MaxResultRows)
		}
		if len(docs) > 0 {
			if err := budget.Charge(1); err != nil {
				return nil, err
			}
		}
		if limits.MaxResponseBytes > 0 {
			if err := chargeBSONDocument(budget, cursor.Current, false, 0); err != nil {
				return nil, err
			}
		}
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		docs = append(docs, doc)
	}
	if err := cursor.Err(); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return docs, nil
}
