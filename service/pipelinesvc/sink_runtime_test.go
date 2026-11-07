package pipelinesvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type noopSinkEffectLedger struct{}

// BeginOrGet keeps isolated sink-runtime tests independent of PostgreSQL while
// production construction continues to require the durable ledger adapter.
func (noopSinkEffectLedger) BeginOrGet(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinerun.SinkEffectAttempt,
) (*pipelinerun.SinkEffect, error) {
	return &pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress}, nil
}

// MarkSucceeded completes the in-memory test claim after the fake connector succeeds.
func (noopSinkEffectLedger) MarkSucceeded(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinerun.SinkEffectAttempt,
) error {
	return nil
}

func testCachedSchemas() map[slug.Slug]datasourcessvc.SchemaResult {
	return map[slug.Slug]datasourcessvc.SchemaResult{
		must.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
				{
					Name: "users",
					Columns: []datasourcessvc.SchemaColumn{
						{Name: "id", Type: "integer"},
						{Name: "email", Type: "text"},
					},
				},
			}},
		},
		must.Slug("acme-http"): datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeHTTP,
			Type:            "http",
			Payload:         datasourcessvc.HTTPSchemaPayload{Operations: []datasourcessvc.SchemaOperation{}},
		},
		must.Slug("acme-ftp"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeFile,
			Type:            "ftp",
			Payload:         datasourcessvc.FileSchemaPayload{},
		},
		must.Slug("acme-git"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeFile,
			Type:            "git",
			Payload:         datasourcessvc.FileSchemaPayload{},
		},
		must.Slug("acme-mongo"): datasourcessvc.Schema[datasourcessvc.DocumentSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeDocument,
			Type:            "mongodb",
			Payload:         datasourcessvc.DocumentSchemaPayload{},
		},
	}
}

func TestService_ExecuteRejectsFTPSinkWithoutQuerySlug(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync",
			Name: "Users Sync",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}, {
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}, {
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-ftp",
					OperationType:    "ftp",
					Config: map[string]any{
						"path":      "exports/users.json",
						"format":    "json",
						"overwrite": true,
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)

	specPath := filepath.Join(tempDir, "users-sync.pipeline.json")
	require.NoError(t, os.WriteFile(specPath, encoded, 0o600))

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)
		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("list-users"), querySlug)
		require.Empty(t, paramValues)

		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows: []map[string]any{{
				"id":    int64(1),
				"email": "alice@example.com",
			}},
			ColumnNames: []string{"id", "email"},
			RowCount:    1,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorContains(
		t,
		err,
		"sink target must set exactly one of operation_ref or operation_inline",
	)
}

