package pipelinesvc

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestService_ValidatePipeline(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsValidResultForCanonicalSpec", func(t *testing.T) {
		t.Parallel()

		svc := newCRUDTestService(t, &fakeServicePipelineStore{})

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			testPipelineSpec(),
		)
		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("ReturnsStructuredDiagnosticForInvalidSpec", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.OperationInline = map[string]any{"sql": "select * from users"}

		svc := newCRUDTestService(t, &fakeServicePipelineStore{})

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)
		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "error", result.Diagnostics[0].Severity)
		require.Equal(t, "PIPELINE_INVALID_INPUT", result.Diagnostics[0].Code)
		require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
		require.NotNil(t, result.Diagnostics[0].StepSlug)
		require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
		require.Equal(
			t,
			"query target must set exactly one of operation_ref or operation_inline",
			result.Diagnostics[0].Message,
		)
	})

	t.Run("PersistsWarningContractImpactReportWithoutBlockingValidation", func(t *testing.T) {
		t.Parallel()

		deployed := testPipelineSpec()
		deployed.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}},
		}
		candidate := testPipelineSpec()
		candidate.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{
				{
					Name:     "id",
					Type:     model.ContractFieldTypeValueInteger,
					Required: true,
				},
				{
					Name: "email",
					Type: model.ContractFieldTypeValueString,
				},
			},
		}

		driftReports := &fakeDriftImpactRepository{}
		store := &fakeServicePipelineStore{
			getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
				return deployed, nil
			},
		}
		svcInterface, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
			store,
			&fakeDBTX{},
			testutil.FakeTransactionManager{},
			&fakeRunRepository{}, incrementalcheckpoints.New(),
			&fakeRunStore{},
			driftReports,
			&fakeQueryExecutor{},
			allowAuthorizationService{},
			testAdmissionService(),
			&fakeDataSourceService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{
					slug.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
						SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
						Type:            "postgres",
						Payload: datasourcessvc.SQLSchemaPayload{
							Tables: []datasourcessvc.SchemaTable{{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
									{Name: "email", Type: "text"},
								},
							}},
						},
					},
				},
			},
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
		svc := svcInterface.(*service)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			candidate,
		)
		require.NoError(t, err)
		require.True(t, result.Valid)
		require.NotNil(t, result.ImpactReport)
		require.Equal(t, pipelinedrift.ReportSeverityWarning, result.ImpactReport.Severity)
		require.Equal(t, pipelinedrift.ReportTypeContractImpact, result.ImpactReport.ReportType)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "warning", result.Diagnostics[0].Severity)
		require.Equal(t, "PIPELINE_CONTRACT_IMPACT_WARNING", result.Diagnostics[0].Code)
		require.Equal(t, 1, driftReports.supersedeOpenCalls)
		require.Equal(t, 1, driftReports.createCalls)
		require.NotEmpty(t, driftReports.created.Slug)
		require.JSONEq(t, `[{"kind":"addition","path":"step[fetch-users].query.output_contract.email","before":"<none>","after":"string(required=false,nullable=false)","severity":"warning","reason_code":"addition"}]`, string(driftReports.created.ChangesJSONB))
	})

	t.Run("DoesNotReportContractImpactWhenLogicalRefResolvesToConcreteDataSource", func(t *testing.T) {
		t.Parallel()

		stored := testPipelineSpec()
		stored.Steps[0].Query.DataSourceSlug = slug.Slug("users-source")
		stored.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}},
		}
		candidate := testPipelineSpec()
		candidate.Steps[0].Query.DataSourceSlug = slug.Slug("users-source")
		candidate.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}},
		}
		resolved := testPipelineSpec()
		resolved.Steps[0].Query.DataSourceSlug = slug.Slug("prod-users-postgres")
		resolved.Steps[0].Query.OutputContract = candidate.Steps[0].Query.OutputContract

		var schemaLookups []slug.Slug
		driftReports := &fakeDriftImpactRepository{}
		store := &fakeServicePipelineStore{
			getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
				return stored, nil
			},
		}
		resolver := &fakeDataSourceRefResolver{
			resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
				slug.Slug("prod"): resolved,
			},
		}
		dataSources := &fakeDataSourceService{
			findBySlugFn: func(
				_ context.Context,
				_ slug.Slug,
				dsSlug slug.Slug,
			) (*datasourcessvc.DataSource, error) {
				schemaLookups = append(schemaLookups, dsSlug)
				if dsSlug != slug.Slug("prod-users-postgres") {
					return nil, datasourcessvc.ErrDataSourceNotFound
				}
				return &datasourcessvc.DataSource{
					Slug: dsSlug,
					Type: model.DataSourceTypePostgres,
					Schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
						SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
						Type:            "postgres",
						Payload: datasourcessvc.SQLSchemaPayload{
							Tables: []datasourcessvc.SchemaTable{{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							}},
						},
					},
				}, nil
			},
		}
		svcInterface, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
			store,
			&fakeDBTX{},
			testutil.FakeTransactionManager{},
			&fakeRunRepository{}, incrementalcheckpoints.New(),
			&fakeRunStore{},
			driftReports,
			&fakeQueryExecutor{},
			allowAuthorizationService{},
			testAdmissionService(),
			dataSources,
			fakeProjectBackendService{},
			fakeGitSourceResolver{},
			clock.New(),
			&recordingPipelineRunProducer{},
			&recordingPipelineGitProducer{},
			eventtest.NewProducer(&fakePipelineEventRecorder{}),
			resolver,
			noopSinkEffectLedger{},
			newTestProofLinks(),
			testutil.FakeRunSucceededNotifier{})
		require.NoError(t, err)

		result, err := svcInterface.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			candidate,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
		require.Nil(t, result.ImpactReport)
		require.Equal(t, []slug.Slug{slug.Slug("prod-users-postgres")}, schemaLookups)
		require.Equal(t, 1, resolver.resolveCalls)
		require.Equal(t, 0, resolver.syncCalls)
		require.Equal(t, slug.Slug("prod"), resolver.lastEnvironmentSlug)
		require.Equal(t, slug.Slug("users-source"), resolver.lastPipeline.Steps[0].Query.DataSourceSlug)
		require.Equal(t, slug.Slug("users-source"), candidate.Steps[0].Query.DataSourceSlug)
		require.Equal(t, 1, store.getCalls)
		require.Equal(t, 0, driftReports.createCalls)
		require.Equal(t, 0, driftReports.supersedeOpenCalls)
	})

	t.Run("ReturnsContractImpactDiagnosticWhenReportPersistenceFails", func(t *testing.T) {
		t.Parallel()

		deployed := testPipelineSpec()
		deployed.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}},
		}
		candidate := testPipelineSpec()
		candidate.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{
				{
					Name:     "id",
					Type:     model.ContractFieldTypeValueInteger,
					Required: true,
				},
				{
					Name: "email",
					Type: model.ContractFieldTypeValueString,
				},
			},
		}

		driftReports := &fakeDriftImpactRepository{
			createErr: xerror.Unexpected().Detail("insert drift report"),
		}
		store := &fakeServicePipelineStore{
			getFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*pipelinespec.Pipeline, error) {
				return deployed, nil
			},
		}
		svcInterface, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
			store,
			&fakeDBTX{},
			testutil.FakeTransactionManager{},
			&fakeRunRepository{}, incrementalcheckpoints.New(),
			&fakeRunStore{},
			driftReports,
			&fakeQueryExecutor{},
			allowAuthorizationService{},
			testAdmissionService(),
			&fakeDataSourceService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{
					slug.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
						SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
						Type:            "postgres",
						Payload: datasourcessvc.SQLSchemaPayload{
							Tables: []datasourcessvc.SchemaTable{{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
									{Name: "email", Type: "text"},
								},
							}},
						},
					},
				},
			},
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

		result, err := svcInterface.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			candidate,
		)

		require.NoError(t, err)
		require.True(t, result.Valid)
		require.Nil(t, result.ImpactReport)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "warning", result.Diagnostics[0].Severity)
		require.Equal(t, "PIPELINE_CONTRACT_IMPACT_WARNING", result.Diagnostics[0].Code)
		require.Equal(t, "step[fetch-users].query.output_contract.email", result.Diagnostics[0].Field)
		require.Equal(t, 1, driftReports.supersedeOpenCalls)
		require.Equal(t, 1, driftReports.createCalls)
	})

	t.Run("ReturnsStructuredDiagnosticForCachedSchemaContractMismatch", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("postgres-main")
		pipeline.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{
				{
					Name:     "id",
					Type:     model.ContractFieldTypeValueString,
					Required: true,
				},
			},
		}

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)
		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
		require.NotNil(t, result.Diagnostics[0].StepSlug)
		require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
		require.Contains(t, result.Diagnostics[0].Message, "query output_contract mismatch")
		require.Contains(t, result.Diagnostics[0].Message, "type differs")
	})

	t.Run("ReturnsStructuredDiagnosticForMissingEdgeContract", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: slug.Slug("acme-postgres"),
						OperationType:  "sql",
						OperationRef:   slug.Slug("list-active-users"),
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: "fetch-users"},
						DataSourceSlug:   slug.Slug("acme-postgres"),
						OperationType:    "sql",
						OperationRef:     slug.Slug("upsert-users"),
					},
				},
			},
		}

		svc := newCRUDTestService(t, &fakeServicePipelineStore{})

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)
		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Contains(t, result.Diagnostics[0].Message, "fetch-users->load-users")
		require.Contains(t, result.Diagnostics[0].Message, "missing_contract")
		require.Contains(t, result.Diagnostics[0].Message, "producer output_contract or consumer input_contract")
	})

	t.Run("RechecksEdgeContractsAfterCachedSchemaDerivation", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: slug.Slug("postgres-main"),
						OperationType:  "sql",
						OperationRef:   slug.Slug("list-users"),
					},
				},
				{
					Kind: "transform",
					Slug: "normalize-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"input": {FromStep: slug.Slug("fetch-users")},
						},
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
						SQL:        "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: slug.Slug("normalize-users")},
						DataSourceSlug:   slug.Slug("postgres-main"),
						OperationType:    "sql",
						OperationRef:     slug.Slug("upsert-users"),
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
			},
		}

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Contains(t, result.Diagnostics[0].Message, "fetch-users->normalize-users")
		require.Contains(t, result.Diagnostics[0].Message, "email")
	})

	t.Run("RechecksInferredTransformOutputAfterPrevalidation", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: slug.Slug("postgres-main"),
						OperationType:  "sql",
						OperationRef:   slug.Slug("list-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"input": {FromStep: slug.Slug("fetch-users")},
						},
						SQL:        "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: slug.Slug("project-users")},
						DataSourceSlug:   slug.Slug("postgres-main"),
						OperationType:    "sql",
						OperationRef:     slug.Slug("upsert-users"),
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
			},
		}

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Contains(t, result.Diagnostics[0].Message, "project-users->load-users")
		require.Contains(t, result.Diagnostics[0].Message, "email")
	})

	t.Run("AllowsInferredCSVTransformOutputToSatisfyCSVStringSink", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: slug.Slug("postgres-main"),
						OperationType:  "sql",
						OperationRef:   slug.Slug("list-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "export-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"input": {FromStep: slug.Slug("fetch-users")},
						},
						SQL:        "SELECT id AS id FROM input",
						OutputKind: model.TransformOutputKindCSV,
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: slug.Slug("export-users")},
						DataSourceSlug:   slug.Slug("postgres-main"),
						OperationType:    "sql",
						OperationRef:     slug.Slug("upsert-users"),
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationCSV,
							Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}},
						},
					},
				},
			},
		}

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("AllowsNullableConsumerForInferredTransformOutput", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: slug.Slug("postgres-main"),
						OperationType:  "sql",
						OperationRef:   slug.Slug("list-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
							Nullable: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"input": {FromStep: slug.Slug("fetch-users")},
						},
						SQL:        "SELECT email AS email FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: slug.Slug("project-users")},
						DataSourceSlug:   slug.Slug("postgres-main"),
						OperationType:    "sql",
						OperationRef:     slug.Slug("upsert-users"),
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: false,
							Nullable: true,
						}}},
					},
				},
			},
		}

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "email", Type: "varchar"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("ResolvesLogicalDataSourceRefsBeforeCachedSchemaValidation", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("users-source")
		pipeline.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{
				{
					Name:     "id",
					Type:     model.ContractFieldTypeValueInteger,
					Required: true,
				},
			},
		}

		resolver := &fakeDataSourceRefResolver{
			resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
				slug.Slug("dev"): func() *pipelinespec.Pipeline {
					resolved := testPipelineSpec()
					resolved.Steps[0].Query.DataSourceSlug = slug.Slug("dev-users-postgres")
					resolved.Steps[0].Query.OutputContract = pipeline.Steps[0].Query.OutputContract
					return resolved
				}(),
				slug.Slug("prod"): func() *pipelinespec.Pipeline {
					resolved := testPipelineSpec()
					resolved.Steps[0].Query.DataSourceSlug = slug.Slug("prod-users-postgres")
					resolved.Steps[0].Query.OutputContract = pipeline.Steps[0].Query.OutputContract
					return resolved
				}(),
			},
		}
		svc := newValidationTestServiceWithDataSourceRefResolver(
			t,
			&fakeDataSourceService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{
					slug.Slug("prod-users-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
						SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
						Type:            "postgres",
						Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
								},
							},
						}},
					},
				},
			},
			resolver,
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
		require.Equal(t, slug.Slug("prod"), resolver.lastEnvironmentSlug)
		require.Equal(t, slug.Slug("users-source"), resolver.lastPipeline.Steps[0].Query.DataSourceSlug)
	})

	t.Run("ReturnsDiagnosticForMissingEnvironmentBinding", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("users-source")

		svc := newValidationTestServiceWithDataSourceRefResolver(
			t,
			&fakeDataSourceService{},
			&fakeDataSourceRefResolver{
				err: xerror.NotFound(xerror.DataSourceBinding).
					Detail(`validate steps: step[0] "fetch-users": data source binding users-source is not configured for environment prod`),
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
		require.Equal(t, "DATA_SOURCE_BINDING_NOT_FOUND", result.Diagnostics[0].Code)
		require.Contains(t, result.Diagnostics[0].Message, "users-source")
		require.Contains(t, result.Diagnostics[0].Message, "prod")
	})

	t.Run("ReturnsDiagnosticForMissingRuntimeVariable", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.Parameters = map[string]any{
			"prefix": "{{runtime.env.missing_prefix}}",
		}

		svc := newValidationTestServiceWithDataSourceRefResolver(
			t,
			&fakeDataSourceService{},
			&fakeDataSourceRefResolver{
				err: xerror.InvalidInput(xerror.RuntimeVariable).
					Detail(`validate steps: step[0] "fetch-users" query parameters: runtime variable missing_prefix is not defined for the selected environment`),
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "RUNTIME_VARIABLE_INVALID_INPUT", result.Diagnostics[0].Code)
		require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
		require.Contains(t, result.Diagnostics[0].Message, "missing_prefix")
	})

	t.Run("AcceptsRuntimeVariableLookbackWindowAfterEnvironmentResolution", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.Incremental = &pipelinespec.IncrementalSourceContract{
			CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
			Field:          "updated_at",
			Ordering:       []string{"updated_at asc"},
			DedupeKey:      "id",
			LookbackWindow: "{{runtime.env.lookback_window}}",
		}
		resolved := testPipelineSpec()
		resolved.Steps[0].Query.Incremental = &pipelinespec.IncrementalSourceContract{
			CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
			Field:          "updated_at",
			Ordering:       []string{"updated_at asc"},
			DedupeKey:      "id",
			LookbackWindow: "24h",
		}
		resolved.Steps[0].Query.OutputContract = &payload.Contract{Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
		}}

		svc := newValidationTestServiceWithDataSourceRefResolver(
			t,
			&fakeDataSourceService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
						Name: "users",
						Columns: []datasourcessvc.SchemaColumn{
							{Name: "id", Type: "integer"},
							{Name: "updated_at", Type: "timestamp"},
						},
					}}},
				},
			}},
			&fakeDataSourceRefResolver{
				resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
					slug.Slug("prod"): resolved,
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("AcceptsEmbeddedRuntimeVariableLookbackWindowBeforeEnvironmentResolution", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.Incremental = &pipelinespec.IncrementalSourceContract{
			CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
			Field:          "updated_at",
			Ordering:       []string{"updated_at asc"},
			DedupeKey:      "id",
			LookbackWindow: "{{runtime.env.lookback_hours}}h",
		}
		resolved := testPipelineSpec()
		resolved.Steps[0].Query.Incremental = &pipelinespec.IncrementalSourceContract{
			CheckpointKind: incrementalCheckpointKindPtr(model.IncrementalCheckpointKindWatermark),
			Field:          "updated_at",
			Ordering:       []string{"updated_at asc"},
			DedupeKey:      "id",
			LookbackWindow: "24h",
		}
		resolved.Steps[0].Query.OutputContract = &payload.Contract{Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
		}}

		svc := newValidationTestServiceWithDataSourceRefResolver(
			t,
			&fakeDataSourceService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
						Name: "users",
						Columns: []datasourcessvc.SchemaColumn{
							{Name: "id", Type: "integer"},
							{Name: "updated_at", Type: "timestamp"},
						},
					}}},
				},
			}},
			&fakeDataSourceRefResolver{
				resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
					slug.Slug("prod"): resolved,
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			slug.Slug("prod"),
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.Truef(t, result.Valid, "diagnostics: %+v", result.Diagnostics)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("ReturnsStructuredDiagnosticForTransformContractMissingField", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("postgres-main")
		pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
			Kind: "transform",
			Slug: slug.Slug("normalize-users"),
			Transform: &pipelinespec.TransformSpec{
				Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: slug.Slug("fetch-users")}},
				SQL:        "SELECT id AS id FROM input",
				OutputKind: model.TransformOutputKindRows,
				OutputContract: &payload.Contract{Fields: []payload.ContractField{
					{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					},
					{
						Name:     "email",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
					},
				}},
			},
		})

		svc := newValidationTestService(
			t,
			map[slug.Slug]datasourcessvc.SchemaResult{
				slug.Slug("postgres-main"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{
						Tables: []datasourcessvc.SchemaTable{
							{
								Name: "users",
								Columns: []datasourcessvc.SchemaColumn{
									{Name: "id", Type: "integer"},
									{Name: "email", Type: "varchar"},
								},
							},
						},
					},
				},
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "steps[1].transform", result.Diagnostics[0].Field)
		require.NotNil(t, result.Diagnostics[0].StepSlug)
		require.Equal(t, "normalize-users", *result.Diagnostics[0].StepSlug)
		require.Equal(t, "PIPELINE_TRANSFORM_CONTRACT_MISSING_FIELD", result.Diagnostics[0].Code)
		require.Contains(t, result.Diagnostics[0].Message, "required output contract field")
		require.Contains(t, result.Diagnostics[0].Message, "email")
	})

	t.Run("ReturnsStructuredDiagnosticForTransformOutputContractFieldValidation", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
			Kind: "transform",
			Slug: slug.Slug("normalize-users"),
			Transform: &pipelinespec.TransformSpec{
				Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: slug.Slug("fetch-users")}},
				SQL:        "SELECT id, email FROM input",
				OutputKind: model.TransformOutputKindRows,
				OutputContract: &payload.Contract{Fields: []payload.ContractField{
					{
						Name:     "email",
						Type:     model.ContractFieldTypeValueString,
						Required: true,
						Validations: &payload.ContractFieldValidations{
							Min: 10,
						},
					},
				}},
			},
		})

		svc := newCRUDTestService(t, &fakeServicePipelineStore{})

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.NoError(t, err)
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		require.Equal(t, "steps[1].transform", result.Diagnostics[0].Field)
		require.NotNil(t, result.Diagnostics[0].StepSlug)
		require.Equal(t, "normalize-users", *result.Diagnostics[0].StepSlug)
		require.Equal(t, "PIPELINE_INVALID_INPUT", result.Diagnostics[0].Code)
		require.Contains(t, result.Diagnostics[0].Message, `field "email" validation "min" is invalid`)
	})

	t.Run("PropagatesCachedSchemaLookupFailure", func(t *testing.T) {
		t.Parallel()

		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("postgres-main")
		pipeline.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{
				{
					Name:     "id",
					Type:     model.ContractFieldTypeValueString,
					Required: true,
				},
			},
		}

		svc := newValidationTestServiceWithDataSources(
			t,
			&fakeDataSourceService{
				findBySlugErr: xerror.Unavailable(xerror.SchemaMetadata).Detail("cached schema store unavailable"),
			},
		)

		result, err := svc.Validate(
			context.Background(),
			slug.Slug("acme"),
			defaultProjectSlug,
			noEnvironmentSlug,
			slug.Slug("test-user"),
			pipeline,
		)

		require.ErrorIs(t, err, xerror.Unavailable(xerror.SchemaMetadata))
		require.Nil(t, result)
	})
}

