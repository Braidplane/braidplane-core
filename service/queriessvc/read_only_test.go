package queriessvc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestDirectOperationIsReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dsType    model.DataSourceType
		statement json.RawMessage
		want      bool
	}{
		{name: "WorkspaceRead", dsType: model.DataSourceTypeGoogleWorkspace, statement: json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"read","worksheet_id":0,"range":"A1:A2"}`), want: true},
		{name: "WorkspaceUpdate", dsType: model.DataSourceTypeGoogleWorkspace, statement: json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"book","operation":"update","worksheet_id":0,"range":"A1:A2","columns":["a"],"rows":[]}`), want: false},
		{name: "WorkspaceMalformedRead", dsType: model.DataSourceTypeGoogleWorkspace, statement: json.RawMessage(`{"type":"google_workspace","operation":"read"}`), want: false},
		{
			name:      "GitRead",
			dsType:    model.DataSourceTypeGit,
			statement: json.RawMessage(`{"type":"file","operation":"read","path":"users.json"}`),
			want:      true,
		},
		{
			name:      "GitDefaultRead",
			dsType:    model.DataSourceTypeGit,
			statement: json.RawMessage(`{"type":"file","path":"users.json"}`),
			want:      true,
		},
		{
			name:      "GitWrite",
			dsType:    model.DataSourceTypeGit,
			statement: json.RawMessage(`{"type":"file","operation":"write","path":"users.json"}`),
			want:      false,
		},
		{
			name:      "GitUnknownOperationFailsClosed",
			dsType:    model.DataSourceTypeGit,
			statement: json.RawMessage(`{"type":"file","operation":"archive","path":"users.json"}`),
			want:      false,
		},
		{
			name:      "HTTPGet",
			dsType:    model.DataSourceTypeHTTP,
			statement: json.RawMessage(`{"method":"GET","path":"/users"}`),
			want:      true,
		},
		{
			name:      "HTTPHead",
			dsType:    model.DataSourceTypeHTTP,
			statement: json.RawMessage(`{"method":"HEAD","path":"/users"}`),
			want:      true,
		},
		{
			name:      "HTTPTraceFailsClosed",
			dsType:    model.DataSourceTypeHTTP,
			statement: json.RawMessage(`{"method":"TRACE","path":"/users"}`),
			want:      false,
		},
		{
			name:      "HTTPPost",
			dsType:    model.DataSourceTypeHTTP,
			statement: json.RawMessage(`{"method":"POST","path":"/users","body":{"id":1}}`),
			want:      false,
		},
		{
			name:      "MongoFind",
			dsType:    model.DataSourceTypeMongoDB,
			statement: json.RawMessage(`{"collection":"users","operation":"find"}`),
			want:      true,
		},
		{
			name:      "MongoUpdate",
			dsType:    model.DataSourceTypeMongoDB,
			statement: json.RawMessage(`{"collection":"users","operation":"update_one"}`),
			want:      false,
		},
		{
			name:      "BigQuerySelect",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "BigQueryReadCTE",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"WITH users AS (SELECT 1 AS id) SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "BigQueryEscapedStringRead",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT 'can\\'t'"}`),
			want:      true,
		},
		{
			name:      "BigQueryEscapedStringContainingSemicolon",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT 'can\\';t'"}`),
			want:      true,
		},
		{
			name:      "BigQueryTripleQuotedEscapedStringContainingSemicolon",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT '''line\\n;still text'''"}`),
			want:      true,
		},
		{
			name:      "BigQueryRawStringContainingBackslashAndSemicolon",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT r'path\\name;still text'"}`),
			want:      true,
		},
		{
			name:      "BigQueryRawBytesPrefixesContainingBackslashAndSemicolon",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT rb'path\\name;still text', br'path\\name;still text'"}`),
			want:      true,
		},
		{
			name:      "BigQueryRawTripleQuotedStringContainingBackslashAndSemicolon",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT r'''path\\name;still text'''"}`),
			want:      true,
		},
		{
			name:      "BigQueryRawStringBackslashDoesNotEscapeQuote",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT r'can\\'t'"}`),
			want:      false,
		},
		{
			name:      "BigQueryRoutineInvocationReliesOnConfiguredIAM",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage("{\"operation\":\"query\",\"sql\":\"SELECT `analytics.remote_routine`(1)\"}"),
			want:      true,
		},
		{
			name:      "BigQueryDMLQuery",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"UPDATE users SET active = true"}`),
			want:      false,
		},
		{
			name:      "BigQueryInsertRows",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"insert_rows","dataset":"analytics","table":"users"}`),
			want:      false,
		},
		{
			name:      "PostgresSelect",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "PostgresSelectContainingSemicolonLiteral",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT ';'::text"}`),
			want:      true,
		},
		{
			name:      "PostgresSelectContainingDollarQuotedSemicolon",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT $$a;b$$::text"}`),
			want:      true,
		},
		{
			name:      "PostgresEscapeStringRead",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT E'can\\'t'"}`),
			want:      true,
		},
		{
			name:      "PostgresEscapeStringContainingSemicolon",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT E'can\\';t'"}`),
			want:      true,
		},
		{
			name:      "PostgresReadCTE",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"WITH users AS (SELECT 1 AS id) SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "PostgresHarmlessQuotedFunctionLiteral",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT '\"pg_advisory_lock\"('::text"}`),
			want:      true,
		},
		{
			name:      "PostgresSideEffectFunction",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT nextval('users_id_seq')"}`),
			want:      false,
		},
		{
			name:      "PostgresAdvisoryLock",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT pg_advisory_lock(730)"}`),
			want:      false,
		},
		{
			name:      "PostgresEscapeStringCannotHideAdvisoryLock",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT E'\\'' , pg_advisory_lock(730) --'"}`),
			want:      false,
		},
		{
			name:      "PostgresQuotedSharedAdvisoryLock",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT pg_catalog.\"pg_advisory_lock_shared\"(730)"}`),
			want:      false,
		},
		{
			name:      "PostgresRowLock",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT id FROM users FOR UPDATE"}`),
			want:      false,
		},
		{
			name:      "PostgresServerFileOutput",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT lo_export(123, '/tmp/users')"}`),
			want:      false,
		},
		{
			name:      "PostgresInsert",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"INSERT INTO users (id) VALUES (1)"}`),
			want:      false,
		},
		{
			name:      "PostgresStackedStatementFailsClosed",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT id FROM users; DELETE FROM users"}`),
			want:      false,
		},
		{
			name:      "PostgresMutatingCTEFailsClosed",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"WITH deleted AS (DELETE FROM users RETURNING id) SELECT id FROM deleted"}`),
			want:      false,
		},
		{
			name:      "PostgresCTESelectIntoFailsClosed",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"WITH source AS (SELECT 1 AS id) SELECT * INTO TEMP created FROM source"}`),
			want:      false,
		},
		{
			name:      "PostgresUnicodeEscapedSideEffectFunctionFailsClosed",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT pg_catalog.U&\"pg_advisory\\005flock\"(730)"}`),
			want:      false,
		},
		{
			name:      "PostgresPlainBackslashStringFailsClosed",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"type":"postgres","sql":"SELECT 'can\\'t'"}`),
			want:      false,
		},
		{
			name:      "MySQLSelect",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "MySQLReadCTE",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"WITH users AS (SELECT 1 AS id) SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "MySQLHashCommentWithSemicolon",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage("{\"type\":\"mysql\",\"sql\":\"SELECT 1 # ; display only\\n\"}"),
			want:      true,
		},
		{
			name:      "MySQLHarmlessExecutableCommentLiteral",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT '/*! display text */'"}`),
			want:      true,
		},
		{
			name:      "MySQLSideEffectFunction",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT GET_LOCK('read-only-fixture', 0)"}`),
			want:      false,
		},
		{
			name:      "MySQLLockingRead",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT id FROM users FOR UPDATE"}`),
			want:      false,
		},
		{
			name:      "MySQLOutputFile",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT id FROM users INTO OUTFILE '/tmp/users'"}`),
			want:      false,
		},
		{
			name:      "MySQLExecutableCommentOutputFile",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 1 /*!50000 INTO OUTFILE '/tmp/users' */"}`),
			want:      false,
		},
		{
			name:      "MariaDBExecutableCommentOutputFile",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 1 /*M! INTO OUTFILE '/tmp/users' */"}`),
			want:      false,
		},
		{
			name:      "MariaDBVersionedExecutableCommentLock",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 1 /*M!100100 + GET_LOCK('read-only-fixture', 0) */"}`),
			want:      false,
		},
		{
			name:      "MySQLDoubleDashWithoutWhitespaceIsNotAComment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 1--GET_LOCK('read-only-fixture', 0)"}`),
			want:      false,
		},
		{
			name:      "MySQLStackedStatementFailsClosed",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 1; DELETE FROM users"}`),
			want:      false,
		},
		{
			name:      "MySQLSessionVariableAssignment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT @counter := 1"}`),
			want:      false,
		},
		{
			name:      "MySQLDottedSessionVariableAssignment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT @counter.name := 1"}`),
			want:      false,
		},
		{
			name:      "MySQLQuotedSessionVariableAssignment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT @'counter-name' := 1"}`),
			want:      false,
		},
		{
			name:      "MySQLBackslashEscapedStringFailsClosed",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"type":"mysql","sql":"SELECT 'can\\'t'"}`),
			want:      false,
		},
		{
			name:      "SnowflakeSelect",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT id FROM users"}`),
			want:      true,
		},
		{
			name:      "SnowflakeInformationSystemFunction",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$TYPEOF('value')"}`),
			want:      true,
		},
		{
			name:      "SnowflakeQuotedSideEffectFunctionName",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT 'SYSTEM$CANCEL_QUERY('"}`),
			want:      true,
		},
		{
			name:      "SnowflakeSequenceNextValue",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT order_sequence.NEXTVAL"}`),
			want:      false,
		},
		{
			name:      "SnowflakeGetNextValue",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT * FROM TABLE(GETNEXTVAL(order_sequence))"}`),
			want:      false,
		},
		{
			name:      "SnowflakeCancelQuery",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$CANCEL_QUERY('query-id')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeSetTaskReturnValue",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$SET_RETURN_VALUE('done')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeReference",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$REFERENCE('TABLE', 'ORDERS')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeAddReference",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$ADD_REFERENCE('TABLE', 'ORDERS')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeEnableBehaviorChangeBundle",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$ENABLE_BEHAVIOR_CHANGE_BUNDLE('2026_01')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeDisableBehaviorChangeBundle",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$DISABLE_BEHAVIOR_CHANGE_BUNDLE('2026_01')"}`),
			want:      false,
		},
		{
			name:      "SnowflakeQueryReference",
			dsType:    model.DataSourceTypeSnowflake,
			statement: json.RawMessage(`{"type":"snowflake","sql":"SELECT SYSTEM$QUERY_REFERENCE('SELECT 1')"}`),
			want:      true,
		},
		{
			name:      "BigQueryExportData",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"EXPORT DATA OPTIONS(uri='gs://bucket/out') AS SELECT 1"}`),
			want:      false,
		},
		{
			name:      "BigQueryStackedStatementFailsClosed",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SELECT 1; DELETE FROM users WHERE id = 1"}`),
			want:      false,
		},
		{
			name:      "BigQueryShowFailsClosed",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"SHOW TABLES"}`),
			want:      false,
		},
		{
			name:      "HubSpotSearch",
			dsType:    model.DataSourceTypeHubSpot,
			statement: json.RawMessage(`{"type":"hubspot","object_type":"contacts","operation":"search"}`),
			want:      true,
		},
		{
			name:      "HubSpotUpsertIsNotReadOnly",
			dsType:    model.DataSourceTypeHubSpot,
			statement: json.RawMessage(`{"type":"hubspot","object_type":"contacts","operation":"upsert"}`),
			want:      false,
		},
		{
			name:      "S3Write",
			dsType:    model.DataSourceTypeS3,
			statement: json.RawMessage(`{"type":"s3","operation":"write","key":"users.json"}`),
			want:      false,
		},
		{
			name:      "MalformedStatementFailsClosed",
			dsType:    model.DataSourceTypeHTTP,
			statement: json.RawMessage(`{"method":`),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := directOperationIsReadOnly(tt.dsType, tt.statement)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDirectOperationReadOnlyRejectionExplainsUnsupportedSQL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dsType    model.DataSourceType
		statement json.RawMessage
		want      string
	}{
		{
			name:      "MultipleStatements",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 1; DELETE FROM users"}`),
			want:      "exactly one statement",
		},
		{
			name:      "PostgresSideEffectFunction",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT pg_advisory_lock(730)"}`),
			want:      "PostgreSQL side-effecting functions",
		},
		{
			name:      "PostgresEscapeStringCannotHideSideEffectFunction",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT E'\\'' , pg_advisory_lock(730) --'"}`),
			want:      "PostgreSQL side-effecting functions",
		},
		{
			name:      "PostgresLockingRead",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT * FROM users FOR SHARE"}`),
			want:      "PostgreSQL row-locking SELECT",
		},
		{
			name:      "MySQLOutputFile",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT * FROM users INTO DUMPFILE '/tmp/users'"}`),
			want:      "MySQL output-file clauses",
		},
		{
			name:      "MySQLExecutableComment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT 1 /*!50000 INTO OUTFILE '/tmp/users' */"}`),
			want:      "MySQL executable comments are unsupported",
		},
		{
			name:      "MySQLUserVariableMutation",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT @counter := 1"}`),
			want:      "MySQL user-variable mutation is unsupported",
		},
		{
			name:      "MySQLDottedUserVariableMutation",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT @counter.name := 1"}`),
			want:      "MySQL user-variable mutation is unsupported",
		},
		{
			name:      "MySQLBackslashEscapes",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT 'can\\'t'"}`),
			want:      "session SQL mode is unknown",
		},
		{
			name:      "PostgresUnicodeEscapedIdentifier",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT pg_catalog.U&\"pg_advisory\\005flock\"(730)"}`),
			want:      "PostgreSQL Unicode-escaped identifiers are unsupported",
		},
		{
			name:      "BigQueryVendorExtension",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":"EXPORT DATA OPTIONS(uri='gs://bucket/out') AS SELECT 1"}`),
			want:      "BigQuery statement type EXPORT is unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := directOperationReadOnlyRejection(tt.dsType, tt.statement)
			assert.Contains(t, got, tt.want)
		})
	}
}