func TestService_ExecuteRunsFTPSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-query", Name: "Users Sync Query"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}, {
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}, {
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-query",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-ftp",
					OperationType:    "ftp",
					OperationRef:     "ftp-upload-users",
					Config: map[string]any{
						"path":      "exports/users-from-query.xlsx",
						"format":    "spreadsheet",
						"overwrite": true,
						"sheet":     "Exports",
						"range":     "B2",
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)

	specPath := filepath.Join(tempDir, "users-sync-query.pipeline.json")
	require.NoError(t, os.WriteFile(specPath, encoded, 0o600))

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)

		if dsSlug == must.Slug("acme-postgres") {
			require.Equal(t, must.Slug("list-users"), querySlug)
			require.Empty(t, paramValues)

			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-ftp"), dsSlug)
		require.Equal(t, must.Slug("ftp-upload-users"), querySlug)
		require.Contains(t, paramValues, sinkbinding.Input)
		require.Equal(t, "exports/users-from-query.xlsx", paramValues["path"])
		require.Equal(t, "spreadsheet", paramValues["format"])
		require.Equal(t, true, paramValues["overwrite"])
		require.Equal(t, "Exports", paramValues["sheet"])
		require.Equal(t, "B2", paramValues["range"])

		content, ok := paramValues[sinkbinding.Input].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		firstRow, ok := content[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, int64(1), firstRow["id"])

		writtenRows := 1
		writtenBytes := 64
		return result.NewFTPResult(&result.FileResult{
			Format:       "spreadsheet",
			Operation:    "write",
			TargetPath:   "exports/users-from-query.xlsx",
			WrittenRows:  &writtenRows,
			WrittenBytes: &writtenBytes,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-query"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
	require.Len(t, run.Timeline.Snapshots, 3)
}

func TestService_ExecuteRunsGitSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-git", Name: "Users Sync Git"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}, {
						Name:     "email",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					}}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}, {
						Name:     "email",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					}}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-git",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-git",
					OperationType:    "git",
					OperationRef:     "git-write-users",
					Config: map[string]any{
						"path":       "exports/users.csv",
						"format":     "csv",
						"delimiter":  ";",
						"has_header": true,
						"overwrite":  true,
						"branch":     "main",
						"message":    "sync users",
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "users-sync-git.pipeline.json"), encoded, 0o600),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)

		if dsSlug == must.Slug("acme-postgres") {
			require.Equal(t, must.Slug("list-users"), querySlug)
			require.Empty(t, paramValues)
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-git"), dsSlug)
		require.Equal(t, must.Slug("git-write-users"), querySlug)

		require.Equal(t, "exports/users.csv", paramValues["path"])
		require.Equal(t, "csv", paramValues["format"])
		require.Equal(t, ";", paramValues["delimiter"])
		require.Equal(t, true, paramValues["has_header"])
		require.Equal(t, true, paramValues["overwrite"])
		require.Equal(t, "main", paramValues["branch"])
		require.Equal(t, "sync users", paramValues["message"])

		content, ok := paramValues[sinkbinding.Input].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		firstRow, ok := content[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, int64(1), firstRow["id"])

		writtenRows := 1
		writtenBytes := 32
		return result.NewGitResult(&result.FileResult{
			Format:       "csv",
			Operation:    "write",
			TargetPath:   "exports/users.csv",
			WrittenRows:  &writtenRows,
			WrittenBytes: &writtenBytes,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-git"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteRunsMongoSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-mongo", Name: "Users Sync Mongo"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-mongo",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-mongo",
					OperationType:    "mongodb",
					OperationRef:     "mongo-insert-users",
					Config: map[string]any{
						"mode": "bulk",
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "users-sync-mongo.pipeline.json"), encoded, 0o600),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)

		if dsSlug == must.Slug("acme-postgres") {
			require.Equal(t, must.Slug("list-users"), querySlug)
			require.Empty(t, paramValues)

			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows: []map[string]any{
					{"id": int64(1), "email": "alice@example.com"},
					{"id": int64(2), "email": "bob@example.com"},
				},
				ColumnNames: []string{"id", "email"},
				RowCount:    2,
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-mongo"), dsSlug)
		require.Equal(t, must.Slug("mongo-insert-users"), querySlug)
		require.Equal(t, "bulk", paramValues["mode"])

		content, ok := paramValues[sinkbinding.Input].([]any)
		require.True(t, ok)
		require.Len(t, content, 2)
		firstDocument, ok := content[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, int64(1), firstDocument["id"])
		require.Equal(t, "alice@example.com", firstDocument["email"])

		insertedCount := int64(2)
		return result.NewMongoDBResult(&result.MongoDBResult{
			OperationType: "insert_many",
			InsertedCount: &insertedCount,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-mongo"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteRunsMongoAggregateSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync-mongo-aggregate",
			Name: "Users Sync Mongo Aggregate",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-stage-pipeline",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-mongo",
					OperationType:  "mongodb",
					OperationRef:   "list-pipeline-stages",
					OutputContract: &payload.Contract{
						Representation: model.ContractRepresentationJSON,
						RootShape:      model.ContractRootShapeArray,
						Fields: []payload.ContractField{{
							Name:     "$match.status",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-mongo-aggregate",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-stage-pipeline"},
					InputContract: &payload.Contract{
						Representation: model.ContractRepresentationJSON,
						RootShape:      model.ContractRootShapeArray,
						Fields: []payload.ContractField{{
							Name:     "$match.status",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}},
					},
					DataSourceSlug: "acme-mongo",
					OperationType:  "mongodb",
					OperationRef:   "mongo-aggregate-users",
					Config: map[string]any{
						"mode": "pipeline",
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(tempDir, "users-sync-mongo-aggregate.pipeline.json"),
			encoded,
			0o600,
		),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)
		require.Equal(t, must.Slug("acme-mongo"), dsSlug)

		if querySlug == must.Slug("list-pipeline-stages") {
			require.Empty(t, paramValues)

			return result.NewMongoDBResult(&result.MongoDBResult{
				OperationType: "find",
				Documents: []map[string]any{{
					"$match": map[string]any{"status": "pending"},
				}},
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("mongo-aggregate-users"), querySlug)
		require.Equal(t, "pipeline", paramValues["mode"])

		pipelineStages, ok := paramValues[sinkbinding.Input].([]any)
		require.True(t, ok)
		require.Len(t, pipelineStages, 1)

		firstStage, ok := pipelineStages[0].(map[string]any)
		require.True(t, ok)

		matchStage, ok := firstStage["$match"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "pending", matchStage["status"])

		return result.NewMongoDBResult(&result.MongoDBResult{
			OperationType: "aggregate",
			Documents: []map[string]any{{
				"email": "alice@example.com",
			}},
		}, 1, nil), nil
	}

	schemas := testCachedSchemas()
	schemas[must.Slug("acme-mongo")] = datasourcessvc.Schema[datasourcessvc.DocumentSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeDocument,
		Type:            "mongodb",
		Payload: datasourcessvc.DocumentSchemaPayload{
			Collections: []datasourcessvc.SchemaCollection{{
				Name: "pipeline_stages",
				Fields: []datasourcessvc.SchemaField{{
					Name: "$match.status",
					Type: "string",
				}},
			}},
		},
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: schemas},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-mongo-aggregate"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteRunsDBSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	_, filePath, _, ok := runtime.Caller(0)
	require.True(t, ok)
	testdataDir := filepath.Join(filepath.Dir(filePath), "testdata")

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)

		if querySlug == must.Slug("list-users") {
			require.Equal(t, must.Slug("acme-postgres"), dsSlug)
			require.Empty(t, paramValues)
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("upsert-users"), querySlug)
		require.Equal(t, "upsert", paramValues["mode"])
		require.Equal(t, "users", paramValues["table"])

		inputRows, ok := paramValues[sinkbinding.InternalInputRows].([]map[string]any)
		require.True(t, ok)
		require.Len(t, inputRows, 1)
		require.Equal(t, int64(1), inputRows[0]["id"])

		inputColumns, ok := paramValues[sinkbinding.InternalInputColumns].([]string)
		require.True(t, ok)
		require.Equal(t, []string{"id", "email"}, inputColumns)

		contentJSON, ok := paramValues[sinkbinding.Input].(string)
		require.True(t, ok)

		var content []map[string]any
		err := json.Unmarshal([]byte(contentJSON), &content)
		require.NoError(t, err)
		require.Len(t, content, 1)
		idValue, ok := content[0]["id"].(float64)
		require.True(t, ok)
		require.InDelta(t, 1.0, idValue, 0.0)

		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(testdataDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteFailsFastOnQueryOutputContractSchemaDrift(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-drift", Name: "Users Sync Drift"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					}}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id AS id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "users-sync-drift.pipeline.json"), encoded, 0o600),
	)

	queryCalls := 0
	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, _ slug.Slug,
		_ model.EventActor,
		_ map[string]any,
	) (*result.ExecutionResult, error) {
		queryCalls++
		return result.NewSQLResult(
			"postgres",
			&result.SQLResult{Rows: []map[string]any{{"id": int64(1)}}, RowCount: 1},
			1,
			nil,
		), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{Tx: &fakeDBTX{}},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-drift"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorContains(t, err, "query output_contract mismatch")
	require.Equal(t, 0, queryCalls)
}

func TestService_ExecuteInfersQueryOutputContractWhenMissing(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-infer", Name: "Users Sync Infer"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id AS id, email AS email FROM input",
					OutputKind: model.TransformOutputKindRows,
					InputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "users-sync-infer.pipeline.json"), encoded, 0o600),
	)

	queryCalls := 0
	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		queryCalls++
		require.Equal(t, must.Slug("acme"), orgSlug)
		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("list-users"), querySlug)
		require.Empty(t, paramValues)
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
			ColumnNames: []string{"id", "email"},
			RowCount:    1,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-infer"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, 1, queryCalls)
	require.Equal(t, "succeeded", run.Metadata.Status)
	require.EqualValues(t, 2, run.Metadata.StepCount)
}

func TestService_ExecuteReturnsErrWhenHTTPSinkMissingQuerySlug(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync-http-missing-query",
			Name: "Users Sync HTTP Missing Query",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-http-missing-query",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-http",
					OperationType:    "http",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(tempDir, "users-sync-http-missing-query.pipeline.json"),
			encoded,
			0o600,
		),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, _ slug.Slug,
		_ model.EventActor,
		_ map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
			ColumnNames: []string{"id", "email"},
			RowCount:    1,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-http-missing-query"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorContains(
		t,
		err,
		"sink target must set exactly one of operation_ref or operation_inline",
	)
}