func TestService_ValidateDataSourceRefResolutionDiagnostics(t *testing.T) {
	t.Parallel()

	type validationCase struct {
		name               string
		memberSlug         slug.Slug
		pipeline           *pipelinespec.Pipeline
		resolver           *fakeDataSourceRefResolver
		dataSourceService  *fakeDataSourceService
		wantValid          bool
		wantDiagnosticCode string
		wantDiagnosticText string
	}

	logicalRefPipeline := func() *pipelinespec.Pipeline {
		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("users-source")
		return pipeline
	}
	resolvedContractMismatchPipeline := func() *pipelinespec.Pipeline {
		pipeline := testPipelineSpec()
		pipeline.Steps[0].Query.DataSourceSlug = slug.Slug("resolved-postgres")
		pipeline.Steps[0].Query.OutputContract = &payload.Contract{
			Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueString,
				Required: true,
			}},
		}
		return pipeline
	}
	schemaService := &fakeDataSourceService{
		schemas: map[slug.Slug]datasourcessvc.SchemaResult{
			slug.Slug("resolved-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
				SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
				Type:            "postgres",
				Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
					Name:    "users",
					Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
				}}},
			},
		},
	}

	for _, tc := range []validationCase{
		{
			name:       "ReadOnlyValidationDoesNotRunSync",
			memberSlug: slug.Slug("test-user"),
			pipeline:   logicalRefPipeline(),
			resolver: &fakeDataSourceRefResolver{
				syncErr: datasourcerefssvc.ErrDataSourceRefIncompatible.Detail(
					`validate steps: step[0] "fetch-users": data source ref users-source is bound in protected environment prod`,
				),
			},
			dataSourceService: &fakeDataSourceService{},
			wantValid:         true,
		},
		{
			name:       "ResolveDiagnosticFailure",
			memberSlug: slug.Slug("test-user"),
			pipeline:   logicalRefPipeline(),
			resolver: &fakeDataSourceRefResolver{
				err: xerror.NotFound(xerror.DataSourceBinding).Detail(
					`validate steps: step[0] "fetch-users": data source binding users-source is not configured for environment prod`,
				),
			},
			dataSourceService:  &fakeDataSourceService{},
			wantValid:          false,
			wantDiagnosticCode: "DATA_SOURCE_BINDING_NOT_FOUND",
			wantDiagnosticText: "users-source",
		},
		{
			name:       "LaterValidationFailureHasNoSyncResult",
			memberSlug: slug.Slug("test-user"),
			pipeline:   logicalRefPipeline(),
			resolver: &fakeDataSourceRefResolver{
				resolvedPipelines: map[slug.Slug]*pipelinespec.Pipeline{
					slug.Slug("prod"): resolvedContractMismatchPipeline(),
				},
			},
			dataSourceService:  schemaService,
			wantValid:          false,
			wantDiagnosticText: "query output_contract mismatch",
		},
		{
			name:              "SuccessfulReadOnlyValidation",
			memberSlug:        slug.Slug("test-user"),
			pipeline:          logicalRefPipeline(),
			resolver:          &fakeDataSourceRefResolver{},
			dataSourceService: &fakeDataSourceService{},
			wantValid:         true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := newValidationTestServiceWithDataSourceRefResolver(t, tc.dataSourceService, tc.resolver)

			result, err := svc.Validate(
				context.Background(),
				slug.Slug("acme"),
				defaultProjectSlug,
				slug.Slug("prod"),
				tc.memberSlug,
				tc.pipeline,
			)

			require.NoError(t, err)
			require.Equal(t, tc.wantValid, result.Valid)
			require.Equal(t, 0, tc.resolver.syncCalls)
			require.Nil(t, result.DataSourceRefSync)
			if tc.wantValid {
				require.Empty(t, result.Diagnostics)
				return
			}
			require.Len(t, result.Diagnostics, 1)
			if tc.wantDiagnosticCode != "" {
				require.Equal(t, tc.wantDiagnosticCode, result.Diagnostics[0].Code)
			}
			require.Equal(t, "steps[0].query", result.Diagnostics[0].Field)
			require.NotNil(t, result.Diagnostics[0].StepSlug)
			require.Equal(t, "fetch-users", *result.Diagnostics[0].StepSlug)
			require.Contains(t, result.Diagnostics[0].Message, tc.wantDiagnosticText)
		})
	}
}