func TestSQLDirectOperationReadOnlyRejectionBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dsType    model.DataSourceType
		statement json.RawMessage
		want      string
	}{
		{
			name:      "PostgresMalformedPayload",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":`),
			want:      "SQL operation payload is malformed",
		},
		{
			name:      "PostgresNullPayload",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`null`),
			want:      "SQL operation payload is malformed",
		},
		{
			name:      "PostgresMissingSQL",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{}`),
			want:      "SQL operation must contain a string sql field",
		},
		{
			name:      "PostgresNonStringSQL",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":42}`),
			want:      "SQL operation must contain a string sql field",
		},
		{
			name:      "MySQLMalformedPayload",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":`),
			want:      "SQL operation payload is malformed",
		},
		{
			name:      "MySQLMissingSQL",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{}`),
			want:      "SQL operation must contain a string sql field",
		},
		{
			name:      "MySQLNonStringSQL",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":42}`),
			want:      "SQL operation must contain a string sql field",
		},
		{
			name:      "BigQueryMalformedPayload",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":`),
			want:      "BigQuery operation payload is malformed",
		},
		{
			name:      "BigQueryMissingSQL",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query"}`),
			want:      "BigQuery query must contain a string sql field",
		},
		{
			name:      "BigQueryNonStringSQL",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"query","sql":42}`),
			want:      "BigQuery query must contain a string sql field",
		},
		{
			name:      "BigQueryOmittedOperationDefaultsToQuery",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"sql":"SELECT 1"}`),
			want:      "",
		},
		{
			name:      "BigQueryNormalizedQueryOperation",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":" QUERY ","sql":"SELECT 1"}`),
			want:      "",
		},
		{
			name:      "BigQueryUnknownOperation",
			dsType:    model.DataSourceTypeBigQuery,
			statement: json.RawMessage(`{"operation":"extract","sql":"SELECT 1"}`),
			want:      "BigQuery operation is unsupported in read-only mode",
		},
		{
			name:      "PostgresShow",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SHOW transaction_read_only"}`),
			want:      "",
		},
		{
			name:      "PostgresValues",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"VALUES (1)"}`),
			want:      "",
		},
		{
			name:      "PostgresReadWithTrailingSemicolon",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 1;"}`),
			want:      "",
		},
		{
			name:      "PostgresEmptySQL",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"  "}`),
			want:      "SQL must contain exactly one statement",
		},
		{
			name:      "PostgresPositionalParameter",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT $1"}`),
			want:      "",
		},
		{
			name:      "MySQLShow",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SHOW TABLES"}`),
			want:      "",
		},
		{
			name:      "MySQLDescribe",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"DESCRIBE users"}`),
			want:      "",
		},
		{
			name:      "MySQLDesc",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"DESC users"}`),
			want:      "",
		},
		{
			name:      "PostgresExplainRead",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"EXPLAIN SELECT 1"}`),
			want:      "",
		},
		{
			name:      "PostgresExplainAnalyze",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"EXPLAIN ANALYZE SELECT 1"}`),
			want:      "EXPLAIN ANALYZE and mutating EXPLAIN targets are unsupported",
		},
		{
			name:      "PostgresExplainWithoutTarget",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"EXPLAIN -- no target"}`),
			want:      "EXPLAIN ANALYZE and mutating EXPLAIN targets are unsupported",
		},
		{
			name:      "MySQLExplainMutatingTarget",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"EXPLAIN UPDATE users SET active = true"}`),
			want:      "EXPLAIN ANALYZE and mutating EXPLAIN targets are unsupported",
		},
		{
			name:      "MySQLLockInShareMode",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT id FROM users LOCK IN SHARE MODE"}`),
			want:      "MySQL locking reads are unsupported",
		},
		{
			name:      "MySQLSelectIntoUserVariable",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"SELECT id INTO @selected_id FROM users"}`),
			want:      "MySQL SELECT INTO user variables is unsupported",
		},
		{
			name:      "MySQLBacktickQuotedUserVariable",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage("{\"sql\":\"SELECT @`selected-id` := 1\"}"),
			want:      "MySQL user-variable mutation is unsupported",
		},
		{
			name:      "PostgresUnterminatedString",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 'unterminated"}`),
			want:      "SQL contains an unterminated string, identifier, or comment",
		},
		{
			name:      "PostgresUnterminatedIdentifier",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 1 AS \"unterminated"}`),
			want:      "SQL contains an unterminated string, identifier, or comment",
		},
		{
			name:      "PostgresUnterminatedDollarQuote",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT $tag$unterminated"}`),
			want:      "SQL contains an unterminated string, identifier, or comment",
		},
		{
			name:      "PostgresUnterminatedBlockComment",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 1 /* unterminated"}`),
			want:      "SQL contains an unterminated string, identifier, or comment",
		},
		{
			name:      "PostgresDoubledStringQuote",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 'it''s safe'"}`),
			want:      "",
		},
		{
			name:      "PostgresPlainBackslashString",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 'can\\'t'"}`),
			want:      "PostgreSQL backslash-escaped strings require E-prefixed escape syntax",
		},
		{
			name:      "PostgresDoubledIdentifierQuote",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"SELECT 1 AS \"safe\"\"identifier\""}`),
			want:      "",
		},
		{
			name:      "MySQLWhitespaceDoubleDashComment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage("{\"sql\":\"SELECT 1 -- ; display only\\n\"}"),
			want:      "",
		},
		{
			name:      "PostgresLeadingComments",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage("{\"sql\":\"/* block */ -- line\\n SELECT 1\"}"),
			want:      "",
		},
		{
			name:      "MySQLLeadingComments",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage("{\"sql\":\"# line\\n/* block */ SELECT 1\"}"),
			want:      "",
		},
		{
			name:      "PostgresLeadingLineCommentWithoutStatement",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"-- no statement"}`),
			want:      "SQL must contain exactly one statement",
		},
		{
			name:      "PostgresLeadingUnterminatedBlockComment",
			dsType:    model.DataSourceTypePostgres,
			statement: json.RawMessage(`{"sql":"/* no statement"}`),
			want:      "SQL contains an unterminated string, identifier, or comment",
		},
		{
			name:      "MySQLLeadingHashCommentWithoutStatement",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"# no statement"}`),
			want:      "SQL must contain exactly one statement",
		},
		{
			name:      "MySQLLeadingExecutableComment",
			dsType:    model.DataSourceTypeMySQL,
			statement: json.RawMessage(`{"sql":"/*!50000 SELECT 1 */"}`),
			want:      "MySQL executable comments are unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := directOperationReadOnlyRejection(tt.dsType, tt.statement)
			assert.Equal(t, tt.want, got)
		})
	}
}