func TestService_ExecuteRunsHTTPSinkViaSavedQuery(t *testing.T) {
	t.Parallel()

	queryExec := &fakeQueryExecutor{}
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync-http", Name: "Users Sync HTTP"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-http",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-http",
					OperationType:    "http",
					OperationRef:     "post-users",
					Config: map[string]any{
						"target": "users-api",
					},
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "users-sync-http.pipeline.json"), encoded, 0o600),
	)

	queryExec.executeWithOptionsFn = func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		resolved := queriessvc.ExecuteOptions{RecordExecution: true}
		for _, opt := range opts {
			opt(&resolved)
		}

		require.Equal(t, must.Slug("acme"), orgSlug)

		if dsSlug == must.Slug("acme-postgres") {
			require.True(t, resolved.RecordExecution)
			require.Equal(t, must.Slug("list-users"), querySlug)
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows: []map[string]any{
					{"id": int64(1), "email": "alice@example.com"},
					{"id": int64(2), "email": "bob@example.com"},
				},
				ColumnNames: []string{"id", "email"},
				RowCount:    2,
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-http"), dsSlug)
		require.False(t, resolved.RecordExecution)
		require.Equal(t, must.Slug("post-users"), querySlug)
		require.Equal(t, "users-api", paramValues["target"])

		content, ok := paramValues[sinkbinding.Input].([]any)
		require.True(t, ok)
		require.Len(t, content, 2)
		firstRow, ok := content[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "alice@example.com", firstRow["email"])

		return result.NewHTTPResult(
			&result.HTTPResult{StatusCode: 202, Body: json.RawMessage(`{"accepted":true}`)},
			1,
			nil,
		), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-http"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteReturnsErrWhenHTTPSinkMissingDataSourceSlug(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync-http-missing-ds",
			Name: "Users Sync HTTP Missing DS",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-http-missing-ds",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					OperationType:    "http",
					OperationRef:     "post-users",
					// DataSourceSlug intentionally omitted to trigger the error.
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(tempDir, "users-sync-http-missing-ds.pipeline.json"),
			encoded,
			0o600,
		),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, _ slug.Slug,
		_ model.EventActor,
		_ map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
			ColumnNames: []string{"id", "email"},
			RowCount:    1,
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-http-missing-ds"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorContains(t, err, "sink target requires data_source_ref")
}