func TestPipelineValidationDiagnosticFromError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantCode     string
		wantField    string
		wantStepSlug *string
		wantMessage  string
	}{
		{
			name: "SinkTarget",
			err: xerror.InvalidInput(xerror.Pipeline).Detail(
				`validate steps: step[2] "write-users": sink target must set exactly one of operation_ref or operation_inline`,
			),
			wantCode:     "PIPELINE_INVALID_INPUT",
			wantField:    "steps[2].sink",
			wantStepSlug: strPtr("write-users"),
			wantMessage:  "sink target must set exactly one of operation_ref or operation_inline",
		},
		{
			name: "Transform",
			err: xerror.InvalidInput(xerror.Pipeline).Detail(
				`validate steps: step[1] "shape-users": transform output_contract is required`,
			),
			wantCode:     "PIPELINE_INVALID_INPUT",
			wantField:    "steps[1].transform",
			wantStepSlug: strPtr("shape-users"),
			wantMessage:  "transform output_contract is required",
		},
		{
			name: "ContractFieldValidation",
			err: xerror.InvalidInput(xerror.Pipeline).Detail(
				`validate steps: step[0] "fetch-users" output_contract: field "email" validation "min" is invalid: min/max require number, integer, or datetime field type`,
			),
			wantCode:     "PIPELINE_INVALID_INPUT",
			wantField:    "steps[0].query",
			wantStepSlug: strPtr("fetch-users"),
			wantMessage:  `field "email" validation "min" is invalid: min/max require number, integer, or datetime field type`,
		},
		{
			name: "StepFallback",
			err: xerror.InvalidInput(xerror.Pipeline).Detail(
				`validate steps: step[3] "notify-users": unsupported step configuration`,
			),
			wantCode:     "PIPELINE_INVALID_INPUT",
			wantField:    "steps[3]",
			wantStepSlug: strPtr("notify-users"),
			wantMessage:  "unsupported step configuration",
		},
		{
			name:        "PlainError",
			err:         assertAnError("plain validation failure"),
			wantCode:    "PIPELINE_VALIDATION_FAILED",
			wantField:   "spec",
			wantMessage: "plain validation failure",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := pipelineValidationDiagnosticFromError(tc.err)

			require.Equal(t, "error", got.Severity)
			require.Equal(t, tc.wantCode, got.Code)
			require.Equal(t, tc.wantField, got.Field)
			require.Equal(t, tc.wantMessage, got.Message)
			if tc.wantStepSlug == nil {
				require.Nil(t, got.StepSlug)
			} else {
				require.NotNil(t, got.StepSlug)
				require.Equal(t, *tc.wantStepSlug, *got.StepSlug)
			}
		})
	}
}