func TestService_ExecuteReturnsErrWhenHTTPSinkReturnsNon2xxStatus(t *testing.T) {
	t.Parallel()

	queryExec := &fakeQueryExecutor{}
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync-http-non2xx",
			Name: "Users Sync HTTP Non2xx",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: "fetch-users"}}, SQL: "SELECT id, email FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
						{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
					}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync-http-non2xx",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: "project-users"},
					DataSourceSlug:   "acme-http",
					OperationType:    "http",
					OperationRef:     "post-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(tempDir, "users-sync-http-non2xx.pipeline.json"),
			encoded,
			0o600,
		),
	)

	queryExec.executeWithOptionsFn = func(
		_ context.Context,
		_ slug.Slug,
		dsSlug, _ slug.Slug,
		_ model.EventActor,
		_ map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		resolved := queriessvc.ExecuteOptions{RecordExecution: true}
		for _, opt := range opts {
			opt(&resolved)
		}

		if dsSlug == must.Slug("acme-postgres") {
			require.True(t, resolved.RecordExecution)
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": int64(1), "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 1, nil), nil
		}
		require.False(t, resolved.RecordExecution)
		require.Equal(t, must.Slug("acme-http"), dsSlug)
		return result.NewHTTPResult(&result.HTTPResult{
			StatusCode: 503,
			Body: json.RawMessage(
				`{"error":"service unavailable","detail":"sensitive internal info"}`,
			),
		}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	_, err = svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("users-sync-http-non2xx"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.ErrorContains(t, err, "http sink returned non-2xx status 503")
	require.NotContains(t, err.Error(), "service unavailable")
	require.NotContains(t, err.Error(), "sensitive internal info")
}

func TestService_ExecuteDBSinkSetsNativeInputRelationFromJSONArrayPayload(t *testing.T) {
	t.Parallel()

	// Verify that when an upstream HTTP query returns a JSON array, the DB sink
	// receives InternalInputRows and InternalInputColumns so SQL connectors can
	// materialize a native FROM input relation.
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "json-to-db", Name: "JSON to DB"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-json",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-http",
					OperationType:  "http",
					OperationRef:   "fetch-users-json",
				},
			},
			{
				Kind: "sink",
				Slug: "json-to-db",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-json"},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: false,
					}}},
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "upsert-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "json-to-db.pipeline.json"), encoded, 0o600),
	)

	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, dsSlug, querySlug slug.Slug,
		_ model.EventActor,
		params map[string]any,
	) (*result.ExecutionResult, error) {
		if querySlug == must.Slug("fetch-users-json") {
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       json.RawMessage(`[{"email":"alice@example.com","id":1}]`),
			}, 1, nil), nil
		}

		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("upsert-users"), querySlug)

		inputRows, ok := params[sinkbinding.InternalInputRows].([]map[string]any)
		require.True(t, ok, "InternalInputRows must be set for JSON array payload")
		require.Len(t, inputRows, 1)
		require.InEpsilon(t, float64(1), inputRows[0]["id"], 1e-9)
		require.Equal(t, "alice@example.com", inputRows[0]["email"])

		inputColumns, ok := params[sinkbinding.InternalInputColumns].([]string)
		require.True(t, ok, "InternalInputColumns must be set for JSON array payload")
		require.Equal(t, []string{"email", "id"}, inputColumns)

		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("json-to-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
}

func TestService_ExecuteDBSinkSetsNativeInputRelationFromJSONObjectPayload(t *testing.T) {
	t.Parallel()

	// A single JSON object (not an array) produces a single-row native input relation.
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "json-obj-to-db", Name: "JSON Object to DB"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-json",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-http",
					OperationType:  "http",
					OperationRef:   "fetch-user-json",
				},
			},
			{
				Kind: "sink",
				Slug: "json-obj-to-db",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-json"},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: false,
					}}},
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "upsert-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "json-obj-to-db.pipeline.json"), encoded, 0o600),
	)

	var capturedParams map[string]any
	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, querySlug slug.Slug,
		_ model.EventActor,
		params map[string]any,
	) (*result.ExecutionResult, error) {
		if querySlug == must.Slug("fetch-user-json") {
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       json.RawMessage(`{"email":"bob@example.com","id":2}`),
			}, 1, nil), nil
		}
		capturedParams = params
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("json-obj-to-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)

	inputRows, ok := capturedParams[sinkbinding.InternalInputRows].([]map[string]any)
	require.True(t, ok, "InternalInputRows must be set for JSON object payload")
	require.Len(t, inputRows, 1)
	require.InEpsilon(t, float64(2), inputRows[0]["id"], 1e-9)

	inputColumns, ok := capturedParams[sinkbinding.InternalInputColumns].([]string)
	require.True(t, ok, "InternalInputColumns must be set for JSON object payload")
	require.Equal(t, []string{"email", "id"}, inputColumns)
}

func TestService_ExecuteDBSinkFallsBackToPublicBindingForScalarJSONPayload(t *testing.T) {
	t.Parallel()

	// Scalar JSON (string, number, null) cannot be normalized to rows:
	// InternalInputRows must NOT be set; the public input binding carries the raw value.
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "scalar-json-to-db", Name: "Scalar JSON to DB"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-scalar",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-http",
					OperationType:  "http",
					OperationRef:   "fetch-scalar-json",
				},
			},
			{
				Kind: "sink",
				Slug: "scalar-json-to-db",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-scalar"},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: false,
					}}},
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "upsert-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "scalar-json-to-db.pipeline.json"), encoded, 0o600),
	)

	var capturedParams map[string]any
	queryExec := &fakeQueryExecutor{}
	// No saved-query contract is registered; the explicit optional edge contract
	// lets executeDBSink exercise malformed payload fallback behavior.
	queryExec.getExpectedInputContractFn = func(_ context.Context, _, _, _ slug.Slug) (*payload.Contract, error) {
		return nil, nil //nolint:nilnil
	}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, querySlug slug.Slug,
		_ model.EventActor,
		params map[string]any,
	) (*result.ExecutionResult, error) {
		if querySlug == must.Slug("fetch-scalar-json") {
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       json.RawMessage(`"just a string"`),
			}, 1, nil), nil
		}
		capturedParams = params
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("scalar-json-to-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)

	_, hasInternalInputRows := capturedParams[sinkbinding.InternalInputRows]
	require.False(
		t,
		hasInternalInputRows,
		"InternalInputRows must not be set for scalar JSON",
	)
	require.NotNil(
		t,
		capturedParams[sinkbinding.Input],
		"public input binding must be present as fallback",
	)
}