type assertAnError string

func (e assertAnError) Error() string {
	return string(e)
}

func newValidationTestService(
	t *testing.T,
	schemas map[slug.Slug]datasourcessvc.SchemaResult,
) Service {
	t.Helper()

	return newValidationTestServiceWithDataSources(t, &fakeDataSourceService{schemas: schemas})
}

func newValidationTestServiceWithDataSources(
	t *testing.T,
	dataSourceService *fakeDataSourceService,
) Service {
	t.Helper()

	return newValidationTestServiceWithDataSourceRefResolver(t, dataSourceService, nil)
}

func newValidationTestServiceWithDataSourceRefResolver(
	t *testing.T,
	dataSourceService *fakeDataSourceService,
	resolver pipelineRuntimeResolver,
) Service {
	t.Helper()
	if resolver == nil {
		resolver = &fakeDataSourceRefResolver{}
	}

	svc, err := New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		&fakeServicePipelineStore{},
		&fakeDBTX{},
		testutil.FakeTransactionManager{},
		&fakeRunRepository{}, incrementalcheckpoints.New(),
		&fakeRunStore{},
		&fakeDriftImpactRepository{},
		&fakeQueryExecutor{},
		allowAuthorizationService{},
		testAdmissionService(),
		dataSourceService,
		fakeProjectBackendService{},
		fakeGitSourceResolver{},
		clock.New(),
		&recordingPipelineRunProducer{},
		&recordingPipelineGitProducer{},
		eventtest.NewProducer(&fakePipelineEventRecorder{}),
		resolver,
		noopSinkEffectLedger{},
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	return svc
}