func TestService_ExecuteDBSinkSetsNativeInputRelationFromCSVPayload(t *testing.T) {
	t.Parallel()

	// CSV payloads are parsed at sink runtime (first row = header) and routed
	// through the native input relation path so SQL can use FROM input.
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "csv-to-db", Name: "CSV to DB"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-csv",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-http",
					OperationType:  "http",
					OperationRef:   "fetch-users-csv",
				},
			},
			{
				Kind: "sink",
				Slug: "csv-to-db",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-csv"},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueString,
						Required: false,
					}}},
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "upsert-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "csv-to-db.pipeline.json"), encoded, 0o600),
	)

	var capturedParams map[string]any
	queryExec := &fakeQueryExecutor{}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, querySlug slug.Slug,
		_ model.EventActor,
		params map[string]any,
	) (*result.ExecutionResult, error) {
		if querySlug == must.Slug("fetch-users-csv") {
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "text/csv"},
				Body:       json.RawMessage("id,email\n1,alice@example.com\n2,bob@example.com"),
			}, 1, nil), nil
		}
		capturedParams = params
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 2}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("csv-to-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)

	inputRows, ok := capturedParams[sinkbinding.InternalInputRows].([]map[string]any)
	require.True(t, ok, "InternalInputRows must be set for CSV payload")
	require.Len(t, inputRows, 2)
	require.Equal(t, "1", inputRows[0]["id"])
	require.Equal(t, "alice@example.com", inputRows[0]["email"])

	inputColumns, ok := capturedParams[sinkbinding.InternalInputColumns].([]string)
	require.True(t, ok, "InternalInputColumns must be set for CSV payload")
	require.Equal(t, []string{"id", "email"}, inputColumns)
}

func TestService_ExecuteDBSinkFallsBackToPublicBindingForMalformedCSVPayload(t *testing.T) {
	t.Parallel()

	// Malformed CSV that cannot be parsed falls back to the public input binding
	// without setting InternalInputRows.
	tempDir := t.TempDir()
	pipelineSpec := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "bad-csv-to-db", Name: "Bad CSV to DB"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-bad-csv",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-http",
					OperationType:  "http",
					OperationRef:   "fetch-bad-csv",
				},
			},
			{
				Kind: "sink",
				Slug: "bad-csv-to-db",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-bad-csv"},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueString,
						Required: false,
					}}},
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "upsert-users",
				},
			},
		},
	}

	encoded, err := json.MarshalIndent(pipelineSpec, "", "  ")
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(tempDir, "bad-csv-to-db.pipeline.json"), encoded, 0o600),
	)

	var capturedParams map[string]any
	queryExec := &fakeQueryExecutor{}
	// No contract registered: skip validateSinkInputContract so executeDBSink is
	// reached even when the payload cannot be normalized to rows.
	queryExec.getExpectedInputContractFn = func(_ context.Context, _, _, _ slug.Slug) (*payload.Contract, error) {
		return nil, nil //nolint:nilnil
	}
	queryExec.executeFn = func(
		_ context.Context,
		_, _, querySlug slug.Slug,
		_ model.EventActor,
		params map[string]any,
	) (*result.ExecutionResult, error) {
		if querySlug == must.Slug("fetch-bad-csv") {
			// A field starting with an unmatched quote causes csv.Reader to return an
			// error, triggering ParseCSVBodyToRows to fail and the sink to fall back
			// to the public input binding.
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "text/csv"},
				Body:       json.RawMessage("id,\"unclosed"),
			}, 1, nil), nil
		}
		capturedParams = params
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 0}, 1, nil), nil
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		newDirectoryPipelineStore(tempDir),
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		queryExec,
		allowAuthorizationService{},
		testAdmissionService(),
		&fakeDataSourceService{schemas: testCachedSchemas()},
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		&fakeDataSourceRefResolver{},
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	run, err := svc.Execute(
		context.Background(),
		must.Slug("acme"),
		defaultProjectSlug,
		must.Slug("prod"),
		must.Slug("bad-csv-to-db"),
		nil,
		nil,
		model.UserEventActor(must.Slug("alice")),
		testRunSource(model.UserEventActor(must.Slug("alice"))),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)

	_, hasInternalInputRows := capturedParams[sinkbinding.InternalInputRows]
	require.False(
		t,
		hasInternalInputRows,
		"InternalInputRows must not be set for malformed CSV",
	)
	require.NotNil(
		t,
		capturedParams[sinkbinding.Input],
		"public input binding must be present as fallback",
	)
}