type fakeDataSourceRefResolver struct {
	resolvedPipelines   map[slug.Slug]*pipelinespec.Pipeline
	err                 error
	syncResult          *datasourcerefssvc.PipelineDataSourceRefSyncResult
	syncErr             error
	syncCalls           int
	reconcileCalls      int
	lastEnvironmentSlug slug.Slug
	lastPipeline        *pipelinespec.Pipeline
	lastSyncPipeline    *pipelinespec.Pipeline
	lastSyncActor       model.EventActor
	resolveCalls        int
}

func (f *fakeDataSourceRefResolver) SyncPipelineDataSourceRefs(
	_ context.Context,
	_, _ slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	f.syncCalls++
	f.lastSyncPipeline = pipeline
	f.lastSyncActor = actor
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	if f.syncResult != nil {
		return f.syncResult, nil
	}
	return &datasourcerefssvc.PipelineDataSourceRefSyncResult{Items: []datasourcerefssvc.PipelineDataSourceRefSyncItem{}}, nil
}

func (f *fakeDataSourceRefResolver) ReconcilePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	f.reconcileCalls++
	return f.SyncPipelineDataSourceRefs(ctx, orgSlug, projectSlug, pipeline, actor)
}

func (f *fakeDataSourceRefResolver) DeleteDataSourceRefsForPipeline(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.EventActor,
) error {
	return nil
}

func (f *fakeDataSourceRefResolver) ResolvePipelineDataSourceRefs(
	_ context.Context,
	_, _, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*datasourcerefssvc.ResolvedPipeline, error) {
	f.resolveCalls++
	return f.resolvePipelineDataSourceRefs(environmentSlug, pipeline)
}

func (f *fakeDataSourceRefResolver) ResolveRuntimeSecrets(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

func (f *fakeDataSourceRefResolver) resolvePipelineDataSourceRefs(
	environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*datasourcerefssvc.ResolvedPipeline, error) {
	f.lastEnvironmentSlug = environmentSlug
	f.lastPipeline = pipeline
	if f.err != nil {
		return nil, f.err
	}
	resolved := f.resolvedPipelines[environmentSlug]
	if resolved == nil {
		resolved = pipeline
	}
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: resolved}, nil
}

type fakeDriftImpactRepository struct {
	supersedeOpenCalls int
	createCalls        int
	created            *driftimpactrepo.Report
	supersedeOpenErr   error
	createErr          error
}

func (f *fakeDriftImpactRepository) SupersedeOpen(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	driftimpactrepo.CreateParams,
) error {
	f.supersedeOpenCalls++
	return f.supersedeOpenErr
}

func (f *fakeDriftImpactRepository) Create(
	_ context.Context,
	_ postgres.DBTX,
	_ slug.Slug,
	params driftimpactrepo.CreateParams,
) (*driftimpactrepo.Report, error) {
	f.createCalls++
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = &driftimpactrepo.Report{
		Slug:           params.Slug,
		ReportType:     params.ReportType,
		Status:         params.Status,
		Severity:       params.Severity,
		Summary:        params.Summary,
		ChangesJSONB:   params.ChangesJSONB,
		ImpactsJSONB:   params.ImpactsJSONB,
		ProjectSlug:    params.ProjectSlug,
		PipelineSlug:   params.PipelineSlug,
		DataSourceSlug: params.DataSourceSlug,
	}
	return f.created, nil
}

func (f *fakeDriftImpactRepository) FindBySlug(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*driftimpactrepo.Report, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func (f *fakeDriftImpactRepository) List(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	driftimpactrepo.ListFilters,
) ([]*driftimpactrepo.Report, error) {
	return nil, errFakeRepositoryMethodNotUsed
}

func TestDisconnectedExecutionBecomesValidationDiagnostic(t *testing.T) {
	t.Parallel()
	err := pipelinevalidate.ErrOrphanStep.Detail("disconnected data-flow groups")
	diagnostic, ok := pipelineValidationResultFromExecutionError(err, testPipelineSpec())
	require.True(t, ok)
	require.False(t, diagnostic.Valid)
	require.Equal(t, "PIPELINE_ORPHAN_STEP", diagnostic.Diagnostics[0].Code)
	require.Contains(t, diagnostic.Diagnostics[0].Message, "disconnected data-flow groups")
}

func TestService_ValidateS3SinkConfiguration(t *testing.T) {
	pipeline := testPipelineSpec()
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: "store", Sink: &pipelinespec.SinkSpec{
		Input:          &pipelinespec.StepInput{FromStep: "fetch-users"},
		DataSourceSlug: "archive", OperationType: "s3",
		OperationInline:  map[string]any{"operation": "write", "key": "private-object", "content": "private-payload"},
		IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe,
	}})
	require.ErrorContains(t, pipelinevalidate.Validate(pipeline), "sink.config.key")
	svc := newCRUDTestService(t, &fakeServicePipelineStore{})
	validation, err := svc.Validate(t.Context(), "acme", defaultProjectSlug, noEnvironmentSlug, "test-user", pipeline)
	require.NoError(t, err)
	require.False(t, validation.Valid)
	require.Len(t, validation.Diagnostics, 1)
	diagnostic := validation.Diagnostics[0]
	require.Equal(t, "PIPELINE_INVALID_INPUT", diagnostic.Code)
	require.Equal(t, "steps[1].sink", diagnostic.Field)
	require.Equal(t, "store", *diagnostic.StepSlug)
	require.Contains(t, diagnostic.Message, "sink.config.key")
	require.NotContains(t, diagnostic.Message, "private-")
	pipeline.Steps[1].Sink.Config = map[string]any{"key": "exports/users.json"}
	require.NoError(t, pipelinevalidate.Validate(pipeline))
}
