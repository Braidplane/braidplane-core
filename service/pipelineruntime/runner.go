package pipelineruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipelinegraph "github.com/Braidplane/braidplane-core/pkg/pipeline/graph"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/redaction"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

const (
	failureKindPlatform     = "platform"
	failureKindDataSource   = "data_source"
	failureSideSource       = "source"
	failureSideSink         = "sink"
	failureSideTransform    = "transform"
	runStatusSucceeded      = "succeeded"
	metricOutcomeError      = "error"
	checkpointStatusSkipped = "skipped"
	checkpointStatusBlocked = "blocked"
	transformOutcomeSuccess = "success"
)

var (
	ErrNilPipeline              = xerror.InvalidRequired(xerror.Pipeline)
	ErrQueryExecutorNotProvided = xerror.UnexpectedRequired(xerror.QueryExecutor)
	ErrReplayRunSlugRequired    = xerror.InvalidRequired(xerror.ReplayRunSlug)
	ErrReplayRunNotFound        = xerror.ReplayRunNotFound()
	ErrReplayStepNotFound       = xerror.ReplayStepNotFound()
	ErrReplayUnsupportedMode    = xerror.ReplayUnsupportedMode()
	ErrReplayUnsafe             = xerror.ReplayUnsafe()
	ErrNilRunStore              = xerror.UnexpectedRequired(xerror.RunStore)
	ErrNilDataSourceService     = xerror.UnexpectedRequired(xerror.DataSourceService)
	ErrNilSinkRuntime           = xerror.UnexpectedRequired(xerror.SinkRuntime)

	ErrMissingOrgSlug         = xerror.InvalidRequired(xerror.OrganizationSlug)
	ErrMissingEnvironmentSlug = xerror.InvalidRequired(xerror.ProjectEnvironment).Detail("environment slug is required")

	ErrTransformUpstreamRequired         = xerror.TransformUpstreamRequired()
	ErrSinkUpstreamRequired              = xerror.SinkUpstreamRequired()
	ErrTransformBindingMissing           = xerror.TransformBindingMissing()
	ErrTransformBindingSourceNotFound    = xerror.TransformBindingSourceNotFound()
	ErrTransformBindingSourceAmbiguous   = xerror.TransformBindingSourceAmbiguous()
	ErrTransformBindingNodeUnsupported   = xerror.TransformBindingNodeUnsupported()
	ErrQueryOutputContractSchemaMismatch = xerror.QueryOutputContractSchemaMismatch()
	ErrSinkSourceResultNotFound          = xerror.SinkSourceResultNotFound()
)

// ReplayInputMode controls where replay input values come from.
//
//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=ReplayInputMode -json -linecomment -output=runner_replayinputmode_enumer.go
type ReplayInputMode int

const (
	// ReplayInputModePinned reuses the exact stored checkpoint input.
	ReplayInputModePinned ReplayInputMode = iota // pinned
)

type queryExecutor interface {
	Execute(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	GetExpectedInputContract(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
	) (*payload.Contract, error)
	ExecuteInline(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
}

type incrementalQueryExecutor interface {
	ExecuteIncremental(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

type incrementalInlineQueryExecutor interface {
	ExecuteInlineIncremental(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
		checkpoint *pipelinerun.IncrementalCheckpointInput,
	) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

type transformRuntime interface {
	Execute(
		ctx context.Context,
		orgSlug slug.Slug,
		transform *pipelinespec.TransformSpec,
		upstreamPayloads map[string]*payload.Payload,
	) (*payload.Output, error)
}

// TransformExecutionRequest is the bounded payload sent to an isolated executor.
type TransformExecutionRequest struct {
	OrganizationSlug slug.Slug                   `json:"organization_slug"`
	Transform        transformsql.Transform      `json:"transform"`
	UpstreamPayloads map[string]*payload.Payload `json:"upstream_payloads"`
	Limits           transformsql.Limits         `json:"limits"`
}

type TransformExecutor interface {
	ExecuteTransform(context.Context, TransformExecutionRequest) (*payload.Output, error)
}

type remoteTransformRuntime struct {
	executor TransformExecutor
	limits   transformsql.Limits
}

type sqlTransformRuntime struct {
	runtime transformsql.Runtime
}

// SinkRuntime executes sink-step delivery logic using resolved upstream output.
type SinkRuntime interface {
	Execute(ctx context.Context, input *SinkRuntimeInput) error
}

// SinkRuntimeInput contains the resolved output passed to sink runtime.
type SinkRuntimeInput struct {
	OperationSnapshot      *contractplan.OperationContent
	OrgSlug                slug.Slug
	ProjectSlug            slug.Slug
	EnvironmentSlug        slug.Slug
	PipelineSlug           slug.Slug
	RunSlug                string
	ReplayIdentity         string
	SinkStepSlug           slug.Slug
	InputKind              model.TransformOutputKind
	SourceStep             slug.Slug
	SourceResult           *pipelinerun.StepResult
	SinkBackend            model.SinkBackend
	SinkDataSourceSlug     slug.Slug
	SinkDataSourceReadOnly bool
	SinkOperationType      string
	SinkOperationRef       slug.Slug
	SinkOperationInline    map[string]any
	SinkConfig             map[string]any
	IdempotencyClass       pipelinespec.SinkIdempotencyClass
	IdempotencyKey         string
	ReplayDecision         string
	EffectState            pipelinerun.SinkEffectState
	EffectStartedAt        time.Time
	EmptyInputPolicy       string
	DeliveryOutcome        string
	SinkResult             *result.ExecutionResult
}

// EnvironmentSecretResolver supplies decrypted environment secret values inside the run boundary.
type EnvironmentSecretResolver interface {
	ResolveRuntimeSecrets(ctx context.Context, orgSlug, environmentSlug slug.Slug) ([]model.RuntimeSecretValue, error)
}

type RunnerOption func(*Runner)

// WithEnvironmentSecretResolver enables runtime resolution for {{runtime.secrets.KEY}} placeholders.
func WithEnvironmentSecretResolver(resolver EnvironmentSecretResolver) RunnerOption {
	return func(r *Runner) {
		r.environmentSecretResolver = resolver
	}
}

// WithTransformLimits applies cumulative runtime budgets to every SQL transform.
func WithTransformLimits(limits transformsql.Limits) RunnerOption {
	return func(r *Runner) {
		r.transformRuntime = newSQLTransformRuntime(limits)
		r.transformLimits = limits
	}
}

// WithTransformExecutor moves transform materialization to the isolated executor.
func WithTransformExecutor(executor TransformExecutor, limits transformsql.Limits) RunnerOption {
	return func(r *Runner) {
		if executor != nil {
			r.transformRuntime = remoteTransformRuntime{executor: executor, limits: limits}
			r.transformLimits = limits
		}
	}
}

// Runner executes a minimal pipeline by delegating query steps to the existing query service.
type Runner struct {
	transformLimits           transformsql.Limits
	executor                  queryExecutor
	transformRuntime          transformRuntime
	sinkRuntime               SinkRuntime
	clock                     clock.Clock
	store                     pipelinerun.RunStore
	dataSourceService         datasourcessvc.Service
	environmentSecretResolver EnvironmentSecretResolver
}

// NewRunner creates a runner with explicit run-store, datasource service,
// and sink runtime dependencies.
func NewRunner(
	executor queryExecutor,
	clk clock.Clock,
	store pipelinerun.RunStore,
	dataSourceService datasourcessvc.Service,
	sinkRuntime SinkRuntime,
	options ...RunnerOption,
) (*Runner, error) {
	if clk == nil {
		clk = clock.New()
	}
	if store == nil {
		return nil, ErrNilRunStore
	}
	if dataSourceService == nil {
		return nil, ErrNilDataSourceService
	}
	if sinkRuntime == nil {
		return nil, ErrNilSinkRuntime
	}

	runner := &Runner{
		executor:          executor,
		transformRuntime:  newSQLTransformRuntime(),
		sinkRuntime:       sinkRuntime,
		clock:             clk,
		store:             store,
		dataSourceService: dataSourceService,
	}
	for _, option := range options {
		if option != nil {
			option(runner)
		}
	}

	return runner, nil
}

func newSQLTransformRuntime(limits ...transformsql.Limits) sqlTransformRuntime {
	runtime := transformsql.NewRuntime(limits...).WithObserver(func(usage transformsql.Usage, err error) {
		outcome := transformOutcomeSuccess
		if err != nil {
			outcome = "error"
		}
		metrics.RecordTransformExecution(metrics.TransformExecutionMetric{
			Outcome:              outcome,
			LimitType:            transformBudgetLimitType(err),
			Duration:             usage.Duration,
			Work:                 usage.Work,
			IntermediateRows:     usage.IntermediateRows,
			IntermediateBytes:    usage.IntermediateBytes,
			OutputRows:           usage.OutputRows,
			OutputBytes:          usage.OutputBytes,
			MaxDepth:             usage.MaxDepth,
			RoundedDecimalValues: usage.RoundedDecimalValues,
			InexactDecimalValues: usage.InexactDecimalValues,
		})
	})
	return sqlTransformRuntime{runtime: runtime}
}

func transformBudgetLimitType(err error) string {
	switch {
	case xerror.Is(err, transformsql.ErrWorkBudgetExceeded):
		return "work"
	case xerror.Is(err, transformsql.ErrIntermediateRowsExceeded):
		return "intermediate_rows"
	case xerror.Is(err, transformsql.ErrIntermediateBytesExceeded):
		return "intermediate_bytes"
	case xerror.Is(err, transformsql.ErrOutputRowsExceeded):
		return "output_rows"
	case xerror.Is(err, transformsql.ErrOutputBytesExceeded):
		return "output_bytes"
	case xerror.Is(err, transformsql.ErrDepthBudgetExceeded):
		return "depth"
	case xerror.Is(err, transformsql.ErrExecutionBudgetInterrupted):
		return "timeout_or_cancellation"
	default:
		return "none"
	}
}

// sqlTransformRuntime adapts the service-owned TransformSpec orchestration
// surface to the neutral SQL transform package. Remove this wrapper once
// specs/results move fully behind narrower pipeline packages.
func (r sqlTransformRuntime) Execute(
	ctx context.Context,
	_ slug.Slug,
	transform *pipelinespec.TransformSpec,
	upstreamPayloads map[string]*payload.Payload,
) (*payload.Output, error) {
	if transform == nil {
		return nil, xerror.UnexpectedTransformSpecRequired()
	}

	output, err := r.runtime.ExecuteContext(
		ctx,
		transformsql.Transform{
			SQL:           transform.SQL,
			OutputKind:    transform.OutputKind,
			InputContract: transform.InputContract, InputContracts: resolvedNamedInputContracts(transform),
		},
		upstreamPayloads,
	)
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r remoteTransformRuntime) Execute(
	ctx context.Context,
	orgSlug slug.Slug,
	transform *pipelinespec.TransformSpec,
	upstreamPayloads map[string]*payload.Payload,
) (*payload.Output, error) {
	if transform == nil {
		return nil, xerror.UnexpectedTransformSpecRequired()
	}
	return r.executor.ExecuteTransform(ctx, TransformExecutionRequest{
		OrganizationSlug: orgSlug,
		Transform: transformsql.Transform{
			SQL: transform.SQL, OutputKind: transform.OutputKind,
			InputContract: transform.InputContract, InputContracts: resolvedNamedInputContracts(transform), OutputContract: transform.OutputContract,
		},
		UpstreamPayloads: upstreamPayloads,
		Limits:           r.limits,
	})
}

// PreValidateTransforms performs pre-flight validation of query and transform steps.
// Query output contracts are derived from cached schema when absent, and validated against cached
// schema when provided. Transform contracts are then validated against inferred upstream input
// types. This method returns an error if any query/transform prevalidation fails.
func (r *Runner) PreValidateTransforms(
	ctx context.Context,
	orgSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) error {
	return r.preValidateTransforms(ctx, orgSlug, pipeline, pipeline.UsesContractComposition())
}

func (r *Runner) preValidateTransforms(ctx context.Context, orgSlug slug.Slug, pipeline *pipelinespec.Pipeline, composition bool) (validationErr error) {
	if pipeline == nil {
		return ErrNilPipeline
	}
	if orgSlug == "" {
		return ErrMissingOrgSlug
	}
	if r.dataSourceService == nil {
		return ErrNilDataSourceService
	}

	normalized, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return err
	}

	hasTransform := false
	for _, step := range pipeline.Steps {
		if step.Transform != nil {
			hasTransform = true
			break
		}
	}

	type stepSchema struct {
		contract        *payload.Contract
		dataSourceType  model.DataSourceType
		inputFieldTypes map[string]model.ContractFieldType
	}

	originalIndexBySlug := make(map[slug.Slug]int, len(pipeline.Steps))
	for i, step := range pipeline.Steps {
		originalIndexBySlug[step.Slug] = i
	}

	deriveTransformOutputForStep := transformOutputDerivationRequirements(normalized)
	stepSchemas := make(map[slug.Slug]stepSchema, len(pipeline.Steps))
	var validatingStep pipelinespec.Step
	defer func() {
		if validationErr != nil && validatingStep.Slug != "" {
			validationErr = &prevalidationStepError{step: validatingStep, cause: validationErr}
		}
	}()

	for _, step := range normalized.TopologicalSteps() {
		validatingStep = step
		i := originalIndexBySlug[step.Slug]
		if step.Input != nil {
			stepSchemas[step.Slug] = stepSchema{
				inputFieldTypes: outputContractToFieldTypes(step.Input.OutputContract),
				contract:        step.Input.OutputContract,
			}
			continue
		}
		if step.Query != nil {
			if !hasTransform &&
				(step.Query.OutputContract == nil || len(step.Query.OutputContract.Fields) == 0) {
				continue
			}

			ds, err := r.dataSourceService.FindBySlug(
				ctx,
				orgSlug,
				step.Query.DataSourceSlug,
			)
			if err != nil {
				return xerror.Wrapf(
					"step[%d] %q load cached schema for datasource %q: %w",
					i,
					step.Slug,
					step.Query.DataSourceSlug,
					err,
				)
			}
			if ds == nil {
				return xerror.Wrapf(
					"step[%d] %q load cached schema for datasource %q: %w",
					i,
					step.Slug,
					step.Query.DataSourceSlug,
					datasourcessvc.ErrDataSourceNotFound,
				)
			}
			schema := ds.Schema
			if schema == nil {
				if composition {
					// Unknown provider shape retains the declared runtime checks.
					stepSchemas[step.Slug] = stepSchema{inputFieldTypes: outputContractToFieldTypes(step.Query.OutputContract), contract: step.Query.OutputContract}
					continue
				}
				return xerror.Unexpected().Detailf(
					"step[%d] %q cached schema is unavailable for datasource %q",
					i,
					step.Slug,
					step.Query.DataSourceSlug,
				)
			}

			nativeFieldTypes, dataSourceType, err := transformSchemaFieldTypes(schema)
			if err != nil {
				return xerror.Wrapf("step[%d] %q schema mapping failed: %w", i, step.Slug, err)
			}

			derivedContract := outputContractFromNativeFieldTypes(
				dataSourceType,
				nativeFieldTypes,
			)

			queryOutputContract := step.Query.OutputContract
			if queryOutputContract != nil && len(queryOutputContract.Fields) > 0 {
				if err := validateQueryOutputContractMatchesSchema(
					step.Query.OutputContract,
					derivedContract,
				); err != nil {
					return xerror.Wrapf(
						"step[%d] %q query output_contract mismatch for datasource %q: %w",
						i,
						step.Slug,
						step.Query.DataSourceSlug,
						err,
					)
				}
			} else {
				queryOutputContract = derivedContract
				if !composition {
					step.Query.OutputContract = derivedContract
				}
			}

			stepSchemas[step.Slug] = stepSchema{
				dataSourceType:  dataSourceType,
				inputFieldTypes: outputContractToFieldTypes(queryOutputContract),
				contract:        queryOutputContract,
			}
		}

		if step.Transform == nil {
			continue
		}

		upstreamStepSlugs, err := resolveTransformInputStepSlugsFromGraph(normalized, step.Slug)
		if err != nil {
			return xerror.Wrapf("step[%d] %q: %w", i, step.Slug, err)
		}

		upstreamSchema := stepSchema{
			dataSourceType:  model.DataSourceTypeUnknown,
			inputFieldTypes: make(map[string]model.ContractFieldType),
		}
		inputContracts := make(map[string]*payload.Contract, len(upstreamStepSlugs))
		inputNames := make([]string, 0, len(upstreamStepSlugs))
		for inputName := range upstreamStepSlugs {
			inputNames = append(inputNames, inputName)
		}
		sort.Strings(inputNames)
		for _, inputName := range inputNames {
			inputStepSlug := upstreamStepSlugs[inputName]
			inputSchema, ok := stepSchemas[inputStepSlug]
			inputContracts[inputName] = inputSchema.contract
			requirement := transformsql.ContractForNamedInput(step.Transform.InputContract, inputName, len(inputNames))
			if attachment := step.Transform.InputContracts[inputName]; attachment != nil {
				requirement = attachment.Contract
			}
			if requirement != nil {
				inputContracts[inputName] = requirement
				known := payload.CloneContract(requirement)
				for index, required := range requirement.Fields {
					known.Fields[index].Validations = nil
					actualType, exists := inputSchema.inputFieldTypes[required.Name]
					if exists && actualType != model.ContractFieldTypeUnknown {
						known.Fields[index].Type = actualType
						if actualType != model.ContractFieldTypeValueDecimal {
							known.Fields[index].Precision, known.Fields[index].Scale = nil, nil
						}
					}
				}
				diagnostic := pipelinevalidate.EvaluateEdgeCompatibility(pipelinevalidate.EdgeLink{FromStep: inputStepSlug, ToStep: step.Slug}, known, requirement)
				if !diagnostic.Compatible {
					return xerror.Wrapf("step[%d] %q input contract for %s contradicts known input shape: %s: %w", i, step.Slug, inputName, pipelinevalidate.EdgeCompatibilityDetail(&diagnostic), pipelinevalidate.ErrContractMismatch)
				}
				// Cached table fields are partial query evidence; aliases retain local runtime obligations.
				localTypes := outputContractToFieldTypes(requirement)
				maps.Copy(localTypes, inputSchema.inputFieldTypes)
				for _, field := range requirement.Fields {
					if localTypes[field.Name] == model.ContractFieldTypeUnknown {
						localTypes[field.Name] = field.Type
					}
				}
				inputSchema.inputFieldTypes = localTypes
				ok = true
			}
			if !ok ||
				(inputSchema.dataSourceType == model.DataSourceTypeUnknown && len(inputSchema.inputFieldTypes) == 0) {
				return xerror.Unexpected().Detailf(
					"step[%d] %q: no upstream query schema available for transform validation",
					i,
					step.Slug,
				)
			}
			if upstreamSchema.dataSourceType == model.DataSourceTypeUnknown {
				upstreamSchema.dataSourceType = inputSchema.dataSourceType
			}
			for fieldName, fieldType := range inputSchema.inputFieldTypes {
				if len(upstreamStepSlugs) == 1 {
					upstreamSchema.inputFieldTypes[fieldName] = fieldType
				}
				upstreamSchema.inputFieldTypes[inputName+"."+fieldName] = fieldType
			}
		}

		if err := transformsql.ValidateTransformInputContracts(step.Transform.SQL, inputContracts); err != nil {
			return xerror.Wrapf("step[%d] %q: %w", i, step.Slug, err)
		}
		err = transformsql.ValidateOutputContractTypesWithInputSchemaForOutputKind(
			step.Transform.SQL,
			step.Transform.OutputKind,
			step.Transform.OutputContract,
			upstreamSchema.dataSourceType,
			upstreamSchema.inputFieldTypes,
		)
		if err != nil {
			return xerror.Wrapf("step[%d] %q: %w", i, step.Slug, err)
		}
		outputContract := step.Transform.OutputContract
		if deriveTransformOutputForStep[step.Slug] &&
			(outputContract == nil || len(outputContract.Fields) == 0) {
			outputContract, err = transformsql.InferOutputContractWithInputSchema(
				step.Transform.SQL,
				step.Transform.OutputKind,
				upstreamSchema.dataSourceType,
				upstreamSchema.inputFieldTypes,
			)
			if err != nil {
				return xerror.Wrapf("step[%d] %q infer output contract: %w", i, step.Slug, err)
			}
		}

		if !composition {
			step.Transform.OutputContract = outputContract
		}
		stepSchemas[step.Slug] = stepSchema{
			dataSourceType: upstreamSchema.dataSourceType,
			contract:       outputContract,
			inputFieldTypes: nextTransformInputFieldTypes(
				outputContract,
				upstreamSchema.inputFieldTypes,
			),
		}
	}
	return nil
}

// transformOutputDerivationRequirements marks transform producers whose output
// shape is needed by a downstream edge or transform prevalidation.
func transformOutputDerivationRequirements(normalized *pipelinegraph.Graph) map[slug.Slug]bool {
	requirements := make(map[slug.Slug]bool)
	for _, edge := range normalized.StepInputEdges() {
		toStep, ok := normalized.Step(edge.ToStepSlug)
		if !ok {
			continue
		}
		if toStep.Transform != nil || pipelinevalidate.StepInputContract(toStep) != nil {
			requirements[edge.FromStepSlug] = true
		}
	}
	return requirements
}

func transformSchemaFieldTypes(
	schemaResult datasourcessvc.SchemaResult,
) (map[string]string, model.DataSourceType, error) {
	if schemaResult == nil {
		return nil, model.DataSourceTypeUnknown, xerror.UnexpectedRequired(xerror.SchemaResult)
	}

	dataSourceType, err := model.DataSourceTypeString(schemaResult.VendorType())
	if err != nil {
		return nil, model.DataSourceTypeUnknown, xerror.Unexpected().With(err).Detail(fmt.Sprintf("unsupported schema vendor type %q", schemaResult.VendorType()))
	}

	nativeFieldTypes := make(map[string]string)

	switch s := schemaResult.(type) {
	case datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]:
		for _, table := range s.Payload.Tables {
			for _, column := range table.Columns {
				if _, exists := nativeFieldTypes[column.Name]; !exists {
					nativeFieldTypes[column.Name] = column.Type
				}
			}
		}
	case datasourcessvc.Schema[datasourcessvc.DocumentSchemaPayload]:
		for _, collection := range s.Payload.Collections {
			for _, field := range collection.Fields {
				if _, exists := nativeFieldTypes[field.Name]; !exists {
					nativeFieldTypes[field.Name] = field.Type
				}
			}
		}
	case datasourcessvc.Schema[datasourcessvc.SpreadsheetSchemaPayload]:
	// Grid metadata does not declare cell types; explicit output contracts provide typing.
	case datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]:
		// HTTP schema payload currently does not expose field-level response typing.
		// Keep map empty; validator will fail closed for uncertain projections.
	case datasourcessvc.Schema[datasourcessvc.ObjectAPISchemaPayload]:
		for _, object := range s.Payload.Objects {
			for _, field := range object.Fields {
				if _, exists := nativeFieldTypes[field.Name]; !exists {
					nativeFieldTypes[field.Name] = field.Type
				}
			}
		}
	case datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]:
		for _, file := range s.Payload.Files {
			for _, field := range file.Fields {
				if _, exists := nativeFieldTypes[field.Name]; !exists {
					nativeFieldTypes[field.Name] = field.Type
				}
			}
		}
	default:
		return nil, model.DataSourceTypeUnknown, xerror.Unexpected().Detailf(
			"unsupported schema payload type %T",
			schemaResult,
		)
	}

	return nativeFieldTypes, dataSourceType, nil
}

func nextTransformInputFieldTypes(
	contract *payload.Contract,
	fallback map[string]model.ContractFieldType,
) map[string]model.ContractFieldType {
	if contract == nil || len(contract.Fields) == 0 {
		return fallback
	}

	fieldTypes := make(map[string]model.ContractFieldType, len(contract.Fields))
	for _, field := range contract.Fields {
		fieldTypes[field.Name] = field.Type
	}

	return fieldTypes
}

func outputContractFromNativeFieldTypes(
	dataSourceType model.DataSourceType,
	nativeFieldTypes map[string]string,
) *payload.Contract {
	if len(nativeFieldTypes) == 0 {
		return nil
	}

	fieldNames := make([]string, 0, len(nativeFieldTypes))
	for fieldName := range nativeFieldTypes {
		fieldNames = append(fieldNames, fieldName)
	}
	sort.Strings(fieldNames)

	fields := make([]payload.ContractField, 0, len(fieldNames))
	for _, fieldName := range fieldNames {
		fields = append(fields, transformsql.ContractFieldFromNative(dataSourceType, fieldName, nativeFieldTypes[fieldName]))
	}

	return &payload.Contract{Fields: fields}
}

func outputContractToFieldTypes(
	contract *payload.Contract,
) map[string]model.ContractFieldType {
	if contract == nil {
		return map[string]model.ContractFieldType{}
	}

	fieldTypes := make(map[string]model.ContractFieldType, len(contract.Fields))
	for _, field := range contract.Fields {
		fieldTypes[field.Name] = field.Type
	}

	return fieldTypes
}

func validateQueryOutputContractMatchesSchema(
	explicit *payload.Contract,
	derived *payload.Contract,
) error {
	explicitTypes := outputContractToFieldTypes(explicit)
	derivedTypes := outputContractToFieldTypes(derived)

	for fieldName, explicitType := range explicitTypes {
		derivedType, ok := derivedTypes[fieldName]
		if !ok {
			continue
		}
		if derivedType == model.ContractFieldTypeUnknown {
			continue
		}
		if derivedType != explicitType {
			return xerror.Wrapf(
				"field %q type differs (contract=%s, schema=%s): %w",
				fieldName,
				explicitType,
				derivedType,
				ErrQueryOutputContractSchemaMismatch,
			)
		}
	}

	return nil
}

// RunRequest contains inputs that affect run metadata generation.
type RunRequest struct {
	executionContext           *pipelineruntimevars.ExecutionContext
	DeploymentID               *int64
	DeploymentRevision         *int64
	DeploymentPlan             []byte
	DeploymentResolutionDigest string
	CompiledPlan               []byte
	CompiledPlanDigest         string
	OrgSlug                    slug.Slug
	ProjectSlug                slug.Slug
	EnvironmentSlug            slug.Slug
	RunSlug                    string
	Source                     pipelinerun.RunSourceSummary
	Pipeline                   *pipelinespec.Pipeline
	PipelineVersionID          int64
	RuntimeInput               map[string]any
	EventInput                 *pipelinerun.EventInput
	ReplayOfRunSlug            *string
	ReplayFromStep             *string
	Incremental                pipelinerun.IncrementalRunOptions
	pinnedResults              map[slug.Slug]*pipelinerun.StepResult
}

func prepareEventInput(
	pipeline *pipelinespec.Pipeline,
	input *pipelinerun.EventInput,
) (*pipelinerun.StepResult, *pipelinerun.EventInputSummary, error) {
	var inputStep *pipelinespec.Step
	if pipeline != nil {
		for i := range pipeline.Steps {
			if pipeline.Steps[i].Input != nil {
				inputStep = &pipeline.Steps[i]
				break
			}
		}
	}

	switch {
	case inputStep == nil && input == nil:
		return nil, nil, nil
	case inputStep == nil:
		return nil, nil, xerror.InvalidInput(xerror.Event).Detail(
			"pipeline does not declare an input step",
		)
	case input == nil:
		return nil, nil, xerror.InvalidRequired(xerror.Event).Detailf(
			"pipeline input %q is required",
			inputStep.Slug,
		)
	case input.Key() != inputStep.Slug:
		return nil, nil, xerror.InvalidInput(xerror.Event).Detailf(
			"event input key %q does not match pipeline input %q",
			input.Key(),
			inputStep.Slug,
		)
	}

	runtimePayload := input.Payload()
	runtimeContract := payload.EffectiveContract(inputStep.Slug.String()+"-runtime", runtimePayload, nil)
	// JSON null has no inferred type; nullable declarations still validate the
	// value below instead of failing a schema comparison on an unknown type.
	if declared := inputStep.Input.OutputContract; declared != nil {
		for i, actual := range runtimeContract.Fields {
			if actual.Type != model.ContractFieldTypeUnknown || !actual.Nullable {
				continue
			}
			for _, expected := range declared.Fields {
				if actual.Name == expected.Name && expected.Nullable {
					runtimeContract.Fields[i].Type = expected.Type
					runtimeContract.Fields[i].Precision, runtimeContract.Fields[i].Scale = expected.Precision, expected.Scale
				}
			}
		}
	}
	compatibility := pipelinevalidate.EvaluateEdgeCompatibility(
		pipelinevalidate.EdgeLink{FromStep: inputStep.Slug, ToStep: inputStep.Slug},
		runtimeContract,
		inputStep.Input.OutputContract,
	)
	if !compatibility.Compatible {
		return nil, nil, xerror.InvalidInput(xerror.Event).Detailf(
			"event input %q is incompatible with its output_contract (%s): %s",
			inputStep.Slug,
			compatibility.ReasonCode,
			pipelinevalidate.EdgeCompatibilityDetail(&compatibility),
		)
	}

	inputResult := pipelinerun.NewRunInputStepResult(runtimePayload)
	if err := pipelinerun.ValidateSinkInputContract(inputResult, inputStep.Input.OutputContract); err != nil {
		return nil, nil, xerror.InvalidInput(xerror.Event).Detailf(
			"event input %q is incompatible with its output_contract: %w",
			inputStep.Slug,
			err,
		)
	}
	return inputResult, input.Summary(), nil
}

// RunOutcome is the service-facing result of a pipeline execution.
// It intentionally omits step results so service orchestration does not depend
// on runtime internals after submitting the run request.
type RunOutcome struct {
	Metadata               RunOutcomeMetadata
	Timeline               pipelinerun.RunTimeline
	IncrementalDiagnostics pipelinerun.IncrementalRunDiagnostics
}

// RunOutcomeMetadata contains run summary fields derived by the runtime.
type RunOutcomeMetadata struct {
	PipelineVersionID int64
	PipelineSlug      slug.Slug
	ProjectSlug       slug.Slug
	EnvironmentSlug   slug.Slug
	RunSlug           string
	Source            pipelinerun.RunSourceSummary
	Status            string
	DurationMs        int64
	StepCount         int
	StepSlug          string
	ErrorSummary      *string
	FailureKind       *string
	StartedAt         time.Time
	EndedAt           *time.Time
	ReplayOfRunSlug   *string
	ReplayFromStep    *string
	Incremental       pipelinerun.IncrementalRunSummary
}

// ReplayRequest describes how to replay a previous run from a checkpoint.
type ReplayRequest struct {
	Source            pipelinerun.RunSourceSummary
	OrgSlug           slug.Slug
	ProjectSlug       slug.Slug
	EnvironmentSlug   slug.Slug
	SourceRunSlug     string
	ReplayRunSlug     string
	FromStep          string
	InputMode         ReplayInputMode
	PipelineVersionID int64
}

// TimelineByRunSlug returns a previously recorded run timeline by run slug.
func (r *Runner) TimelineByRunSlug(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelinerun.RunTimeline, error) {
	if r.store == nil {
		return nil, ErrNilRunStore
	}
	if orgSlug == "" {
		return nil, ErrMissingOrgSlug
	}

	return r.store.Get(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
}

// Execute runs a pipeline and returns the service-facing execution outcome.
func (r *Runner) Execute(ctx context.Context, req RunRequest) (*RunOutcome, error) {
	runResult, runErr := r.Run(ctx, req)
	if runErr != nil && runResult == nil {
		return nil, runErr
	}

	outcome, err := newRunOutcome(req.Pipeline, runResult)
	if err != nil {
		if runErr != nil {
			return nil, xerror.Join(err, runErr)
		}
		return nil, err
	}

	return outcome, runErr
}

func newRunOutcome(pipeline *pipelinespec.Pipeline, runResult *pipelinerun.RunResult) (*RunOutcome, error) {
	if runResult == nil {
		return nil, xerror.Unexpected().Detail("run result is required")
	}

	var errorSummary *string
	var failureKind *string
	if runResult.Timeline.Failure != nil {
		if runResult.Timeline.Failure.Error != "" {
			s := runResult.Timeline.Failure.Error
			errorSummary = &s
		}
		if runResult.Timeline.Failure.Kind != "" {
			k := runResult.Timeline.Failure.Kind
			failureKind = &k
		}
	}

	startedAt := time.Time{}
	if len(runResult.Timeline.Events) > 0 {
		startedAt = runResult.Timeline.Events[0].At
	}
	for _, event := range runResult.Timeline.Events {
		if event.Type == "run_started" {
			startedAt = event.At
			break
		}
	}

	var endedAt *time.Time
	if n := len(runResult.Timeline.Events); n > 0 {
		lastAt := runResult.Timeline.Events[n-1].At
		endedAt = &lastAt
	}

	return &RunOutcome{
		Metadata: RunOutcomeMetadata{
			PipelineVersionID: runResult.Metadata.PipelineVersionID,
			PipelineSlug:      runResult.Timeline.PipelineSlug,
			ProjectSlug:       runResult.Metadata.ProjectSlug,
			EnvironmentSlug:   runResult.Metadata.EnvironmentSlug,
			RunSlug:           runResult.Metadata.RunSlug,
			Source:            runResult.Metadata.Source,
			Status:            runResult.Metadata.Status,
			DurationMs:        runResult.Metadata.DurationMs,
			StepCount:         runResult.Metadata.StepCount,
			StepSlug:          lastExecutedStepSlug(pipeline, runResult),
			ErrorSummary:      errorSummary,
			FailureKind:       failureKind,
			StartedAt:         startedAt,
			EndedAt:           endedAt,
			ReplayOfRunSlug:   runResult.Metadata.ReplayOfRunSlug,
			ReplayFromStep:    runResult.Metadata.ReplayFromStep,
			Incremental:       runResult.Metadata.Incremental,
		},
		Timeline:               runResult.Timeline,
		IncrementalDiagnostics: runResult.IncrementalDiagnostics,
	}, nil
}

func lastExecutedStepSlug(pipeline *pipelinespec.Pipeline, runResult *pipelinerun.RunResult) string {
	if runResult.Metadata.FailedStep != nil {
		return string(*runResult.Metadata.FailedStep)
	}
	for i := len(runResult.Timeline.Events) - 1; i >= 0; i-- {
		event := runResult.Timeline.Events[i]
		switch event.Type {
		case "step_started", "step_succeeded", "step_failed", "step_reused":
			return event.Step.String()
		}
	}
	if runResult.Metadata.Status == "running" || runResult.Metadata.Status == "queued" {
		return ""
	}
	if pipeline != nil && len(pipeline.Steps) > 0 {
		return string(pipeline.Steps[len(pipeline.Steps)-1].Slug)
	}
	if n := len(runResult.Timeline.Snapshots); n > 0 {
		return runResult.Timeline.Snapshots[n-1].Step.String()
	}
	return ""
}

// Replay reruns a pipeline from a previously saved checkpoint with deterministic semantics.
func (r *Runner) Replay(
	ctx context.Context,
	req ReplayRequest,
) (runResult *pipelinerun.RunResult, runErr error) {
	if req.SourceRunSlug == "" {
		return nil, ErrReplayRunSlugRequired
	}
	if req.OrgSlug == "" {
		return nil, ErrMissingOrgSlug
	}

	if req.InputMode != ReplayInputModePinned {
		return nil, ErrReplayUnsupportedMode.Detailf("%s", req.InputMode)
	}

	if r.store == nil {
		return nil, ErrReplayRunNotFound
	}

	checkpoint, err := r.store.GetCheckpoint(
		ctx,
		req.OrgSlug,
		req.ProjectSlug,
		req.EnvironmentSlug,
		req.SourceRunSlug,
	)
	if err != nil {
		if xerror.Is(err, pipelinerun.ErrReplayCheckpointNotFound) {
			return nil, ErrReplayRunNotFound.With(err).WithPublicDetail("Retained repair inputs are unavailable or expired. Reconcile provider effects before starting a new evaluation, which reads sources again.")
		}
		return nil, xerror.Wrapf("get replay checkpoint: %w", err)
	}
	if checkpoint == nil || checkpoint.Pipeline == nil || len(checkpoint.Results) == 0 {
		return nil, ErrReplayRunNotFound.WithPublicDetail("Retained repair inputs are unavailable or incomplete. Reconcile provider effects before starting a new evaluation, which reads sources again.")
	}

	if req.FromStep != "" && req.FromStep != string(checkpoint.StepSlug) {
		return nil, ErrReplayStepNotFound.Detailf("%s", req.FromStep)
	}
	if err := r.validateReplaySafety(ctx, req, checkpoint.Pipeline); err != nil {
		return nil, err
	}

	replayRunSlug, err := replayRunSlug(req.ReplayRunSlug, req.SourceRunSlug, r.clock.Now())
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	for _, step := range checkpoint.Pipeline.Steps {
		if step.Sink != nil {
			continue
		}
		retained := checkpoint.Results[step.Slug]
		if retained == nil {
			return nil, ErrReplayUnsupportedMode.Detail("sink recovery requires retained outputs for every source and transform").WithPublicDetail("Recovery requires complete retained outputs for every source and transform. These inputs are incomplete; reconcile provider effects before a new evaluation, which reads sources again.")
		}
		storedPayload, ok := retained.Payload()
		if ok && storedPayload.Representation == model.ContractRepresentationFile && (storedPayload.File == nil || int64(len(storedPayload.FileBytes)) != storedPayload.File.SizeBytes) {
			return nil, ErrReplayUnsupportedMode.WithPublicDetail("Retained file bytes are unavailable or incomplete. File metadata does not provide repair inputs; reconcile provider effects before starting a new evaluation, which reads sources again.")
		}
	}
	if req.EnvironmentSlug == "" {
		req.EnvironmentSlug = checkpoint.EnvironmentSlug
	}
	if req.ProjectSlug == "" {
		req.ProjectSlug = checkpoint.ProjectSlug
	}
	timeline, err := r.store.Get(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, req.SourceRunSlug)
	if err != nil {
		return nil, xerror.Wrapf("get pinned recovery timeline: %w", err)
	}
	if timeline != nil && timeline.DeploymentResolutionDigest != "" && timeline.DeploymentResolutionDigest != checkpoint.DeploymentResolutionDigest {
		return nil, deploymentplan.ErrInvalid.WithPublicDetail("The retained checkpoint does not match the original deployment resolution. Recovery cannot use current bindings as a substitute.")
	}
	if timeline == nil || timeline.ExecutionContext == nil {
		return nil, ErrReplayUnsupportedMode.Detail("recovery requires retained execution context; legacy runs cannot recalculate it")
	}
	if timeline.Failure != nil && timeline.Failure.ReasonCode == pipelinerun.FailureReasonCodeWorkerOwnershipLost {
		recoveryContext := *timeline.ExecutionContext
		recoveryContext.RunSlug = replayRunSlug
		stableExecutionValue := func(value any) error {
			original, err := pipelineruntimevars.ResolveExecutionValue(value, *timeline.ExecutionContext)
			if err != nil {
				return err
			}
			recovered, err := pipelineruntimevars.ResolveExecutionValue(value, recoveryContext)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(original, recovered) {
				return ErrReplayUnsafe.WithPublicDetail("Worker-loss recovery cannot change sink targets or inputs derived from runtime.run.slug. Use runtime.sink.replay_identity for repeat-safe effect identity.")
			}
			return nil
		}
		for _, step := range checkpoint.Pipeline.Steps {
			if step.Sink == nil {
				continue
			}
			if !sinkIdempotencyClassForStep(step).IsReplaySafe() {
				return nil, ErrReplayUnsafe.WithPublicDetail("Worker-loss recovery requires a repeat-safe strategy for every sink. Reconcile unknown or unsafe provider effects first.")
			}
			if sinkBackendForStep(step) == model.SinkBackendS3 && sinkIdempotencyClassForStep(step) == pipelinespec.SinkIdempotencyClassNaturallyIdempotent && step.Sink.Config["overwrite"] != true {
				return nil, ErrReplayUnsafe.WithPublicDetail("Worker-loss recovery of a naturally idempotent S3 sink requires deterministic overwrite with overwrite: true.")
			}
			for _, value := range []any{step.Sink.Config, step.Sink.OperationInline} {
				if err := stableExecutionValue(value); err != nil {
					return nil, err
				}
			}
		}
		if len(checkpoint.CompiledPlan) > 0 {
			plan, err := contractplan.Read(checkpoint.CompiledPlan, checkpoint.CompiledPlanDigest)
			if err != nil {
				return nil, err
			}
			for _, operation := range plan.Operations {
				if operation.Role != "sink" {
					continue
				}
				var value any
				if err := json.Unmarshal(operation.Content, &value); err != nil {
					return nil, contractplan.ErrInvalid.With(err)
				}
				if err := stableExecutionValue(value); err != nil {
					return nil, err
				}
			}
		}
	}
	effectIdentity := sinkReplayIdentity(req.SourceRunSlug, timeline.ReplayOfRunSlug, pipelinerun.RunSourceSummary{})
	if timeline.SinkReplayIdentity != "" {
		effectIdentity = timeline.SinkReplayIdentity
	}
	replayFromStep := checkpoint.StepSlug.String()
	replayPipeline := checkpoint.Pipeline
	if len(checkpoint.CompiledPlan) > 0 {
		replayPipeline = nil
	}
	return r.Run(ctx, RunRequest{
		OrgSlug: req.OrgSlug, ProjectSlug: req.ProjectSlug, EnvironmentSlug: req.EnvironmentSlug,
		RunSlug: replayRunSlug, Source: req.Source, Pipeline: replayPipeline,
		CompiledPlan: checkpoint.CompiledPlan, CompiledPlanDigest: checkpoint.CompiledPlanDigest,
		DeploymentPlan: checkpoint.DeploymentPlan, DeploymentResolutionDigest: checkpoint.DeploymentResolutionDigest,
		PipelineVersionID: req.PipelineVersionID, ReplayOfRunSlug: &effectIdentity,
		ReplayFromStep: &replayFromStep, executionContext: timeline.ExecutionContext, pinnedResults: cloneStepResults(checkpoint.Results),
		Incremental: pipelinerun.IncrementalRunOptions{
			SourceWindows: cloneIncrementalSourceWindows(checkpoint.IncrementalSourceWindows),
			// Recovery never advances a source checkpoint from retained payloads.
			SkipCheckpointCommit: true,
		},
	})
}

// Recover returns the service-facing outcome of a pinned sink recovery.
func (r *Runner) Recover(ctx context.Context, req ReplayRequest) (*RunOutcome, error) {
	result, runErr := r.Replay(ctx, req)
	if result == nil {
		return nil, runErr
	}
	returnOutcome, err := newRunOutcome(nil, result)
	if err != nil {
		return nil, xerror.Join(err, runErr)
	}
	if len(result.Timeline.Snapshots) > 0 {
		returnOutcome.Metadata.StepSlug = result.Timeline.Snapshots[len(result.Timeline.Snapshots)-1].Step.String()
	}
	return returnOutcome, runErr
}

func (r *Runner) Run(
	ctx context.Context,
	req RunRequest,
) (runResult *pipelinerun.RunResult, runErr error) {
	secretRedactor := runtimeSecretRedactor{}
	defer func() {
		if runResult != nil {
			redactRunResultSecrets(runResult, secretRedactor)
		}
		runErr = secretRedactor.RedactError(runErr)
	}()

	operations := map[slug.Slug]contractplan.Operation{}
	var deployment *deploymentplan.Plan
	if len(req.DeploymentPlan) > 0 || req.DeploymentResolutionDigest != "" {
		var err error
		deployment, err = deploymentplan.Read(req.DeploymentPlan, req.DeploymentResolutionDigest)
		if err != nil || !deployment.Matches(req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, deployment.Pipeline.Metadata.Slug) {
			return nil, deploymentplan.ErrInvalid.With(err)
		}
		req.Pipeline = nil
		req.CompiledPlan, req.CompiledPlanDigest = deployment.CompiledPlan, deployment.CompiledDigest
		ctx = deploymentplan.WithDispatch(ctx, req.OrgSlug, deployment.Targets)
	}
	if len(req.CompiledPlan) != 0 || req.CompiledPlanDigest != "" {
		plan, err := contractplan.Read(req.CompiledPlan, req.CompiledPlanDigest)
		if err != nil {
			return nil, err
		}
		if req.Pipeline != nil {
			raw, err := json.Marshal(req.Pipeline)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != plan.AuthoredSpecHash {
				return nil, contractplan.ErrInvalid.With(err)
			}
		}
		pipeline, err := plan.RuntimePipeline()
		if err != nil {
			return nil, err
		}
		req.Pipeline = pipeline
		for _, op := range plan.Operations {
			operations[op.Step] = op
		}
	}
	if deployment != nil {
		req.Pipeline = deployment.Pipeline
		for step, op := range operations {
			for _, target := range deployment.Targets {
				if target.Step != step || target.Role != op.Role {
					continue
				}
				var content contractplan.OperationContent
				if err := json.Unmarshal(op.Content, &content); err != nil {
					return nil, deploymentplan.ErrInvalid.With(err)
				}
				if content.ResourceScope != nil {
					content.ResourceScope.DataSource = target.DataSource
					encoded, err := json.Marshal(content)
					if err != nil {
						return nil, deploymentplan.ErrInvalid.With(err)
					}
					op.Content = encoded
				}
				op.DataSource = target.DataSource
				operations[step] = op
			}
		}
	}
	if req.Pipeline == nil {
		return nil, ErrNilPipeline
	}
	if r.executor == nil {
		return nil, ErrQueryExecutorNotProvided
	}
	if r.sinkRuntime == nil {
		return nil, ErrNilSinkRuntime
	}
	if req.OrgSlug == "" {
		return nil, ErrMissingOrgSlug
	}
	// Environment is the runtime scope for deployment, bindings, run history,
	// and diagnostics even when a pipeline spec does not actively use bindings.
	if req.EnvironmentSlug == "" {
		return nil, ErrMissingEnvironmentSlug
	}
	checkpointPipeline, err := clonePipeline(req.Pipeline)
	if err != nil {
		return nil, xerror.Wrapf("clone checkpoint pipeline: %w", err)
	}
	originalPipeline, err := clonePipeline(req.Pipeline)
	if err != nil {
		return nil, xerror.Wrapf("clone original pipeline: %w", err)
	}
	workingPipeline, err := clonePipeline(req.Pipeline)
	if err != nil {
		return nil, xerror.Wrapf("clone working pipeline: %w", err)
	}
	inputPipeline := workingPipeline
	if req.pinnedResults != nil {
		inputCopy := *workingPipeline
		inputCopy.Steps = append([]pipelinespec.Step(nil), workingPipeline.Steps...)
		for i, step := range inputCopy.Steps {
			if step.Sink == nil && req.pinnedResults[step.Slug] != nil {
				// Retained outputs replace source execution; its request-local parameters are no longer needed.
				inputCopy.Steps[i].Query = nil
			}
		}
		inputPipeline = &inputCopy
	}
	runSlugValue, err := runSlug(req.RunSlug, req.Pipeline.Metadata.Slug, r.clock.Now())
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	req.Source = pipelinerun.NormalizeAttemptSource(req.Source, slug.FromString(runSlugValue))
	runStartedAt := r.clock.Now()
	ctx, finishFiles := filestream.BeginAttempt(ctx, time.Now())
	defer finishFiles()

	snapshot, err := r.executionSnapshot(ctx, req, runStartedAt)
	if err != nil {
		return nil, err
	}
	// Time and history stay pinned; attempt identity belongs to this execution.
	attemptSnapshot := *snapshot
	attemptSnapshot.RunSlug = runSlugValue
	snapshot = &attemptSnapshot
	req.executionContext = snapshot
	ctx = pipelineruntimevars.WithExecutionContext(ctx, *snapshot)
	if err := pipelineruntimevars.ResolvePipelineExecution(inputPipeline, *snapshot); err != nil {
		return nil, err
	}
	if err := pipelineruntimevars.ResolvePipelineInput(inputPipeline, req.RuntimeInput); err != nil {
		return nil, xerror.Wrapf("resolve runtime input: %w", err)
	}
	resolvedSecretRedactor, err := r.resolveEnvironmentSecrets(ctx, req.OrgSlug, req.EnvironmentSlug, workingPipeline)
	if err != nil {
		return nil, xerror.Wrapf("resolve environment secrets: %w", err)
	}
	secretRedactor = resolvedSecretRedactor
	if err := pipelinevalidate.Validate(workingPipeline); err != nil {
		return nil, xerror.Wrapf("invalid pipeline: %w", err)
	}
	literalBindings := pipelineruntimevars.FreezeLiteralOperationFields(workingPipeline)
	var runInputResult *pipelinerun.StepResult
	var runInputSummary *pipelinerun.EventInputSummary
	normalizedGraph, err := pipelinegraph.Normalize(workingPipeline)
	if err != nil {
		return nil, xerror.Wrapf("normalize pipeline graph: %w", err)
	}
	originalGraph, err := pipelinegraph.Normalize(originalPipeline)
	if err != nil {
		return nil, xerror.Wrapf("normalize original pipeline graph: %w", err)
	}
	req.Pipeline = workingPipeline

	workerObservedAt := runStartedAt.UTC()
	timeline := pipelinerun.RunTimeline{
		SinkReplayIdentity: sinkReplayIdentity(runSlugValue, req.ReplayOfRunSlug, req.Source),
		CompiledPlanDigest: req.CompiledPlanDigest, WorkerObservedAt: &workerObservedAt,
		ExecutionContext:           snapshot,
		DeploymentResolutionDigest: req.DeploymentResolutionDigest,
		RunSlug:                    runSlugValue,
		PipelineSlug:               req.Pipeline.Metadata.Slug,
		ProjectSlug:                req.ProjectSlug,
		EnvironmentSlug:            req.EnvironmentSlug,
		Status:                     "running",
		ReplayOfRunSlug:            req.ReplayOfRunSlug,
		Events: []pipelinerun.TimelineEvent{
			{At: runStartedAt, Type: "run_started", Status: "running"},
		},
	}

	journal, err := newCheckJournal(r, req, originalPipeline, operations, &timeline, runStartedAt, secretRedactor)
	if err != nil {
		return nil, err
	}
	{
		previous, readErr := r.store.Get(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, runSlugValue)
		if readErr != nil && !xerror.Is(readErr, pipelinerun.ErrTimelineNotFound) {
			return nil, readErr
		}
		if previous != nil && (previous.Revision > 0 || len(previous.StructuralChecks) > 0) {
			// A worker delivery retry is the same attempt, not permission to rerun checks or effects.
			timeline = *previous
			if previous.Status == "running" || previous.Status == "queued" {
				pipelinerun.FinalizeStructuralChecks(timeline.StructuralChecks, r.clock.Now())
				timeline.Status = "failed"
				if timeline.Failure == nil {
					timeline.Failure = &pipelinerun.FailureContext{At: r.clock.Now(), Kind: failureKindPlatform, ReasonCode: pipelinerun.FailureReasonCodeWorkerOwnershipLost, Error: errAttemptInterrupted.Error(), OutcomeUncertain: true, AutomaticRetryProhibited: true}
				}
				timeline.Failure.OutcomeUncertain = true
				if n := len(timeline.Events); n > 0 && timeline.Events[n-1].Type == "run_completed" {
					timeline.Events[n-1].Status = "failed"
				} else {
					timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{At: r.clock.Now(), Type: "run_completed", Status: "failed"})
				}
				if err := journal.persist(ctx); err != nil {
					return nil, err
				}
			}
			return nil, errAttemptInterrupted
		}
		if previous != nil {
			for _, event := range previous.Events {
				if event.Type == "run_started" {
					timeline.Events = nil
					break
				}
			}
			timeline.Events = append(previous.Events, timeline.Events...)
		}
	}
	if err := journal.persist(ctx); err != nil {
		return nil, err
	}
	// Every admitted attempt must finish, even without planned structural checks.
	finalized := false
	defer func() {
		if runResult != nil || finalized {
			return
		}
		pipelinerun.FinalizeStructuralChecks(timeline.StructuralChecks, r.clock.Now())
		timeline.Status = "failed"
		if timeline.Failure == nil {
			timeline.Failure = &pipelinerun.FailureContext{At: r.clock.Now(), Kind: failureKindPlatform, Error: "run execution did not complete"}
		}
		// An interrupted sink may have reached the provider before its evidence was committed.
		for _, event := range timeline.Events {
			if event.Type != "step_started" {
				continue
			}
			for _, step := range originalPipeline.Steps {
				if step.Slug == event.Step && step.Sink != nil {
					timeline.Failure.OutcomeUncertain = true
				}
			}
		}
		timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{At: r.clock.Now(), Type: "run_completed", Status: "failed"})
		timeline.Failure.AutomaticRetryProhibited = timeline.Failure.AutomaticRetryProhibited || xerror.Is(runErr, xerror.ExecutionRowLimitExceeded()) || xerror.Is(runErr, xerror.ExecutionResponseSizeExceeded()) || xerror.Is(runErr, xerror.HTTPAcknowledgementRejected()) || xerror.Is(runErr, xerror.HTTPAcknowledgementUncertain())
		finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if persistErr := journal.persist(finalizeCtx); persistErr != nil {
			runErr = xerror.Join(runErr, persistErr)
		}
	}()
	observationRequest := req
	observationRequest.RunSlug = runSlugValue
	ctx, stopObservations := r.observeWorker(ctx, observationRequest, workerObservedAt, pipelinerun.WorkerObservationInterval)
	defer func() {
		if err := stopObservations(); err != nil && runResult == nil {
			runErr = xerror.Join(runErr, err)
		}
	}()
	eventStep := slug.Slug("")
	for _, step := range req.Pipeline.Steps {
		if step.Input != nil {
			eventStep = step.Slug
			break
		}
	}
	var eventSource *pipelinerun.StepResult
	if req.EventInput != nil {
		eventSource = pipelinerun.NewRunInputStepResult(req.EventInput.Payload())
	}
	err = journal.evaluate(ctx, eventStep, "step", "event_admission", "", eventSource, func() error {
		var admissionErr error
		runInputResult, runInputSummary, admissionErr = prepareEventInput(workingPipeline, req.EventInput)
		return admissionErr
	})
	if err != nil {
		admissionRejected := xerror.Is(err, xerror.InvalidInput(xerror.Event))
		var diagnostic *pipelinerun.ContractFailureDiagnostic
		for _, check := range timeline.StructuralChecks {
			if check.Step != eventStep || check.Phase != "event_admission" {
				continue
			}
			switch check.Status {
			case pipelinerun.CheckStatusError:
				admissionRejected = false
			case pipelinerun.CheckStatusFailed:
				admissionRejected = true
			case pipelinerun.CheckStatusUnavailable, pipelinerun.CheckStatusPassed, pipelinerun.CheckStatusSkipped:
			}
			diagnostic = check.ContractFailure
			break
		}
		if admissionRejected {
			timeline.Failure = &pipelinerun.FailureContext{
				Step: eventStep, At: r.clock.Now(), Kind: failureKindPlatform, Side: failureSideSource,
				ReasonCode: pipelinerun.FailureReasonCodeEventInputRejected,
				Error:      "Event input was rejected before step execution.",
				ActionHint: "Review the input step's contract and event admission checks. Correct the event structure and value types before submitting a new event.",
				Contract:   diagnostic, AutomaticRetryProhibited: true,
			}
		}
		return nil, err
	}

	metricStartedAt := time.Now()
	defer func() {
		recordPipelineRunMetric(
			incrementalTriggerSource(req.Incremental, "manual"),
			runResult,
			runErr,
			time.Since(metricStartedAt),
		)
	}()

	finishEdgeValidationFailure := func(
		edgeDiagnostics []pipelinevalidate.EdgeCompatibilityResult,
		edgeValidationErr error,
		failure *pipelinerun.FailureContext,
	) (*pipelinerun.RunResult, error) {
		failureAt := r.clock.Now()
		failureStep, failureSide := preflightFailureContext(req.Pipeline, edgeDiagnostics)
		if failure == nil {
			failure = &pipelinerun.FailureContext{
				Step:       failureStep,
				Error:      edgeValidationErr.Error(),
				At:         failureAt,
				Kind:       failureKindPlatform,
				Side:       failureSide,
				ReasonCode: preflightFailureReasonCode(failureSide),
				ActionHint: "The pipeline configuration is invalid. " +
					"Review step connections and data flow before retrying.",
			}
		}
		failure.At = failureAt
		timeline.Failure = failure
		runCompletedAt := failureAt
		overallStatus := statusFor(edgeValidationErr)
		pipelinerun.FinalizeStructuralChecks(timeline.StructuralChecks, runCompletedAt)
		timeline.Status = overallStatus
		timeline.Events = append(timeline.Events,
			pipelinerun.TimelineEvent{At: runCompletedAt, Type: "run_completed", Status: overallStatus},
		)
		finalized = true

		runResult = &pipelinerun.RunResult{
			Metadata: pipelinerun.RunMetadata{
				DeploymentID:               req.DeploymentID,
				DeploymentRevision:         req.DeploymentRevision,
				ProjectSlug:                req.ProjectSlug,
				EnvironmentSlug:            req.EnvironmentSlug,
				RunSlug:                    runSlugValue,
				Source:                     req.Source,
				Status:                     overallStatus,
				DurationMs:                 durationMilliseconds(runStartedAt, runCompletedAt),
				StepCount:                  len(req.Pipeline.Steps),
				PipelineVersionID:          req.PipelineVersionID,
				DeploymentResolutionDigest: req.DeploymentResolutionDigest,
				ReplayOfRunSlug:            req.ReplayOfRunSlug,
				ReplayFromStep:             req.ReplayFromStep,
				Incremental:                fullIncrementalRunSummary(),
			},
			Timeline:               timeline,
			StepResults:            map[slug.Slug]*pipelinerun.StepResult{},
			EdgeDiagnostics:        edgeDiagnostics,
			IncrementalDiagnostics: emptyIncrementalRunDiagnostics(),
		}

		runResult.Timeline.Revision++
		redactRunResultSecrets(runResult, secretRedactor)
		persistErr := r.store.PersistRun(
			ctx,
			req.OrgSlug,
			req.ProjectSlug,
			req.EnvironmentSlug,
			originalPipeline,
			runResult,
		)
		if persistErr != nil {
			if req.pinnedResults != nil {
				runResult = nil
			}
			return runResult, xerror.Join(
				xerror.Wrapf("persist pipeline run: %w", persistErr),
				edgeValidationErr,
			)
		}

		return runResult, edgeValidationErr
	}

	edgeDiagnostics, edgeValidationErr := preflightEdgeDiagnostics(originalPipeline)
	if edgeValidationErr != nil {
		return finishEdgeValidationFailure(edgeDiagnostics, edgeValidationErr, nil)
	}

	if err := r.preValidateTransforms(ctx, req.OrgSlug, req.Pipeline, len(req.CompiledPlan) > 0 || req.Pipeline.UsesContractComposition()); err != nil {
		return finishEdgeValidationFailure(edgeDiagnostics, xerror.Wrapf("pre-validate transforms: %w", err), prevalidationFailure(err))
	}
	diagnosticPipeline, err := clonePipeline(req.Pipeline)
	if err != nil {
		return nil, xerror.Wrapf("clone diagnostic pipeline: %w", err)
	}

	edgeDiagnostics, edgeValidationErr = preflightEdgeDiagnostics(req.Pipeline)
	if edgeValidationErr != nil {
		return finishEdgeValidationFailure(edgeDiagnostics, edgeValidationErr, nil)
	}

	completedResults := make(map[slug.Slug]*pipelinerun.StepResult, len(req.Pipeline.Steps))
	diagnosticResults := make(map[slug.Slug]*pipelinerun.StepResult, len(req.Pipeline.Steps))
	incrementalObservations := make(map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation)
	incrementalLookups := make(map[slug.Slug]pipelinerun.IncrementalCheckpointLookup)
	incrementalInputs := make(map[slug.Slug]*pipelinerun.IncrementalCheckpointInput)
	succeededSinks := make(map[slug.Slug]bool)
	failedSinks := make(map[slug.Slug]error)
	var failedStep *slug.Slug
	var lastCompletedOutputStep slug.Slug
	saveCheckpoint := func(ctx context.Context, beforeSink bool) error {
		// Event payloads are never retained; secret-bearing runs cannot retain exact repair inputs.
		if len(completedResults) == 0 || req.EventInput != nil || (beforeSink && !secretRedactor.Empty()) {
			return nil
		}
		checkpoint := pipelinerun.ReplayCheckpoint{
			RunSlug: timeline.RunSlug, OrgSlug: req.OrgSlug, ProjectSlug: req.ProjectSlug, EnvironmentSlug: req.EnvironmentSlug,
			StepSlug: lastCompletedOutputStep, Pipeline: checkpointPipeline,
			CompiledPlan: req.CompiledPlan, CompiledPlanDigest: req.CompiledPlanDigest,
			DeploymentPlan: req.DeploymentPlan, DeploymentResolutionDigest: req.DeploymentResolutionDigest,
			Results:                  cloneStepResultsForStorage(completedResults, secretRedactor),
			IncrementalSourceWindows: incrementalSourceWindowsFromObservations(incrementalObservations),
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			if beforeSink {
				// Nonserializable legacy outputs are dispatchable but ineligible for early retention.
				return nil
			}
			return xerror.Unexpected().With(err)
		}
		// Bound the complete retained artifact, including plans and all intermediate outputs.
		if beforeSink && len(encoded) > 10*1024*1024 {
			return nil
		}
		return r.store.SaveCheckpoint(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, timeline.RunSlug, checkpoint)
	}
	for stepIndex, step := range normalizedGraph.TopologicalSteps() {
		if stepIndex > 0 {
			if err := journal.persist(ctx); err != nil {
				return nil, err
			}
		}
		ctx := pipelineruntimevars.WithLiteralBindings(deploymentplan.WithStep(ctx, step.Slug), literalBindings[step.Slug])

		if pinned := req.pinnedResults[step.Slug]; pinned != nil && step.Sink == nil {
			for i := range timeline.StructuralChecks {
				check := &timeline.StructuralChecks[i]
				if check.Step == step.Slug {
					check.Status, check.ReasonCode = pipelinerun.CheckStatusSkipped, "retained_output_reused"
					if req.ReplayOfRunSlug != nil {
						check.ReusedFromRun = *req.ReplayOfRunSlug
					}
				}
			}
			if err := journal.persist(ctx); err != nil {
				return nil, err
			}

			completedResults[step.Slug] = pinned
			diagnosticResults[step.Slug] = pinned
			lastCompletedOutputStep = step.Slug
			timeline.Snapshots = append(timeline.Snapshots, replaySnapshotsFromResults(req.Pipeline, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, map[slug.Slug]*pipelinerun.StepResult{step.Slug: pinned})...)
			timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{At: r.clock.Now(), Type: "step_reused", Step: step.Slug, Status: "succeeded"})
			if window, ok := req.Incremental.SourceWindows[step.Slug]; ok {
				operationRef, operationInline, usesInline, err := resolveQueryOperationForStep(step)
				if err != nil {
					return nil, xerror.Wrapf("resolve pinned source operation: %w", err)
				}
				lookup, _, err := incrementalCheckpointLookupForStep(req.Pipeline.Metadata.Slug, req.PipelineVersionID, incrementalCheckpointNamespace(req.Incremental), step, operationRef, operationInline, usesInline)
				if err != nil {
					return nil, xerror.Wrapf("resolve pinned source checkpoint identity: %w", err)
				}
				incrementalLookups[step.Slug] = lookup
				incrementalObservations[step.Slug] = &pipelinerun.IncrementalCheckpointObservation{
					SourceStepSlug: step.Slug, CheckpointNamespace: lookup.CheckpointNamespace,
					CheckpointKind: lookup.CheckpointKind, ObservedAt: r.clock.Now(), ObservedLowerBound: window.LowerBound,
					ObservedUpperBound: window.UpperBound, DisplayMetadata: window.DisplayMetadata,
				}
			}
			continue
		}
		if step.Sink != nil {
			if err := saveCheckpoint(ctx, true); err != nil {
				timeline.Failure = &pipelinerun.FailureContext{
					Step: step.Slug, At: r.clock.Now(), Kind: failureKindPlatform, Side: failureSideSink,
					Error:      "Sink repair inputs could not be retained before dispatch.",
					ActionHint: "Restore checkpoint storage before retrying. This sink was not dispatched; earlier sink delivery evidence still applies.",
				}
				return nil, xerror.Wrapf("retain sink repair inputs before dispatch: %w", err)
			}
		}
		stepStartedAt := r.clock.Now()
		timeline.Events = append(
			timeline.Events,
			pipelinerun.TimelineEvent{
				At:     stepStartedAt,
				Type:   "step_started",
				Step:   step.Slug,
				Status: "running",
			},
		)

		if err := journal.persist(ctx); err != nil {
			return nil, err
		}

		if step.Input != nil {
			completedResults[step.Slug] = runInputResult
			diagnosticResults[step.Slug] = runInputResult
			timeline.Snapshots = append(timeline.Snapshots, pipelinerun.StepSnapshot{
				Step: step.Slug,
				Kind: step.Kind,
				Input: pipelinerun.StepInputSnapshot{
					OrganizationSlug: req.OrgSlug,
					ProjectSlug:      req.ProjectSlug,
					EnvironmentSlug:  req.EnvironmentSlug,
					ParameterKeys:    []string{},
					EventInput:       runInputSummary,
				},
				Output: snapshotOutput(runInputResult),
			})
			timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{
				At:     r.clock.Now(),
				Type:   "step_succeeded",
				Step:   step.Slug,
				Status: "succeeded",
			})
			lastCompletedOutputStep = step.Slug
			continue
		}

		if step.Transform != nil {
			if r.transformRuntime == nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     r.clock.Now(),
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      "transform runtime is required",
					At:         r.clock.Now(),
					Kind:       failureKindPlatform,
					Side:       failureSideTransform,
					ReasonCode: pipelinerun.FailureReasonCodeTransformRuntimeFailed,
					ActionHint: "The pipeline platform is misconfigured. Contact your administrator.",
				}
				failedStep = &fs
				runErr = xerror.Unexpected().Detailf(
					"execute step %q: transform runtime is required",
					step.Slug,
				)
				break
			}

			upstreamStepSlugs, upstreamErr := resolveTransformInputStepSlugsFromGraph(
				normalizedGraph,
				step.Slug,
			)
			if upstreamErr != nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     r.clock.Now(),
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      upstreamErr.Error(),
					At:         r.clock.Now(),
					Kind:       failureKindPlatform,
					Side:       failureSideTransform,
					ReasonCode: pipelinerun.FailureReasonCodeTransformRuntimeFailed,
					ActionHint: "The transform step is missing a required upstream source step. Review the pipeline step order.",
				}
				failedStep = &fs
				runErr = xerror.Wrapf("execute step %q: %w", step.Slug, upstreamErr)
				break
			}

			upstreamPayloads := make(map[string]*payload.Payload, len(upstreamStepSlugs))
			inputNames := make([]string, 0, len(upstreamStepSlugs))
			for inputName := range upstreamStepSlugs {
				inputNames = append(inputNames, inputName)
			}
			sort.Strings(inputNames)
			for _, inputName := range inputNames {
				upstreamStepSlug := upstreamStepSlugs[inputName]
				upstreamStepResult, ok := completedResults[upstreamStepSlug]
				if !ok {
					timeline.Events = append(
						timeline.Events,
						pipelinerun.TimelineEvent{
							At:     r.clock.Now(),
							Type:   "step_failed",
							Step:   step.Slug,
							Status: "failed",
						},
					)
					fs := step.Slug
					timeline.Failure = &pipelinerun.FailureContext{
						Step:       step.Slug,
						Error:      fmt.Sprintf("upstream step result not found: %s", upstreamStepSlug),
						At:         r.clock.Now(),
						Kind:       failureKindPlatform,
						Side:       failureSideTransform,
						ReasonCode: pipelinerun.FailureReasonCodeTransformRuntimeFailed,
						ActionHint: "The transform step depends on an upstream result that is unavailable. Check the pipeline step order and that the upstream step succeeded.",
					}
					failedStep = &fs
					runErr = xerror.Unexpected().Detailf(
						"execute step %q: upstream step result not found: %s",
						step.Slug,
						upstreamStepSlug,
					)
					break
				}

				upstreamPayload, ok := upstreamStepResult.Payload()
				if !ok {
					execErr := xerror.UnexpectedUpstreamResultRequired().Detailf(
						"execute step %q: upstream step payload not found: %s",
						step.Slug,
						upstreamStepSlug,
					)
					stepCompletedAt := r.clock.Now()

					timeline.Snapshots = append(timeline.Snapshots, pipelinerun.StepSnapshot{
						Step: step.Slug,
						Kind: step.Kind,
						Input: pipelinerun.StepInputSnapshot{
							OrganizationSlug: req.OrgSlug,
							ProjectSlug:      req.ProjectSlug,
							EnvironmentSlug:  req.EnvironmentSlug,
						},
					})

					timeline.Events = append(
						timeline.Events,
						pipelinerun.TimelineEvent{
							At:     stepCompletedAt,
							Type:   "step_failed",
							Step:   step.Slug,
							Status: "failed",
						},
					)
					fs := step.Slug
					timeline.Failure = &pipelinerun.FailureContext{
						Step:       step.Slug,
						Error:      execErr.Error(),
						At:         stepCompletedAt,
						Kind:       failureKindPlatform,
						Side:       failureSideTransform,
						ReasonCode: pipelinerun.FailureReasonCodeTransformRuntimeFailed,
						ActionHint: "The upstream step did not produce usable output. Check that the source step succeeds and returns the expected data format.",
					}
					failedStep = &fs
					runErr = xerror.Wrapf("execute step %q: %w", step.Slug, execErr)
					break
				}

				upstreamPayloads[inputName] = upstreamPayload
			}
			if runErr != nil {
				break
			}

			var execResult *payload.Output
			var execErr error
			if len(timeline.StructuralChecks) > 0 {
				for _, name := range inputNames {
					contract := transformsql.ContractForNamedInput(step.Transform.InputContract, name, len(inputNames))
					if attachment := step.Transform.InputContracts[name]; attachment != nil {
						contract = attachment.Contract
					}
					if contract == nil {
						continue
					}
					source := completedResults[upstreamStepSlugs[name]]
					execErr = journal.evaluate(ctx, step.Slug, "step", "normalized_input", name, source, func() error {
						return transformsql.NewRuntime(r.transformLimits).ValidateInputContext(ctx, upstreamPayloads[name], contract)
					})
					if execErr != nil {
						break
					}
				}
			}
			if execErr == nil {
				execResult, execErr = r.transformRuntime.Execute(ctx, req.OrgSlug, step.Transform, upstreamPayloads)
			}
			stepCompletedAt := r.clock.Now()
			transformDuration := durationMilliseconds(stepStartedAt, stepCompletedAt)

			// Post-execution output contract validation.
			if execErr == nil && step.Transform.OutputContract != nil {
				if contractErr := journal.evaluate(ctx, step.Slug, "step", "producer_output", "", pipelinerun.NewTransformStepResult(execResult, transformDuration, nil), func() error {
					return transformsql.ValidateOutputContract(execResult, step.Transform.OutputContract)
				}); contractErr != nil {
					execErr = contractErr
				}
			}

			var wrappedResult *pipelinerun.StepResult
			if execResult != nil &&
				(execErr == nil || xerror.Is(execErr, transformsql.ErrOutputContractViolation)) {
				wrappedResult = pipelinerun.NewTransformStepResult(execResult, transformDuration, execErr)
				originalStep, _ := originalGraph.Step(step.Slug)
				annotateStepResultPayload(originalStep, step, wrappedResult, firstRuntimePayload(upstreamPayloads))
				if xerror.Is(execErr, transformsql.ErrOutputContractViolation) {
					markStepResultPayloadVerificationStatus(
						wrappedResult,
						payload.VerificationStatusIncompatible,
					)
				}
				diagnosticResults[step.Slug] = wrappedResult
			}

			timeline.Snapshots = append(timeline.Snapshots, pipelinerun.StepSnapshot{
				Step: step.Slug,
				Kind: step.Kind,
				Input: pipelinerun.StepInputSnapshot{
					OrganizationSlug: req.OrgSlug,
					ProjectSlug:      req.ProjectSlug,
					EnvironmentSlug:  req.EnvironmentSlug,
				},
				Output: snapshotOutput(wrappedResult),
			})

			if execErr != nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     stepCompletedAt,
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      execErr.Error(),
					At:         stepCompletedAt,
					Kind:       failureKindPlatform,
					Side:       failureSideTransform,
					ReasonCode: transformFailureReasonCode(execErr),
					Contract:   contractFailureDiagnostic(execErr),
					ActionHint: "The transform step failed to process the input data. Review the transform logic for errors.",
				}
				if timeline.Failure.Contract != nil {
					timeline.Failure.ActionHint = contractFailureActionHint()
				}
				failedStep = &fs
				runErr = xerror.Wrapf("execute step %q: %w", step.Slug, execErr)
				break
			}

			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     stepCompletedAt,
					Type:   "step_succeeded",
					Step:   step.Slug,
					Status: "succeeded",
				},
			)
			completedResults[step.Slug] = wrappedResult
			lastCompletedOutputStep = step.Slug
			diagnosticResults[step.Slug] = wrappedResult
			continue
		}

		if step.Sink != nil {
			upstreamStepSlug, sinkResolveErr := resolveSinkUpstreamStepSlugFromGraph(
				normalizedGraph,
				step.Slug,
			)
			if sinkResolveErr != nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     r.clock.Now(),
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      sinkResolveErr.Error(),
					At:         r.clock.Now(),
					Kind:       failureKindPlatform,
					Side:       failureSideSink,
					ReasonCode: pipelinerun.FailureReasonCodeSinkContractMismatch,
					ActionHint: "The sink step is missing a required upstream source step. Review the pipeline step order.",
				}
				failedStep = &fs
				runErr = xerror.Wrapf("execute step %q: %w", step.Slug, sinkResolveErr)
				break
			}

			sourceResult, ok := completedResults[upstreamStepSlug]
			if !ok {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     r.clock.Now(),
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step: step.Slug,
					Error: fmt.Sprintf(
						"sink source step result not found: %s",
						upstreamStepSlug,
					),
					At:         r.clock.Now(),
					Kind:       failureKindPlatform,
					Side:       failureSideSink,
					ReasonCode: pipelinerun.FailureReasonCodeSinkContractMismatch,
					ActionHint: "The sink step depends on an upstream result that is unavailable. Check that the source step succeeded.",
				}
				failedStep = &fs
				runErr = ErrSinkSourceResultNotFound.Detailf(
					"execute step %q: %s",
					step.Slug,
					upstreamStepSlug,
				)
				break
			}

			// Pre-sink input contract validation.
			var execErr error
			var sinkExecKind string
			var sinkStepResult *pipelinerun.StepResult
			var sinkRuntimeInput *SinkRuntimeInput
			sinkDSSlug := sinkDataSourceSlugForStep(step)
			var operationSnapshot *contractplan.OperationContent
			if op, ok := operations[step.Slug]; ok {
				var content contractplan.OperationContent
				if err := json.Unmarshal(op.Content, &content); err != nil {
					return nil, contractplan.ErrInvalid.With(err)
				}
				operationSnapshot = &content
				inputContract, _, err := contractplan.OperationContracts(op)
				if err != nil {
					return nil, err
				}
				if err := journal.evaluate(ctx, step.Slug, "operation", "pre_sink", "", sourceResult, func() error { return pipelinerun.ValidateSinkInputContract(sourceResult, inputContract) }); err != nil {
					execErr = xerror.Wrapf("operation input obligation (pre_sink): %w", err)
					sinkExecKind = failureKindPlatform
				}
			}
			var sinkInputContract *payload.Contract
			var hasSinkInputContract bool
			var contractErr error
			if operationSnapshot != nil {
				sinkInputContract = step.Sink.InputContract
				hasSinkInputContract = sinkInputContract != nil
			} else {
				sinkInputContract, hasSinkInputContract, contractErr = r.effectiveSinkInputContract(ctx, req.OrgSlug, step)
			}
			if contractErr != nil {
				execErr = contractErr
				sinkExecKind = failureKindPlatform
			} else if execErr == nil && hasSinkInputContract {
				step = stepWithSinkInputContract(step, sinkInputContract)
				setSinkInputContractForStep(diagnosticPipeline, step.Slug, sinkInputContract)
				execErr = journal.evaluate(ctx, step.Slug, "step", "pre_sink", "", sourceResult, func() error { return pipelinerun.ValidateSinkInputContract(sourceResult, sinkInputContract) })
				if execErr != nil && operationSnapshot != nil {
					execErr = xerror.Wrapf("step input obligation (pre_sink): %w", execErr)
				}
				if execErr != nil {
					sinkExecKind = failureKindPlatform
				}
			}

			if execErr == nil {
				idempotencyClass := sinkIdempotencyClassForStep(step)
				var sinkDSReadOnly bool
				var sinkDataSourceType model.DataSourceType
				if sinkDSSlug != "" {
					ds, err := r.dataSourceService.FindBySlug(ctx, req.OrgSlug, sinkDSSlug)
					switch {
					case err != nil:
						execErr = xerror.Wrapf(
							"failed to fetch sink data source %q: %w",
							sinkDSSlug,
							err,
						)
						sinkExecKind = failureKindPlatform
					case ds == nil:
						execErr = xerror.Wrapf(
							"failed to fetch sink data source %q: %w",
							sinkDSSlug,
							datasourcessvc.ErrDataSourceNotFound,
						)
						sinkExecKind = failureKindPlatform
					default:
						sinkDSReadOnly = ds.ReadOnly
						sinkDataSourceType = ds.Type
					}
				}
				if execErr == nil && !pipelinespec.SupportsSinkIdempotencyClass(sinkDataSourceType, idempotencyClass) {
					execErr = xerror.InvalidInput(xerror.Pipeline).Detailf(
						"sink data source type %q does not support idempotency_class %q",
						sinkDataSourceType,
						idempotencyClass,
					)
					sinkExecKind = failureKindPlatform
				}
				if execErr == nil {
					replayIdentity := timeline.SinkReplayIdentity
					idempotencyKey, keyErr := sinkIdempotencyKey(
						req.OrgSlug,
						req.ProjectSlug,
						req.Pipeline.Metadata.Slug,
						req.PipelineVersionID,
						replayIdentity,
						step.Slug,
						normalizedGraph,
						incrementalObservations,
					)
					if keyErr != nil {
						execErr = keyErr
						sinkExecKind = failureKindPlatform
					}
					if execErr == nil {
						sinkRuntimeInput = &SinkRuntimeInput{
							OperationSnapshot:      operationSnapshot,
							OrgSlug:                req.OrgSlug,
							ProjectSlug:            req.ProjectSlug,
							EnvironmentSlug:        req.EnvironmentSlug,
							PipelineSlug:           req.Pipeline.Metadata.Slug,
							RunSlug:                runSlugValue,
							ReplayIdentity:         replayIdentity,
							SinkStepSlug:           step.Slug,
							InputKind:              sinkInputKindForStep(step),
							SourceStep:             upstreamStepSlug,
							SourceResult:           sourceResult,
							SinkBackend:            sinkBackendForStep(step),
							SinkDataSourceSlug:     sinkDSSlug,
							SinkDataSourceReadOnly: sinkDSReadOnly,
							SinkOperationType:      sinkOperationTypeForStep(step),
							SinkOperationRef:       sinkOperationRefForStep(step),
							SinkOperationInline:    sinkOperationInlineForStep(step),
							SinkConfig:             sinkConfigForStep(step),
							IdempotencyClass:       idempotencyClass,
							IdempotencyKey:         idempotencyKey,
							EmptyInputPolicy:       step.Sink.EmptyInputPolicy,
						}
						execErr = r.sinkRuntime.Execute(ctx, sinkRuntimeInput)
						if execErr != nil {
							if xerror.Is(execErr, errSinkReplayBlocked) || xerror.Is(execErr, errSinkConfigurationInvalid) {
								sinkExecKind = failureKindPlatform
							} else {
								sinkExecKind = failureKindDataSource
							}
						}
						if sinkRuntimeInput.SinkResult != nil {
							sinkStepResult = pipelinerun.NewStepResult(sinkRuntimeInput.SinkResult)
						}
					}
				}
			}
			stepCompletedAt := r.clock.Now()

			timeline.Snapshots = append(timeline.Snapshots, pipelinerun.StepSnapshot{
				Step: step.Slug,
				Kind: step.Kind,
				Input: pipelinerun.StepInputSnapshot{
					OrganizationSlug:    req.OrgSlug,
					ProjectSlug:         req.ProjectSlug,
					EnvironmentSlug:     req.EnvironmentSlug,
					DataSourceSlug:      sinkDSSlug,
					OperationType:       sinkOperationTypeForStep(step),
					OperationRef:        sinkOperationRefForStep(step),
					UsesInline:          len(sinkOperationInlineForStep(step)) > 0,
					ParameterKeys:       sortedKeys(sinkConfigForStep(step)),
					SinkReplayStrategy:  sinkReplayStrategy(sinkRuntimeInput),
					SinkReplayMethod:    sinkReplayMethodForInput(sinkRuntimeInput),
					SinkReplayDecision:  sinkReplayDecision(sinkRuntimeInput),
					SinkEffectState:     sinkEffectState(sinkRuntimeInput),
					SinkDeliveryOutcome: sinkDeliveryOutcome(sinkRuntimeInput),
				},
				Output: snapshotOutput(sinkStepResult),
			})

			if execErr != nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     stepCompletedAt,
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				sinkReasonCode := sinkFailureReasonCode(execErr, step, sinkExecKind)
				sinkFailure := &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      execErr.Error(),
					At:         stepCompletedAt,
					Kind:       sinkExecKind,
					Side:       failureSideSink,
					ReasonCode: sinkReasonCode,
					Contract:   contractFailureDiagnostic(execErr),
				}
				switch {
				case sinkExecKind == "data_source":
					sinkFailure.Error = dataSourceFailureSummary(execErr, sinkReasonCode)
					sinkFailure.DataSourceSlug = string(sinkDSSlug)
					sinkFailure.ActionHint = dataSourceActionHint(execErr)
				case xerror.Is(execErr, errSinkConfigurationInvalid):
					sinkFailure.Error = dataSourceIssueSummary(execErr)
					sinkFailure.ActionHint = "The sink configuration failed before dispatch. Review the named sink.config field, selected operation, and payload format; validate the pipeline before retrying."
				case sinkFailure.Contract != nil:
					sinkFailure.ActionHint = contractFailureActionHint()
				default:
					sinkFailure.ActionHint = "The sink step configuration is invalid. Review the pipeline sink step settings."
				}
				if xerror.Is(execErr, errSinkReplayBlocked) {
					sinkFailure.ActionHint = "Do not start a new run to bypass this refusal. Verify the prior provider outcome; unsafe and ledger-unknown attempts require operator reconciliation outside automatic recovery."
				}
				timeline.Failure = sinkFailure
				failedStep = &fs
				failedSinks[step.Slug] = execErr
				runErr = xerror.Wrapf("execute step %q: %w", step.Slug, execErr)
				break
			}

			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     stepCompletedAt,
					Type:   "step_succeeded",
					Step:   step.Slug,
					Status: "succeeded",
				},
			)
			succeededSinks[step.Slug] = true
			continue
		}

		if step.Query == nil {
			return nil, xerror.Unexpected().Detailf("step %q has no query configuration", step.Slug)
		}

		resolvedParams, refErr := pipelinerun.ResolveParams(step.Query.Parameters, completedResults)
		if refErr != nil {
			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     r.clock.Now(),
					Type:   "step_failed",
					Step:   step.Slug,
					Status: "failed",
				},
			)
			fs := step.Slug
			timeline.Failure = &pipelinerun.FailureContext{
				Step:       step.Slug,
				Error:      refErr.Error(),
				At:         r.clock.Now(),
				Kind:       failureKindPlatform,
				Side:       failureSideSource,
				ReasonCode: pipelinerun.FailureReasonCodeSourceQueryFailed,
				ActionHint: "A step parameter reference could not be resolved. Check that all referenced upstream steps and parameters are correctly defined.",
			}
			failedStep = &fs
			runErr = xerror.Wrapf("resolve parameters for step %q: %w", step.Slug, refErr)
			break
		}

		queryOperationRef, queryOperationInline, usesInlineQueryOperation, refErr := resolveQueryOperationForStep(
			step,
		)
		if refErr != nil {
			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     r.clock.Now(),
					Type:   "step_failed",
					Step:   step.Slug,
					Status: "failed",
				},
			)
			fs := step.Slug
			timeline.Failure = &pipelinerun.FailureContext{
				Step:       step.Slug,
				Error:      refErr.Error(),
				At:         r.clock.Now(),
				Kind:       failureKindPlatform,
				Side:       failureSideSource,
				ReasonCode: pipelinerun.FailureReasonCodeSourceQueryFailed,
				ActionHint: "The query operation reference could not be resolved. Check the data source query configuration.",
			}
			failedStep = &fs
			runErr = xerror.Wrapf("resolve query operation for step %q: %w", step.Slug, refErr)
			break
		}

		var execResult *result.ExecutionResult
		var checkpointObservation *pipelinerun.IncrementalCheckpointObservation
		var execErr error
		incrementalLookup, isIncrementalSource, lookupErr := incrementalCheckpointLookupForStep(
			req.Pipeline.Metadata.Slug,
			req.PipelineVersionID,
			incrementalCheckpointNamespace(req.Incremental),
			step,
			queryOperationRef,
			queryOperationInline,
			usesInlineQueryOperation,
		)
		if lookupErr != nil {
			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     r.clock.Now(),
					Type:   "step_failed",
					Step:   step.Slug,
					Status: "failed",
				},
			)
			fs := step.Slug
			timeline.Failure = &pipelinerun.FailureContext{
				Step:       step.Slug,
				Error:      lookupErr.Error(),
				At:         r.clock.Now(),
				Kind:       failureKindPlatform,
				Side:       failureSideSource,
				ReasonCode: pipelinerun.FailureReasonCodeSourceQueryFailed,
				ActionHint: "The incremental source checkpoint identity could not be resolved. Review the source operation configuration.",
			}
			failedStep = &fs
			runErr = xerror.Wrapf("resolve incremental checkpoint for step %q: %w", step.Slug, lookupErr)
			break
		}

		switch {
		case isIncrementalSource:
			sourceWindow, hasSourceWindow := req.Incremental.SourceWindows[step.Slug]
			var currentCheckpoint *pipelinerun.IncrementalCheckpoint
			var checkpointErr error
			if !hasSourceWindow {
				currentCheckpoint, checkpointErr = r.store.GetCurrentIncrementalCheckpoint(
					ctx,
					req.OrgSlug,
					req.ProjectSlug,
					req.EnvironmentSlug,
					incrementalLookup,
				)
				if xerror.Is(checkpointErr, pipelinerun.ErrIncrementalCheckpointNotFound) {
					currentCheckpoint = nil
					checkpointErr = nil
				}
			}
			if checkpointErr != nil {
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     r.clock.Now(),
						Type:   "step_failed",
						Step:   step.Slug,
						Status: "failed",
					},
				)
				fs := step.Slug
				timeline.Failure = &pipelinerun.FailureContext{
					Step:       step.Slug,
					Error:      checkpointErr.Error(),
					At:         r.clock.Now(),
					Kind:       failureKindPlatform,
					Side:       failureSideSource,
					ReasonCode: pipelinerun.FailureReasonCodeSourceQueryFailed,
					ActionHint: "The current incremental checkpoint could not be loaded. Retry after the checkpoint store is available.",
				}
				failedStep = &fs
				runErr = xerror.Wrapf("load incremental checkpoint for step %q: %w", step.Slug, checkpointErr)
				break
			}

			checkpointInput := &pipelinerun.IncrementalCheckpointInput{
				Lookup:            incrementalLookup,
				CurrentCheckpoint: currentCheckpoint,
			}
			if hasSourceWindow {
				window := sourceWindow
				checkpointInput.Window = &window
				checkpointInput.LowerBound = cloneRawMessage(sourceWindow.LowerBound)
				checkpointInput.UpperBound = cloneRawMessage(sourceWindow.UpperBound)
			} else if currentCheckpoint != nil {
				checkpointInput.LowerBound = currentCheckpoint.ObservedUpperBound
			}
			incrementalLookups[step.Slug] = incrementalLookup
			incrementalInputs[step.Slug] = checkpointInput
			journal.observeWindow(step.Slug, checkpointInput)

			if op, ok := operations[step.Slug]; ok {
				execResult, checkpointObservation, execErr = executeCapturedOperation(ctx, r.executor, req.OrgSlug, step.Query.DataSourceSlug, op, resolvedParams, checkpointInput)
			} else if usesInlineQueryOperation {
				executor, ok := r.executor.(incrementalInlineQueryExecutor)
				if ok {
					execResult, checkpointObservation, execErr = executor.ExecuteInlineIncremental(
						ctx,
						req.OrgSlug,
						step.Query.DataSourceSlug,
						queryOperationInline,
						resolvedParams,
						checkpointInput,
					)
				} else {
					execErr = xerror.Unsupported(xerror.QueryExecutor).Detail(
						"incremental inline query executor is required for inline incremental source steps",
					).WithPublicDetail("incremental inline query executor is required")
				}
			} else if executor, ok := r.executor.(incrementalQueryExecutor); ok {
				execResult, checkpointObservation, execErr = executor.ExecuteIncremental(
					ctx,
					req.OrgSlug,
					step.Query.DataSourceSlug,
					queryOperationRef,
					resolvedParams,
					checkpointInput,
				)
			} else {
				execErr = xerror.Unsupported(xerror.QueryExecutor).Detail(
					"incremental query executor is required for incremental source steps",
				).WithPublicDetail("incremental query executor is required")
			}
			if checkpointObservation != nil {
				normalizeIncrementalObservation(
					checkpointObservation,
					step.Slug,
					incrementalLookup,
					checkpointInput.LowerBound,
					r.clock.Now(),
				)
				if len(checkpointObservation.ObservedUpperBound) == 0 &&
					len(checkpointInput.UpperBound) > 0 {
					checkpointObservation.ObservedUpperBound = cloneRawMessage(checkpointInput.UpperBound)
				}
				incrementalObservations[step.Slug] = checkpointObservation
				timeline.Events = append(
					timeline.Events,
					pipelinerun.TimelineEvent{
						At:     checkpointObservation.ObservedAt,
						Type:   "checkpoint_observed",
						Step:   step.Slug,
						Status: "observed",
					},
				)
			}
		case operations[step.Slug].Role == "query":
			execResult, _, execErr = executeCapturedOperation(ctx, r.executor, req.OrgSlug, step.Query.DataSourceSlug, operations[step.Slug], resolvedParams, nil)
		case usesInlineQueryOperation:
			execResult, execErr = r.executor.ExecuteInline(
				ctx,
				req.OrgSlug,
				step.Query.DataSourceSlug,
				queryOperationInline,
				resolvedParams,
			)
		default:
			execResult, execErr = r.executor.Execute(
				ctx,
				req.OrgSlug,
				step.Query.DataSourceSlug,
				queryOperationRef,
				resolvedParams,
			)
		}
		stepCompletedAt := r.clock.Now()

		var queryStepResult *pipelinerun.StepResult
		if execResult != nil {
			queryStepResult = pipelinerun.NewStepResult(execResult)
		}

		outputChecks := []struct {
			origin   string
			contract *payload.Contract
		}{{"step", step.Query.OutputContract}}
		if op, ok := operations[step.Slug]; ok && execErr == nil {
			_, outputContract, err := contractplan.OperationContracts(op)
			if err != nil {
				execErr = err
			} else {
				outputChecks = append([]struct {
					origin   string
					contract *payload.Contract
				}{{"operation", outputContract}}, outputChecks...)
			}
		}
		for _, check := range outputChecks {
			if execErr != nil {
				break
			}
			if check.contract == nil {
				continue
			}
			contractErr := journal.evaluate(ctx, step.Slug, check.origin, "producer_output", "", queryStepResult, func() error {
				contractErr := pipelinerun.ValidateQueryOutputContractAtRuntime(execResult, check.contract)
				if contractErr == nil && (originalPipeline.UsesContractComposition() || len(req.CompiledPlan) > 0) && queryStepResult != nil {
					if runtimePayload, ok := queryStepResult.Payload(); ok {
						contractErr = validateCompositionProducerPayload(runtimePayload, check.contract)
					}
				}
				return contractErr
			})
			if contractErr != nil {
				execErr = contractErr
				if len(req.CompiledPlan) > 0 {
					execErr = xerror.Wrapf("%s output obligation (post_query): %w", check.origin, contractErr)
				}
			}
		}

		if queryStepResult != nil &&
			(execErr == nil || xerror.Is(execErr, ErrQueryOutputContractSchemaMismatch)) {
			originalStep, _ := originalGraph.Step(step.Slug)
			annotateStepResultPayload(originalStep, step, queryStepResult, nil)
			if xerror.Is(execErr, ErrQueryOutputContractSchemaMismatch) {
				markStepResultPayloadVerificationStatus(
					queryStepResult,
					payload.VerificationStatusIncompatible,
				)
			}
			diagnosticResults[step.Slug] = queryStepResult
		}

		timeline.Snapshots = append(timeline.Snapshots, pipelinerun.StepSnapshot{
			Step: step.Slug,
			Kind: step.Kind,
			Input: pipelinerun.StepInputSnapshot{
				OrganizationSlug: req.OrgSlug,
				ProjectSlug:      req.ProjectSlug,
				EnvironmentSlug:  req.EnvironmentSlug,
				DataSourceSlug:   step.Query.DataSourceSlug,
				OperationType:    step.Query.OperationType,
				OperationRef:     step.Query.OperationRef,
				UsesInline:       usesInlineQueryOperation,
				ParameterKeys:    sortedKeys(step.Query.Parameters),
			},
			Output: snapshotOutput(queryStepResult),
		})

		if execErr != nil {
			timeline.Events = append(
				timeline.Events,
				pipelinerun.TimelineEvent{
					At:     stepCompletedAt,
					Type:   "step_failed",
					Step:   step.Slug,
					Status: "failed",
				},
			)
			fs := step.Slug
			queryFailureReasonCode := sourceFailureReasonCode(execErr)
			queryFailureKind := failureKindDataSource
			queryFailureError := dataSourceFailureSummary(execErr, queryFailureReasonCode)
			queryFailureActionHint := dataSourceActionHint(execErr)
			queryFailureContract := contractFailureDiagnostic(execErr)
			if queryFailureContract != nil || xerror.Is(execErr, ErrQueryOutputContractSchemaMismatch) {
				queryFailureKind = failureKindPlatform
				queryFailureError = execErr.Error()
				queryFailureActionHint = contractFailureActionHint()
			}
			timeline.Failure = &pipelinerun.FailureContext{
				Step:           step.Slug,
				Error:          queryFailureError,
				At:             stepCompletedAt,
				Kind:           queryFailureKind,
				Side:           failureSideSource,
				ReasonCode:     queryFailureReasonCode,
				DataSourceSlug: string(step.Query.DataSourceSlug),
				ActionHint:     queryFailureActionHint,
				Contract:       queryFailureContract,
			}
			failedStep = &fs
			runErr = xerror.Wrapf("execute step %q: %w", step.Slug, execErr)
			break
		}

		timeline.Events = append(
			timeline.Events,
			pipelinerun.TimelineEvent{
				At:     stepCompletedAt,
				Type:   "step_succeeded",
				Step:   step.Slug,
				Status: "succeeded",
			},
		)
		completedResults[step.Slug] = queryStepResult
		lastCompletedOutputStep = step.Slug
	}

	if err := journal.persist(ctx); err != nil {
		return nil, err
	}

	if timeline.Failure != nil {
		timeline.Failure.AutomaticRetryProhibited = xerror.Is(runErr, xerror.ExecutionRowLimitExceeded()) || xerror.Is(runErr, xerror.ExecutionResponseSizeExceeded()) || xerror.Is(runErr, xerror.HTTPAcknowledgementRejected()) || xerror.Is(runErr, xerror.HTTPAcknowledgementUncertain())
	}
	runCompletedAt := r.clock.Now()
	overallStatus := statusFor(runErr)
	transitions := incrementalCheckpointTransitions(
		runSlugValue,
		originalGraph,
		incrementalObservations,
		incrementalLookups,
		succeededSinks,
		failedSinks,
		timeline.Failure,
		req.Incremental,
	)
	incrementalSummary, incrementalDiagnostics := incrementalRunVisibility(
		req.Pipeline,
		req.Incremental,
		incrementalInputs,
		incrementalObservations,
		transitions,
		timeline.Failure,
		originalGraph,
		succeededSinks,
	)
	timeline.Events = append(timeline.Events, checkpointTimelineEvents(runCompletedAt, transitions, incrementalDiagnostics)...)
	pipelinerun.FinalizeStructuralChecks(timeline.StructuralChecks, runCompletedAt)
	timeline.Status = overallStatus
	timeline.Events = append(timeline.Events,
		pipelinerun.TimelineEvent{At: runCompletedAt, Type: "run_completed", Status: overallStatus},
	)
	finalized = true
	edgeDiagnostics = finalizeRunEdgeDiagnostics(diagnosticPipeline, diagnosticResults, edgeDiagnostics)

	runResult = &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			DeploymentID:               req.DeploymentID,
			DeploymentRevision:         req.DeploymentRevision,
			ProjectSlug:                req.ProjectSlug,
			EnvironmentSlug:            req.EnvironmentSlug,
			RunSlug:                    runSlugValue,
			Source:                     req.Source,
			Status:                     overallStatus,
			DurationMs:                 durationMilliseconds(runStartedAt, runCompletedAt),
			StepCount:                  len(req.Pipeline.Steps),
			PipelineVersionID:          req.PipelineVersionID,
			DeploymentResolutionDigest: req.DeploymentResolutionDigest,
			FailedStep:                 failedStep,
			ReplayOfRunSlug:            req.ReplayOfRunSlug,
			ReplayFromStep:             req.ReplayFromStep,
			Incremental:                incrementalSummary,
		},
		Timeline:                         timeline,
		StepResults:                      completedResults,
		EdgeDiagnostics:                  edgeDiagnostics,
		IncrementalCheckpointTransitions: transitions,
		IncrementalDiagnostics:           incrementalDiagnostics,
	}

	runResult.Timeline.Revision++
	runResult.Timeline.DeploymentResolutionDigest = req.DeploymentResolutionDigest
	runResult.Timeline.Admissions = deploymentplan.Admissions(ctx)
	redactRunResultSecrets(runResult, secretRedactor)
	persistErr := r.store.PersistRun(
		ctx,
		req.OrgSlug,
		req.ProjectSlug,
		req.EnvironmentSlug,
		originalPipeline,
		runResult,
	)
	if persistErr != nil {
		// Recovery may return a failed run only after its diagnostics are durable.
		runResult = nil
	}
	if persistErr != nil && runErr != nil {
		return runResult, xerror.Join(
			xerror.Wrapf("persist pipeline run: %w", persistErr),
			runErr,
		)
	}
	if persistErr != nil {
		return runResult, xerror.Wrapf("persist pipeline run: %w", persistErr)
	}
	if err := saveCheckpoint(ctx, false); err != nil {
		slog.Error("save checkpoint", "run_slug", timeline.RunSlug, "err", err)
	}
	return runResult, runErr
}

// resolveEnvironmentSecrets substitutes write-only secret values only on the execution copy.
func (r *Runner) resolveEnvironmentSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (runtimeSecretRedactor, error) {
	if !pipelineruntimevars.HasEnvironmentSecretReferenceInPipeline(pipeline) {
		return runtimeSecretRedactor{}, nil
	}
	if r.environmentSecretResolver == nil {
		return runtimeSecretRedactor{}, xerror.UnexpectedRequired(xerror.EnvironmentSecret).
			Detail("environment secret resolver is required")
	}
	secrets, err := r.environmentSecretResolver.ResolveRuntimeSecrets(ctx, orgSlug, environmentSlug)
	if err != nil {
		return runtimeSecretRedactor{}, err
	}
	redactor := newRuntimeSecretRedactor(secrets)
	return redactor, pipelineruntimevars.ResolvePipelineSecrets(pipeline, secrets)
}

func (r *Runner) validateReplaySafety(
	ctx context.Context,
	req ReplayRequest,
	pipeline *pipelinespec.Pipeline,
) error {
	if pipeline == nil {
		return nil
	}

	sourceTimeline, err := r.store.Get(ctx, req.OrgSlug, req.ProjectSlug, req.EnvironmentSlug, req.SourceRunSlug)
	if err != nil {
		if xerror.Is(err, pipelinerun.ErrTimelineNotFound) {
			return nil
		}
		return xerror.Wrapf("get replay source timeline: %w", err)
	}
	if sourceTimeline == nil ||
		sourceTimeline.Failure == nil ||
		sourceTimeline.Failure.Side != failureSideSink {
		return nil
	}

	failedSink := sourceTimeline.Failure.Step
	normalized, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return xerror.Wrapf("normalize replay pipeline graph: %w", err)
	}
	failedInput, ok := normalized.SinkInputStepSlug(failedSink)
	if !ok {
		return nil
	}

	for _, sinkSlug := range successfulSinkStepsBeforeFailure(*sourceTimeline) {
		siblingInput, ok := normalized.SinkInputStepSlug(sinkSlug)
		if !ok || siblingInput != failedInput {
			continue
		}
		successfulSinks := map[slug.Slug]bool{sinkSlug: true}
		if _, reason, blocked := replayUnsafeSiblingSink(normalized, failedSink, successfulSinks); blocked {
			return ErrReplayUnsafe.Detail(reason).WithPublicDetail(reason + ". Reconcile the provider outcome; do not start a new run to bypass replay safety.")
		}
	}

	return nil
}

func successfulSinkStepsBeforeFailure(timeline pipelinerun.RunTimeline) []slug.Slug {
	var failureAt time.Time
	if timeline.Failure != nil {
		failureAt = timeline.Failure.At
	}

	succeeded := make(map[slug.Slug]bool)
	for _, event := range timeline.Events {
		if event.Type != "step_succeeded" || event.Step == "" {
			continue
		}
		if !failureAt.IsZero() && event.At.After(failureAt) {
			continue
		}
		succeeded[event.Step] = true
	}

	for _, snapshot := range timeline.Snapshots {
		if snapshot.Input.SinkDeliveryOutcome == "no_op" || snapshot.Input.SinkReplayDecision == "skip_empty_input" {
			delete(succeeded, snapshot.Step)
		}
	}
	out := make([]slug.Slug, 0, len(succeeded))
	for stepSlug := range succeeded {
		out = append(out, stepSlug)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

func recordPipelineRunMetric(
	trigger string,
	runResult *pipelinerun.RunResult,
	runErr error,
	elapsed time.Duration,
) {
	duration := elapsed
	if runResult != nil && runResult.Metadata.DurationMs > 0 {
		duration = time.Duration(runResult.Metadata.DurationMs) * time.Millisecond
	}

	metrics.RecordPipelineRun(metrics.PipelineRunMetric{
		Trigger:   trigger,
		Outcome:   pipelineOutcome(runResult, runErr),
		ErrorType: pipelineErrorType(runResult, runErr),
		Duration:  duration,
	})
}

func incrementalCheckpointNamespace(options pipelinerun.IncrementalRunOptions) string {
	if options.PromoteCheckpoint {
		return pipelinerun.DefaultIncrementalCheckpointNamespace
	}
	if options.CheckpointNamespace != "" {
		return options.CheckpointNamespace
	}
	return pipelinerun.DefaultIncrementalCheckpointNamespace
}

func shouldSkipIncrementalCheckpointCommit(options pipelinerun.IncrementalRunOptions) bool {
	return options.SkipCheckpointCommit && !options.PromoteCheckpoint
}

func incrementalTriggerSource(options pipelinerun.IncrementalRunOptions, fallback string) string {
	if strings.TrimSpace(options.TriggerSource) != "" {
		return strings.TrimSpace(options.TriggerSource)
	}
	return fallback
}

func pipelineOutcome(runResult *pipelinerun.RunResult, runErr error) string {
	if runErr != nil {
		return metricOutcomeError
	}
	if runResult != nil {
		switch runResult.Metadata.Status {
		case runStatusSucceeded:
			return "success"
		case "canceled":
			return "canceled"
		default:
			return metricOutcomeError
		}
	}
	return "success"
}

func pipelineErrorType(runResult *pipelinerun.RunResult, runErr error) string {
	if runErr != nil {
		var appErr *xerror.Error
		if xerror.As(runErr, &appErr) {
			return appErr.Code()
		}
		return metricOutcomeError
	}
	if runResult == nil || runResult.Timeline.Failure == nil {
		return "none"
	}
	if runResult.Timeline.Failure.Kind != "" {
		return runResult.Timeline.Failure.Kind
	}
	return metricOutcomeError
}

// contractFailureDiagnostic extracts sanitized field-validation metadata from
// wrapped contract errors without copying payload values into persisted run
// detail or operator-facing responses.
func contractFailureDiagnostic(err error) *pipelinerun.ContractFailureDiagnostic {
	var contractFailure *pipelinerun.ContractFailureError
	if xerror.As(err, &contractFailure) {
		diagnostic := contractFailure.Diagnostic
		return &diagnostic
	}
	var failure *payload.ContractFieldValidationError
	if !xerror.As(err, &failure) || failure == nil {
		return nil
	}

	return &pipelinerun.ContractFailureDiagnostic{
		ContractName:   failure.ContractName,
		FieldName:      failure.FieldName,
		Rule:           failure.Rule.String(),
		ViolationCount: failure.ViolationCount,
		ReasonCode:     pipelinevalidate.ContractReasonFieldValidationFailed,
	}
}

func contractFailureActionHint() string {
	return "Review the reported contract field and validation rule, then fix the upstream data or contract declaration before retrying."
}

func clonePipeline(p *pipelinespec.Pipeline) (*pipelinespec.Pipeline, error) {
	if p == nil {
		return nil, ErrNilPipeline
	}

	literalPaths := pipelineruntimevars.LiteralLocations(p)
	data, err := json.Marshal(p)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("marshal pipeline: %w", err)
	}

	var cloned pipelinespec.Pipeline
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&cloned); err != nil {
		return nil, xerror.Unexpected().Detailf("unmarshal pipeline: %w", err)
	}
	normalizeClonedPipelineValues(&cloned)
	if !pipelineruntimevars.RestoreLiteralLocations(&cloned, literalPaths) {
		return nil, xerror.Unexpected().Detail("restore pipeline literal values")
	}

	return &cloned, nil
}

// normalizeClonedPipelineValues restores stable Go scalar types in generic spec maps.
func normalizeClonedPipelineValues(pipeline *pipelinespec.Pipeline) {
	for i := range pipeline.Steps {
		step := &pipeline.Steps[i]
		if step.Query != nil {
			step.Query.OperationInline = normalizeClonedMap(step.Query.OperationInline)
			step.Query.Parameters = normalizeClonedMap(step.Query.Parameters)
		}
		if step.Sink != nil {
			step.Sink.OperationInline = normalizeClonedMap(step.Sink.OperationInline)
			step.Sink.Config = normalizeClonedMap(step.Sink.Config)
		}
	}
}

// normalizeClonedMap recursively normalizes generic JSON map values after cloning.
func normalizeClonedMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	for key, value := range values {
		values[key] = jsonvalue.Normalize(value)
	}
	return values
}

func runSlug(explicit string, fallback slug.Slug, now time.Time) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	base, err := timestampedSlugBase(fallback, now)
	if err != nil {
		return "", err
	}

	generated, err := slug.WithRandomSuffix(base, 4)
	if err != nil {
		return "", err
	}

	return generated.String(), nil
}

func replayRunSlug(explicit, sourceRunSlug string, now time.Time) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	replayBase, err := slug.FromName(fmt.Sprintf("%s-replay", sourceRunSlug))
	if err != nil {
		return "", err
	}

	base, err := timestampedSlugBase(replayBase, now)
	if err != nil {
		return "", err
	}

	generated, err := slug.WithRandomSuffix(base, 4)
	if err != nil {
		return "", err
	}

	return generated.String(), nil
}

func timestampedSlugBase(base slug.Slug, now time.Time) (slug.Slug, error) {
	return slug.FromName(fmt.Sprintf("%s-%s", base, now.UTC().Format("20060102150405")))
}

func statusFor(err error) string {
	if err != nil {
		return "failed"
	}

	return "succeeded"
}

// preflightFailureReasonCode maps graph contract failures to the step side that
// blocked execution before any connector call or sink write could happen.
func preflightFailureReasonCode(failureSide string) pipelinerun.FailureReasonCode {
	switch failureSide {
	case failureSideSource:
		return pipelinerun.FailureReasonCodeSourceResultShapeMismatch
	case failureSideSink:
		return pipelinerun.FailureReasonCodeSinkContractMismatch
	case failureSideTransform:
		return pipelinerun.FailureReasonCodeTransformContractMismatch
	default:
		return pipelinerun.FailureReasonCodeUnknown
	}
}

// sourceFailureReasonCode classifies source-step failures using stable typed
// errors so persisted diagnostics do not depend on provider error text.
func sourceFailureReasonCode(err error) pipelinerun.FailureReasonCode {
	switch {
	case xerror.Is(err, xerror.SourceQueryInvalid()):
		return pipelinerun.FailureReasonCodeSourceQueryInvalid
	case xerror.Is(err, xerror.SourceSchemaReferenceInvalid()):
		return pipelinerun.FailureReasonCodeSourceSchemaReferenceInvalid
	case xerror.Is(err, resourcescope.ErrInvalid):
		return pipelinerun.FailureReasonCodeResourceScopeRejected
	case isDataSourceCredentialFailure(err):
		return pipelinerun.FailureReasonCodeDataSourceCredentialsFailed
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return pipelinerun.FailureReasonCodeDataSourcePermissionDenied
	case xerror.Is(err, xerror.Timeout(xerror.DataSource)) || xerror.Is(err, xerror.Timeout(xerror.Query)):
		return pipelinerun.FailureReasonCodeDataSourceTimeout
	case xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)):
		return pipelinerun.FailureReasonCodeDataSourceConnectionFailed
	case xerror.Is(err, ErrQueryOutputContractSchemaMismatch):
		return pipelinerun.FailureReasonCodeSourceResultShapeMismatch
	default:
		return pipelinerun.FailureReasonCodeSourceQueryFailed
	}
}

// transformFailureReasonCode separates sql_v1 runtime errors from runtime
// contract validation failures for operator triage.
func transformFailureReasonCode(err error) pipelinerun.FailureReasonCode {
	if xerror.Is(err, transformsql.ErrOutputContractViolation) || contractFailureDiagnostic(err) != nil {
		return pipelinerun.FailureReasonCodeTransformContractMismatch
	}
	return pipelinerun.FailureReasonCodeTransformRuntimeFailed
}

// sinkFailureReasonCode classifies failures at the sink boundary before
// user-facing summaries are sanitized for display.
func sinkFailureReasonCode(
	err error,
	step pipelinespec.Step,
	failureKind string,
) pipelinerun.FailureReasonCode {
	switch {
	case xerror.Is(err, xerror.DestinationConstraintViolation()):
		return pipelinerun.FailureReasonCodeDestinationConstraintViolation
	case xerror.Is(err, resourcescope.ErrInvalid):
		return pipelinerun.FailureReasonCodeResourceScopeRejected
	case xerror.Is(err, errSinkConfigurationInvalid):
		return pipelinerun.FailureReasonCodeSinkContractMismatch
	case xerror.Is(err, pipelinerun.ErrSinkInputContractViolation) ||
		contractFailureDiagnostic(err) != nil:
		return pipelinerun.FailureReasonCodeSinkContractMismatch
	case isDataSourceCredentialFailure(err):
		return pipelinerun.FailureReasonCodeDataSourceCredentialsFailed
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return pipelinerun.FailureReasonCodeDataSourcePermissionDenied
	case xerror.Is(err, xerror.Timeout(xerror.DataSource)) || xerror.Is(err, xerror.Timeout(xerror.Query)):
		return pipelinerun.FailureReasonCodeDataSourceTimeout
	case xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)):
		return pipelinerun.FailureReasonCodeDataSourceConnectionFailed
	case failureKind == failureKindDataSource && sinkBackendForStep(step) == model.SinkBackendS3:
		return pipelinerun.FailureReasonCodeS3ObjectWriteFailed
	default:
		return pipelinerun.FailureReasonCodeUnknown
	}
}

// isDataSourceCredentialFailure returns true for credential-auth failures that
// should be summarized generically instead of copying provider detail text.
func isDataSourceCredentialFailure(err error) bool {
	return xerror.Is(err, xerror.InvalidCredentials())
}

// dataSourceActionHint derives recovery guidance from established causes, never provider text.
func dataSourceActionHint(err error) string {
	if err == nil {
		return ""
	}

	switch {
	case xerror.Is(err, xerror.DestinationConstraintViolation()):
		return "Review the destination constraints and the values produced for this sink. Verify destination state before repeating a write; rejection does not prove rollback."
	case xerror.Is(err, resourcescope.ErrInvalid):
		return "The resource scope rejected this step. Use an unambiguous relative object key within the pinned scope, without traversal or absolute paths. Keep the bucket and target settings fixed to the pinned scope; review the selected operation and scope before retrying."
	case xerror.Is(err, xerror.SourceQueryInvalid()):
		return "Review the query syntax, expressions and types against the source database schema before retrying."
	case xerror.Is(err, xerror.SourceSchemaReferenceInvalid()):
		return "Review the query's column, relation and schema references against the current database and search path before retrying."
	case isDataSourceCredentialFailure(err):
		return "Authentication to the data source failed. Verify the credentials configured for this data source."
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return "The data source denied access. Review the connection's access policy and permission for the selected operation."
	case xerror.Is(err, xerror.NotFound(xerror.DataSource)):
		return "The selected resource was not found. Review the connection and query target settings."
	case xerror.Is(err, xerror.Timeout(xerror.DataSource)) || xerror.Is(err, xerror.Timeout(xerror.Query)):
		return "The request timed out. Verify the provider outcome before repeating a write; a timeout does not prove that it was rolled back."
	case xerror.Is(err, xerror.HTTPAcknowledgementRejected()):
		return "The HTTP response rejected the acknowledgement contract. Verify remote state before retrying; a rejected acknowledgement does not prove that the write was rolled back."
	case xerror.Is(err, xerror.HTTPAcknowledgementUncertain()):
		return "The HTTP write outcome is uncertain. Verify remote state before retrying; automatic retries are disabled."
	case xerror.Is(err, xerror.ExecutionRowLimitExceeded()):
		return "The data source result exceeded the configured row limit. Reduce the query result size or increase the execution row limit."
	case xerror.Is(err, xerror.ExecutionResponseSizeExceeded()):
		return "The data source result exceeded the configured response size limit. Reduce the returned payload or increase the execution response size limit."
	case xerror.Is(err, xerror.ExecutionRequestSizeExceeded()):
		return "The data source request exceeded the configured request size limit. Reduce the outbound payload or increase the HTTP request size limit."
	case xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()):
		return "The HTTP data source exceeded the configured redirect limit. Review the target URL or increase the HTTP redirect limit."
	case xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()):
		return "The data source reached the configured concurrency limit. Retry after active executions finish or increase the connector concurrency limit."
	case xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)):
		return "The data source could not be reached. Verify the connection settings and that the data source is online."
	default:
		return "The data source returned an unexpected error. Check the data source status and credentials."
	}
}

// dataSourceFailureSummary returns a persisted safe summary for the failure
// reason before falling back to sanitized connector error detail.
func dataSourceFailureSummary(
	err error,
	reasonCode pipelinerun.FailureReasonCode,
) string {
	switch reasonCode {
	case pipelinerun.FailureReasonCodeDestinationConstraintViolation:
		return xerror.DestinationConstraintViolation().Message()
	case pipelinerun.FailureReasonCodeSourceQueryInvalid:
		return xerror.SourceQueryInvalid().Message()
	case pipelinerun.FailureReasonCodeSourceSchemaReferenceInvalid:
		return xerror.SourceSchemaReferenceInvalid().Message()
	case pipelinerun.FailureReasonCodeResourceScopeRejected:
		return resourcescope.ErrInvalid.Message()
	case pipelinerun.FailureReasonCodeDataSourceCredentialsFailed:
		return xerror.InvalidCredentials().Error()
	case pipelinerun.FailureReasonCodeDataSourcePermissionDenied:
		return "The data source denied permission for the selected operation."
	case pipelinerun.FailureReasonCodeDataSourceConnectionFailed:
		return "The data source endpoint could not be reached."
	case pipelinerun.FailureReasonCodeDataSourceTimeout:
		return "The data source operation timed out."
	case pipelinerun.FailureReasonCodeS3ObjectWriteFailed:
		if detail := strings.TrimSpace(xerror.PublicDetail(err)); detail != "" {
			return detail
		}
		return "s3 object sink write failed"
	default:
		return dataSourceIssueSummary(err)
	}
}

// dataSourceIssueSummary exposes only explicitly safe detail or a typed message.
// Provider text can contain credentials and customer data, even without JSON.
func dataSourceIssueSummary(err error) string {
	if err == nil {
		return ""
	}
	if isDataSourceCredentialFailure(err) {
		return xerror.InvalidCredentials().Message()
	}
	if detail := strings.TrimSpace(xerror.PublicDetail(err)); detail != "" {
		return detail
	}
	var xe *xerror.Error
	if xerror.As(err, &xe) && xe != nil {
		return xe.Message()
	}
	return "data source execution failed"
}

const runtimeSecretRedaction = "[REDACTED]"

// runtimeSecretRedactor holds distinct secret values ordered to redact longer matches first.
type runtimeSecretRedactor struct {
	values []string
}

// newRuntimeSecretRedactor builds a value-only redactor from resolved runtime secrets.
func newRuntimeSecretRedactor(secrets []model.RuntimeSecretValue) runtimeSecretRedactor {
	seen := make(map[string]struct{}, len(secrets))
	values := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret.Value == "" {
			continue
		}
		if _, ok := seen[secret.Value]; ok {
			continue
		}
		seen[secret.Value] = struct{}{}
		values = append(values, secret.Value)
	}
	sort.Slice(values, func(i, j int) bool {
		return len(values[i]) > len(values[j])
	})
	return runtimeSecretRedactor{values: values}
}

// Empty reports whether the redactor has any secret values to remove.
func (r runtimeSecretRedactor) Empty() bool {
	return len(r.values) == 0
}

// RedactString replaces every known secret value in a string without mutating the redactor.
func (r runtimeSecretRedactor) RedactString(value string) string {
	if value == "" || r.Empty() {
		return value
	}
	redacted := value
	for _, secret := range r.values {
		redacted = strings.ReplaceAll(redacted, secret, runtimeSecretRedaction)
	}
	return redacted
}

// RedactError wraps an error with a redacted message while preserving the original cause.
func (r runtimeSecretRedactor) RedactError(err error) error {
	if err == nil || r.Empty() {
		return err
	}
	redacted := r.RedactString(err.Error())
	if redacted == err.Error() {
		return err
	}
	return redactedRuntimeSecretError{message: redacted, cause: err}
}

// redactedRuntimeSecretError exposes a safe error string while retaining unwrap access to the cause.
type redactedRuntimeSecretError struct {
	message string
	cause   error
}

// Error returns the redacted error message.
func (e redactedRuntimeSecretError) Error() string {
	return e.message
}

// Unwrap returns the original error for sentinel and type checks.
func (e redactedRuntimeSecretError) Unwrap() error {
	return e.cause
}

// redactRunResultSecrets mutates run-result metadata and replaces stored step results with redacted clones.
func redactRunResultSecrets(runResult *pipelinerun.RunResult, redactor runtimeSecretRedactor) {
	if runResult == nil || redactor.Empty() {
		return
	}
	if runResult.Timeline.Failure != nil {
		runResult.Timeline.Failure.Error = redactor.RedactString(runResult.Timeline.Failure.Error)
		runResult.Timeline.Failure.ActionHint = redactor.RedactString(runResult.Timeline.Failure.ActionHint)
		runResult.Timeline.Failure.DataSourceSlug = redactor.RedactString(runResult.Timeline.Failure.DataSourceSlug)
	}
	redactIncrementalSummary(&runResult.Metadata.Incremental, redactor)
	redactIncrementalDiagnostics(&runResult.IncrementalDiagnostics, redactor)
	redactIncrementalCheckpointTransitions(runResult.IncrementalCheckpointTransitions, redactor)
	runResult.StepResults = redactStepResults(runResult.StepResults, redactor)
	for i := range runResult.Timeline.Snapshots {
		output := runResult.Timeline.Snapshots[i].Output
		if output == nil {
			continue
		}
		if output.Details == nil {
			continue
		}
		if output.Details.TargetPath != nil {
			*output.Details.TargetPath = redactor.RedactString(*output.Details.TargetPath)
		}
		if output.Details.File != nil {
			output.Details.File.Filename = redactor.RedactString(output.Details.File.Filename)
		}
	}
	for i := range runResult.EdgeDiagnostics {
		redaction.StringMap(runResult.EdgeDiagnostics[i].Expected, redactor.RedactString)
		redaction.StringMap(runResult.EdgeDiagnostics[i].Actual, redactor.RedactString)
	}
}

// redactIncrementalSummary mutates the user-visible incremental summary fields in place.
func redactIncrementalSummary(summary *pipelinerun.IncrementalRunSummary, redactor runtimeSecretRedactor) {
	if summary == nil {
		return
	}
	summary.SourceStepSlug = redactor.RedactString(summary.SourceStepSlug)
	summary.BlockedReason = redactor.RedactString(summary.BlockedReason)
	summary.ReplayBlockedReason = redactor.RedactString(summary.ReplayBlockedReason)
	summary.BackfillSeriesSlug = redactor.RedactString(summary.BackfillSeriesSlug)
}

// redactIncrementalDiagnostics mutates checkpoint diagnostics that can echo resolved runtime values.
func redactIncrementalDiagnostics(
	diagnostics *pipelinerun.IncrementalRunDiagnostics,
	redactor runtimeSecretRedactor,
) {
	if diagnostics == nil {
		return
	}
	for i := range diagnostics.Sources {
		source := &diagnostics.Sources[i]
		source.SourceStepSlug = redactor.RedactString(source.SourceStepSlug)
		source.CheckpointKind = redactor.RedactString(source.CheckpointKind)
		redactIncrementalCheckpointSummary(&source.PreviousCheckpoint, redactor)
		redactIncrementalCheckpointWindowSummary(&source.Window, redactor)
		redactIncrementalCheckpointSummary(&source.ObservedCheckpoint, redactor)
		source.CommitResult.BlockedByStep = redactor.RedactString(source.CommitResult.BlockedByStep)
		source.CommitResult.BlockedReason = redactor.RedactString(source.CommitResult.BlockedReason)
	}
	diagnostics.ReplaySafety.BlockedSink = redactor.RedactString(diagnostics.ReplaySafety.BlockedSink)
	diagnostics.ReplaySafety.BlockedReason = redactor.RedactString(diagnostics.ReplaySafety.BlockedReason)
}

// redactIncrementalCheckpointTransitions mutates checkpoint transition records before persistence.
func redactIncrementalCheckpointTransitions(
	transitions []pipelinerun.IncrementalCheckpointTransition,
	redactor runtimeSecretRedactor,
) {
	for i := range transitions {
		transition := &transitions[i]
		redactIncrementalCheckpointLookup(&transition.Lookup, redactor)
		transition.CheckpointNamespace = redactor.RedactString(transition.CheckpointNamespace)
		transition.CheckpointKind = redactor.RedactString(transition.CheckpointKind)
		transition.OperationType = redactor.RedactString(transition.OperationType)
		transition.OperationInlineHash = redactor.RedactString(transition.OperationInlineHash)
		transition.OperationIdentity = redactor.RedactString(transition.OperationIdentity)
		transition.ObservedLowerBound = redaction.RawJSON(transition.ObservedLowerBound, redactor.RedactString)
		transition.ObservedUpperBound = redaction.RawJSON(transition.ObservedUpperBound, redactor.RedactString)
		transition.DisplayMetadata = redaction.RawJSON(transition.DisplayMetadata, redactor.RedactString)
		transition.ObservedRunSlug = redactor.RedactString(transition.ObservedRunSlug)
		transition.CommittingRunSlug = redactor.RedactString(transition.CommittingRunSlug)
		transition.BlockedReason = redactor.RedactString(transition.BlockedReason)
	}
}

// redactIncrementalCheckpointLookup mutates checkpoint lookup identity and cursor fields.
func redactIncrementalCheckpointLookup(
	lookup *pipelinerun.IncrementalCheckpointLookup,
	redactor runtimeSecretRedactor,
) {
	if lookup == nil {
		return
	}
	lookup.CheckpointNamespace = redactor.RedactString(lookup.CheckpointNamespace)
	lookup.CheckpointKind = redactor.RedactString(lookup.CheckpointKind)
	lookup.OperationType = redactor.RedactString(lookup.OperationType)
	lookup.OperationInlineHash = redactor.RedactString(lookup.OperationInlineHash)
	lookup.OperationIdentity = redactor.RedactString(lookup.OperationIdentity)
	lookup.IncrementalField = redactor.RedactString(lookup.IncrementalField)
	lookup.IncrementalCursor = redactor.RedactString(lookup.IncrementalCursor)
	for i, value := range lookup.IncrementalOrdering {
		lookup.IncrementalOrdering[i] = redactor.RedactString(value)
	}
	lookup.IncrementalDedupeKey = redactor.RedactString(lookup.IncrementalDedupeKey)
	lookup.IncrementalLookback = redactor.RedactString(lookup.IncrementalLookback)
}

// redactIncrementalCheckpointSummary mutates summary text and display metadata in place.
func redactIncrementalCheckpointSummary(
	summary *pipelinerun.IncrementalCheckpointSummary,
	redactor runtimeSecretRedactor,
) {
	if summary == nil {
		return
	}
	summary.Summary = redactor.RedactString(summary.Summary)
	redaction.AnyMap(summary.DisplayMetadata, redactor.RedactString)
}

// redactIncrementalCheckpointWindowSummary mutates window bounds and display metadata in place.
func redactIncrementalCheckpointWindowSummary(
	summary *pipelinerun.IncrementalCheckpointWindowSummary,
	redactor runtimeSecretRedactor,
) {
	if summary == nil {
		return
	}
	summary.Summary = redactor.RedactString(summary.Summary)
	summary.LowerBound = redactor.RedactString(summary.LowerBound)
	summary.UpperBound = redactor.RedactString(summary.UpperBound)
	redaction.AnyMap(summary.DisplayMetadata, redactor.RedactString)
}

// redactStepResults returns a new step-result map whose values are redacted clones.
func redactStepResults(
	results map[slug.Slug]*pipelinerun.StepResult,
	redactor runtimeSecretRedactor,
) map[slug.Slug]*pipelinerun.StepResult {
	if len(results) == 0 || redactor.Empty() {
		return results
	}

	redacted := make(map[slug.Slug]*pipelinerun.StepResult, len(results))
	for stepSlug, stepResult := range results {
		redacted[stepSlug] = stepResult.RedactedClone(redactor.RedactString)
	}
	return redacted
}

// cloneStepResultsForStorage returns either plain clones or redacted clones for run persistence.
func cloneStepResultsForStorage(
	results map[slug.Slug]*pipelinerun.StepResult,
	redactor runtimeSecretRedactor,
) map[slug.Slug]*pipelinerun.StepResult {
	if redactor.Empty() {
		return cloneStepResults(results)
	}
	return redactStepResults(results, redactor)
}

func durationMilliseconds(startedAt, completedAt time.Time) int64 {
	if completedAt.Before(startedAt) {
		return 0
	}

	return completedAt.Sub(startedAt).Milliseconds()
}

func preflightFailureContext(
	pipeline *pipelinespec.Pipeline,
	edgeDiagnostics []pipelinevalidate.EdgeCompatibilityResult,
) (slug.Slug, string) {
	if pipeline == nil || len(pipeline.Steps) == 0 {
		return "", failureSideTransform
	}

	failureStep := pipeline.Steps[0].Slug
	failureSide := failureSideTransform

	for _, diagnostic := range edgeDiagnostics {
		if diagnostic.Compatible {
			continue
		}

		if diagnostic.ToStepSlug != "" {
			failureStep = diagnostic.ToStepSlug
		}

		if step, ok := stepBySlug(pipeline, failureStep); ok {
			switch {
			case step.Sink != nil:
				failureSide = failureSideSink
			case step.Query != nil:
				failureSide = failureSideSource
			default:
				failureSide = failureSideTransform
			}
		}

		return failureStep, failureSide
	}

	return failureStep, failureSide
}

func sortedKeys(values map[string]any) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func resolveTransformInputStepSlugs(pipeline *pipelinespec.Pipeline, stepIndex int) (map[string]slug.Slug, error) {
	if pipeline == nil {
		return nil, ErrNilPipeline
	}
	if stepIndex <= 0 || stepIndex >= len(pipeline.Steps) {
		return nil, ErrTransformUpstreamRequired
	}

	step := pipeline.Steps[stepIndex]
	normalized, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return nil, ErrTransformUpstreamRequired.With(err).Detail(fmt.Sprintf("for %q", step.Slug))
	}
	return resolveTransformInputStepSlugsFromGraph(normalized, step.Slug)
}

func resolveTransformInputStepSlugsFromGraph(
	normalized *pipelinegraph.Graph,
	stepSlug slug.Slug,
) (map[string]slug.Slug, error) {
	if normalized == nil {
		return nil, ErrNilPipeline
	}

	inputs, ok := normalized.TransformInputStepSlugs(stepSlug)
	if !ok || len(inputs) == 0 {
		return nil, ErrTransformUpstreamRequired.Detailf("for %q", stepSlug)
	}
	return inputs, nil
}

func resolveTransformUpstreamStepSlug(pipeline *pipelinespec.Pipeline, stepIndex int) (slug.Slug, error) {
	upstreamStepSlugs, err := resolveTransformInputStepSlugs(pipeline, stepIndex)
	if err != nil {
		return "", err
	}
	if len(upstreamStepSlugs) != 1 {
		return "", ErrTransformUpstreamRequired.Detailf("for step[%d]", stepIndex)
	}
	for _, upstreamStepSlug := range upstreamStepSlugs {
		return upstreamStepSlug, nil
	}
	return "", ErrTransformUpstreamRequired.Detailf("for step[%d]", stepIndex)
}

func firstRuntimePayload(payloads map[string]*payload.Payload) *payload.Payload {
	if len(payloads) == 0 {
		return nil
	}
	names := make([]string, 0, len(payloads))
	for name := range payloads {
		names = append(names, name)
	}
	sort.Strings(names)
	return payloads[names[0]]
}

func stepBySlug(pipeline *pipelinespec.Pipeline, stepSlug slug.Slug) (pipelinespec.Step, bool) {
	normalized, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return pipelinespec.Step{}, false
	}

	return normalized.Step(stepSlug)
}

func sinkInputKindForStep(step pipelinespec.Step) model.TransformOutputKind {
	return pipelinevalidate.SinkInputKindForStep(step)
}

func (r *Runner) effectiveSinkInputContract(
	ctx context.Context,
	orgSlug slug.Slug,
	step pipelinespec.Step,
) (*payload.Contract, bool, error) {
	if step.Sink == nil {
		return nil, false, nil
	}

	if step.Sink.InputContract != nil {
		return cloneRunnerSchemaContract(step.Sink.InputContract), true, nil
	}

	if step.Sink.OperationRef == "" || step.Sink.DataSourceSlug == "" {
		return nil, false, nil
	}

	contract, err := r.executor.GetExpectedInputContract(
		ctx,
		orgSlug,
		step.Sink.DataSourceSlug,
		step.Sink.OperationRef,
	)
	if err != nil {
		return nil, false, xerror.Wrapf(
			"failed to resolve saved sink input contract for %q: %w",
			step.Sink.OperationRef,
			err,
		)
	}

	if contract == nil {
		return nil, false, nil
	}

	return cloneRunnerSchemaContract(contract), true, nil
}

func cloneRunnerSchemaContract(contract *payload.Contract) *payload.Contract {
	return payload.CloneContract(contract)
}

func stepWithSinkInputContract(step pipelinespec.Step, contract *payload.Contract) pipelinespec.Step {
	if step.Sink == nil || contract == nil {
		return step
	}

	sink := *step.Sink
	sink.InputContract = cloneRunnerSchemaContract(contract)
	step.Sink = &sink
	return step
}

func setSinkInputContractForStep(
	pipeline *pipelinespec.Pipeline,
	stepSlug slug.Slug,
	contract *payload.Contract,
) {
	if pipeline == nil || contract == nil {
		return
	}

	for i := range pipeline.Steps {
		if pipeline.Steps[i].Slug != stepSlug || pipeline.Steps[i].Sink == nil {
			continue
		}

		sink := *pipeline.Steps[i].Sink
		sink.InputContract = cloneRunnerSchemaContract(contract)
		pipeline.Steps[i].Sink = &sink
		return
	}
}

func sinkBackendForStep(step pipelinespec.Step) model.SinkBackend {
	if step.Sink == nil {
		return model.SinkBackendUnknown
	}
	if step.Sink.OperationType != "" {
		backend, _ := pipelinespec.SinkBackendForOperationType(step.Sink.OperationType)
		return backend
	}
	return model.SinkBackendUnknown
}

func sinkDataSourceSlugForStep(step pipelinespec.Step) slug.Slug {
	if step.Sink == nil {
		return ""
	}

	return step.Sink.DataSourceSlug
}

func sinkOperationRefForStep(step pipelinespec.Step) slug.Slug {
	if step.Sink == nil {
		return ""
	}

	return step.Sink.OperationRef
}

func sinkOperationTypeForStep(step pipelinespec.Step) string {
	if step.Sink == nil {
		return ""
	}

	return step.Sink.OperationType
}

func sinkOperationInlineForStep(step pipelinespec.Step) map[string]any {
	if step.Sink == nil || len(step.Sink.OperationInline) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(step.Sink.OperationInline))
	for key, value := range step.Sink.OperationInline {
		cloned[key] = value
	}

	return cloned
}

// incrementalCheckpointLookupForStep builds the stable source checkpoint identity
// used to read and write committed progress for an incremental query step.
func incrementalCheckpointLookupForStep(
	pipelineSlug slug.Slug,
	pipelineVersionID int64,
	checkpointNamespace string,
	step pipelinespec.Step,
	operationRef slug.Slug,
	operationInline map[string]any,
	usesInline bool,
) (pipelinerun.IncrementalCheckpointLookup, bool, error) {
	if step.Query == nil || step.Query.Incremental == nil {
		return pipelinerun.IncrementalCheckpointLookup{}, false, nil
	}
	checkpointKind := ""
	if step.Query.Incremental.CheckpointKind != nil {
		checkpointKind = step.Query.Incremental.CheckpointKind.String()
	}

	operationInlineHash := ""
	if usesInline {
		identity, err := pipelineruntimevars.BoundOperationIdentity(operationInline, step.Query.Parameters)
		if err != nil {
			return pipelinerun.IncrementalCheckpointLookup{}, true, err
		}
		operationInlineJSON, err := json.Marshal(identity)
		if err != nil {
			return pipelinerun.IncrementalCheckpointLookup{}, true, err
		}
		sum := sha256.Sum256(operationInlineJSON)
		operationInlineHash = hex.EncodeToString(sum[:])
	}

	namespace := checkpointNamespace
	if namespace == "" {
		namespace = pipelinerun.DefaultIncrementalCheckpointNamespace
	}
	operationIdentity := strings.Join([]string{
		"pipeline=" + pipelineSlug.String(),
		"source=" + step.Slug.String(),
		"namespace=" + namespace,
		"kind=" + checkpointKind,
		"data_source=" + step.Query.DataSourceSlug.String(),
		"operation_type=" + step.Query.OperationType,
		"operation_ref=" + operationRef.String(),
		"operation_inline_hash=" + operationInlineHash,
		"field=" + step.Query.Incremental.Field,
		"cursor=" + step.Query.Incremental.Cursor,
		"ordering=" + strings.Join(step.Query.Incremental.Ordering, ","),
		"dedupe_key=" + step.Query.Incremental.DedupeKey,
	}, "|")

	return pipelinerun.IncrementalCheckpointLookup{
		PipelineSlug:         pipelineSlug,
		PipelineVersionID:    pipelineVersionID,
		SourceStepSlug:       step.Slug,
		CheckpointNamespace:  namespace,
		CheckpointKind:       checkpointKind,
		DataSourceSlug:       step.Query.DataSourceSlug,
		OperationType:        step.Query.OperationType,
		OperationRef:         operationRef,
		OperationInlineHash:  operationInlineHash,
		OperationIdentity:    operationIdentity,
		IncrementalField:     step.Query.Incremental.Field,
		IncrementalCursor:    step.Query.Incremental.Cursor,
		IncrementalOrdering:  append([]string(nil), step.Query.Incremental.Ordering...),
		IncrementalDedupeKey: step.Query.Incremental.DedupeKey,
		IncrementalLookback:  step.Query.Incremental.LookbackWindow,
	}, true, nil
}

// normalizeIncrementalObservation fills runtime-owned observation defaults that
// connector fakes and future connector implementations may omit.
func normalizeIncrementalObservation(
	observation *pipelinerun.IncrementalCheckpointObservation,
	sourceStepSlug slug.Slug,
	lookup pipelinerun.IncrementalCheckpointLookup,
	lowerBound json.RawMessage,
	now time.Time,
) {
	if observation.SourceStepSlug == "" {
		observation.SourceStepSlug = sourceStepSlug
	}
	if observation.CheckpointNamespace == "" {
		observation.CheckpointNamespace = lookup.CheckpointNamespace
	}
	if observation.CheckpointKind == "" {
		observation.CheckpointKind = lookup.CheckpointKind
	}
	if len(observation.ObservedLowerBound) == 0 {
		observation.ObservedLowerBound = lowerBound
	}
	if len(observation.DisplayMetadata) == 0 {
		observation.DisplayMetadata = json.RawMessage(`{}`)
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = now.UTC()
	}
}

// incrementalCheckpointTransitions converts source observations into committed
// or blocked persistence transitions after sink outcomes are known.
func incrementalCheckpointTransitions(
	runSlugValue string,
	g *pipelinegraph.Graph,
	observations map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation,
	lookups map[slug.Slug]pipelinerun.IncrementalCheckpointLookup,
	succeededSinks map[slug.Slug]bool,
	failedSinks map[slug.Slug]error,
	failure *pipelinerun.FailureContext,
	options pipelinerun.IncrementalRunOptions,
) []pipelinerun.IncrementalCheckpointTransition {
	if len(observations) == 0 {
		return nil
	}

	sourceSlugs := make([]slug.Slug, 0, len(observations))
	for sourceSlug := range observations {
		sourceSlugs = append(sourceSlugs, sourceSlug)
	}
	sort.Slice(sourceSlugs, func(i, j int) bool {
		return sourceSlugs[i] < sourceSlugs[j]
	})

	transitions := make([]pipelinerun.IncrementalCheckpointTransition, 0, len(sourceSlugs))
	for _, sourceSlug := range sourceSlugs {
		observation := observations[sourceSlug]
		lookup := lookups[sourceSlug]
		transition := incrementalCheckpointTransitionFromObservation(
			runSlugValue,
			lookup,
			observation,
		)

		blockedBy, blockedReason, blocked := incrementalCheckpointBlocker(
			g,
			sourceSlug,
			succeededSinks,
			failedSinks,
			failure,
		)
		if blocked {
			transition.Status = pipelinerun.IncrementalCheckpointTransitionBlocked
			transition.CommittingRunSlug = ""
			transition.BlockedByStep = blockedBy
			transition.BlockedReason = blockedReason
		}
		if shouldSkipIncrementalCheckpointCommit(options) && !blocked {
			transition.Status = pipelinerun.IncrementalCheckpointTransitionBlocked
			transition.CommittingRunSlug = ""
			transition.BlockedReason = "checkpoint promotion skipped for this run"
		}

		transitions = append(transitions, transition)
	}

	return transitions
}

func fullIncrementalRunSummary() pipelinerun.IncrementalRunSummary {
	return pipelinerun.IncrementalRunSummary{
		Mode:               "full",
		CheckpointStatus:   "not_applicable",
		ReplaySafetyStatus: "safe",
	}
}

func emptyIncrementalRunDiagnostics() pipelinerun.IncrementalRunDiagnostics {
	return pipelinerun.IncrementalRunDiagnostics{
		Sources:      []pipelinerun.IncrementalSourceDiagnostic{},
		ReplaySafety: pipelinerun.IncrementalReplaySafety{Status: "safe"},
	}
}

// incrementalRunVisibility folds checkpoint inputs, observations, and commit
// transitions into the run-owned read model. It emits display summaries only:
// raw checkpoint JSON may contain opaque cursor bodies and must not be surfaced
// through API, CLI, or SSR views.
func incrementalRunVisibility(
	pipeline *pipelinespec.Pipeline,
	options pipelinerun.IncrementalRunOptions,
	inputs map[slug.Slug]*pipelinerun.IncrementalCheckpointInput,
	observations map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation,
	transitions []pipelinerun.IncrementalCheckpointTransition,
	failure *pipelinerun.FailureContext,
	g *pipelinegraph.Graph,
	succeededSinks map[slug.Slug]bool,
) (pipelinerun.IncrementalRunSummary, pipelinerun.IncrementalRunDiagnostics) {
	if len(inputs) == 0 && len(observations) == 0 && !pipelineHasIncrementalSource(pipeline) {
		return fullIncrementalRunSummary(), emptyIncrementalRunDiagnostics()
	}

	replaySafety := incrementalReplaySafety(failure, g, succeededSinks)
	summary := pipelinerun.IncrementalRunSummary{
		Mode:                incrementalRunMode(options),
		ReplaySafetyStatus:  replaySafety.Status,
		ReplayBlockedReason: replaySafety.BlockedReason,
		BackfillSeriesSlug:  options.BackfillSeriesSlug.String(),
		BackfillChunkIndex:  options.BackfillChunkIndex,
		BackfillChunkCount:  options.BackfillChunkCount,
	}

	transitionBySource := make(map[slug.Slug]pipelinerun.IncrementalCheckpointTransition, len(transitions))
	for _, transition := range transitions {
		transitionBySource[transition.SourceStepSlug] = transition
	}

	// Execution can fail after a source window is selected but before a
	// connector returns an observation, so detail rows need the union of both.
	sourceSlugs := make([]slug.Slug, 0, len(inputs)+len(observations))
	seen := make(map[slug.Slug]bool, len(inputs)+len(observations))
	for sourceSlug := range inputs {
		sourceSlugs = append(sourceSlugs, sourceSlug)
		seen[sourceSlug] = true
	}
	for sourceSlug := range observations {
		if !seen[sourceSlug] {
			sourceSlugs = append(sourceSlugs, sourceSlug)
		}
	}
	sort.Slice(sourceSlugs, func(i, j int) bool {
		return sourceSlugs[i] < sourceSlugs[j]
	})

	diagnostics := pipelinerun.IncrementalRunDiagnostics{
		Sources:      make([]pipelinerun.IncrementalSourceDiagnostic, 0, len(sourceSlugs)),
		ReplaySafety: replaySafety,
	}
	// List cards show the most operationally important source state.
	statusPriority := map[string]int{
		"not_applicable":        0,
		"committed":             1,
		"unchanged":             2,
		checkpointStatusSkipped: 3,
		checkpointStatusBlocked: 4,
	}
	for _, sourceSlug := range sourceSlugs {
		input := inputs[sourceSlug]
		observation := observations[sourceSlug]
		transition, hasTransition := transitionBySource[sourceSlug]
		sourceDiagnostic := incrementalSourceDiagnostic(sourceSlug, input, observation, transition, hasTransition, options)
		diagnostics.Sources = append(diagnostics.Sources, sourceDiagnostic)

		if summary.CheckpointStatus == "" ||
			statusPriority[sourceDiagnostic.CommitResult.Status] > statusPriority[summary.CheckpointStatus] {
			summary.CheckpointStatus = sourceDiagnostic.CommitResult.Status
			summary.SourceStepSlug = string(sourceSlug)
			summary.BlockedReason = sourceDiagnostic.CommitResult.BlockedReason
		} else if summary.SourceStepSlug == "" {
			summary.SourceStepSlug = string(sourceSlug)
		}
	}
	if len(diagnostics.Sources) == 0 {
		summary.CheckpointStatus = checkpointStatusSkipped
	}
	if summary.CheckpointStatus == checkpointStatusBlocked &&
		summary.BlockedReason != "" &&
		summary.ReplaySafetyStatus == "safe" {
		summary.ReplaySafetyStatus = checkpointStatusBlocked
		summary.ReplayBlockedReason = summary.BlockedReason
		diagnostics.ReplaySafety.Status = checkpointStatusBlocked
		diagnostics.ReplaySafety.BlockedReason = summary.BlockedReason
	}

	return summary, diagnostics
}

func incrementalRunMode(options pipelinerun.IncrementalRunOptions) string {
	if options.BackfillSeriesSlug != "" ||
		strings.HasPrefix(options.CheckpointNamespace, "backfill:") {
		return "backfill"
	}
	return "incremental"
}

func pipelineHasIncrementalSource(pipeline *pipelinespec.Pipeline) bool {
	if pipeline == nil {
		return false
	}
	for _, step := range pipeline.Steps {
		if step.Query != nil && step.Query.Incremental != nil {
			return true
		}
	}
	return false
}

// incrementalSourceDiagnostic builds one sanitized detail row for a source.
// The raw lower and upper bounds are reduced to compact display values and are
// marked as not exposed even when they are simple timestamp-like values.
func incrementalSourceDiagnostic(
	sourceSlug slug.Slug,
	input *pipelinerun.IncrementalCheckpointInput,
	observation *pipelinerun.IncrementalCheckpointObservation,
	transition pipelinerun.IncrementalCheckpointTransition,
	hasTransition bool,
	options pipelinerun.IncrementalRunOptions,
) pipelinerun.IncrementalSourceDiagnostic {
	checkpointKind := ""
	if observation != nil {
		checkpointKind = observation.CheckpointKind
	}
	if checkpointKind == "" && input != nil {
		checkpointKind = input.Lookup.CheckpointKind
	}
	if checkpointKind == "" {
		checkpointKind = transition.CheckpointKind
	}

	diagnostic := pipelinerun.IncrementalSourceDiagnostic{
		SourceStepSlug:     string(sourceSlug),
		CheckpointKind:     checkpointKind,
		PreviousCheckpoint: previousCheckpointSummary(input),
		Window:             checkpointWindowSummary(input, observation),
		ObservedCheckpoint: observedCheckpointSummary(observation),
		CommitResult:       checkpointCommitResult(input, observation, transition, hasTransition, options),
	}
	return diagnostic
}

func previousCheckpointSummary(input *pipelinerun.IncrementalCheckpointInput) pipelinerun.IncrementalCheckpointSummary {
	if input == nil || input.CurrentCheckpoint == nil {
		return checkpointSummaryFromRaw("", nil, nil, "none")
	}
	return checkpointSummaryFromRaw(
		input.Lookup.CheckpointKind,
		input.CurrentCheckpoint.ObservedUpperBound,
		input.CurrentCheckpoint.DisplayMetadata,
		"none",
	)
}

func observedCheckpointSummary(
	observation *pipelinerun.IncrementalCheckpointObservation,
) pipelinerun.IncrementalCheckpointSummary {
	if observation == nil {
		return checkpointSummaryFromRaw("", nil, nil, "not observed")
	}
	return checkpointSummaryFromRaw(
		observation.CheckpointKind,
		observation.ObservedUpperBound,
		observation.DisplayMetadata,
		"not observed",
	)
}

// checkpointWindowSummary describes the effective source read window. Backfill
// callers usually provide both bounds; normal incremental runs usually have only
// the previous committed checkpoint as the lower bound.
func checkpointWindowSummary(
	input *pipelinerun.IncrementalCheckpointInput,
	observation *pipelinerun.IncrementalCheckpointObservation,
) pipelinerun.IncrementalCheckpointWindowSummary {
	var lower, upper, display json.RawMessage
	if input != nil {
		lower = input.LowerBound
		upper = input.UpperBound
		if input.Window != nil {
			display = input.Window.DisplayMetadata
		}
	}
	if len(upper) == 0 && observation != nil {
		upper = observation.ObservedUpperBound
	}
	checkpointKind := checkpointKindForSummary(input, observation)
	lowerSummary := CheckpointValueSummary(checkpointKind, lower)
	upperSummary := CheckpointValueSummary(checkpointKind, upper)
	summary := "full source"
	switch {
	case lowerSummary != "" && upperSummary != "":
		summary = lowerSummary + " to " + upperSummary
	case lowerSummary != "":
		summary = "after " + lowerSummary
	case upperSummary != "":
		summary = "through " + upperSummary
	}
	if displaySummary := displayMetadataSummary(display); displaySummary != "" {
		summary = displaySummary
	}

	return pipelinerun.IncrementalCheckpointWindowSummary{
		Summary:         summary,
		LowerBound:      lowerSummary,
		UpperBound:      upperSummary,
		DisplayMetadata: displayMetadataMap(display),
		RawValueExposed: false,
	}
}

func checkpointKindForSummary(
	input *pipelinerun.IncrementalCheckpointInput,
	observation *pipelinerun.IncrementalCheckpointObservation,
) string {
	if observation != nil && observation.CheckpointKind != "" {
		return observation.CheckpointKind
	}
	if input != nil {
		return input.Lookup.CheckpointKind
	}
	return ""
}

// checkpointCommitResult turns checkpoint persistence transitions into operator
// statuses. A committed transition whose observed upper bound equals the lower
// bound is shown as unchanged because no checkpoint progress was made.
func checkpointCommitResult(
	input *pipelinerun.IncrementalCheckpointInput,
	observation *pipelinerun.IncrementalCheckpointObservation,
	transition pipelinerun.IncrementalCheckpointTransition,
	hasTransition bool,
	options pipelinerun.IncrementalRunOptions,
) pipelinerun.IncrementalCheckpointCommitResult {
	result := pipelinerun.IncrementalCheckpointCommitResult{Status: checkpointStatusSkipped}
	if !hasTransition {
		return result
	}
	switch transition.Status {
	case pipelinerun.IncrementalCheckpointTransitionCommitted:
		result.Status = "committed"
		if observation != nil && input != nil &&
			bytes.Equal(bytes.TrimSpace(observation.ObservedUpperBound), bytes.TrimSpace(input.LowerBound)) {
			result.Status = "unchanged"
		}
	case pipelinerun.IncrementalCheckpointTransitionBlocked:
		result.Status = checkpointStatusBlocked
		if shouldSkipIncrementalCheckpointCommit(options) &&
			strings.Contains(transition.BlockedReason, checkpointStatusSkipped) {
			result.Status = checkpointStatusSkipped
			return result
		}
		result.BlockedByStep = transition.BlockedByStep.String()
		result.BlockedReason = transition.BlockedReason
	default:
		result.Status = checkpointStatusSkipped
	}
	return result
}

// checkpointSummaryFromRaw prefers connector-provided display metadata and only
// falls back to compact scalar summaries. Unknown object-shaped values are
// redacted instead of being serialized back to operators.
func checkpointSummaryFromRaw(
	checkpointKind string,
	raw json.RawMessage,
	display json.RawMessage,
	fallback string,
) pipelinerun.IncrementalCheckpointSummary {
	summary := displayMetadataSummary(display)
	if summary == "" {
		summary = CheckpointValueSummary(checkpointKind, raw)
	}
	if summary == "" {
		summary = fallback
	}
	return pipelinerun.IncrementalCheckpointSummary{
		Summary:         summary,
		DisplayMetadata: displayMetadataMap(display),
		RawValueExposed: false,
	}
}

func displayMetadataSummary(raw json.RawMessage) string {
	metadata := displayMetadataMap(raw)
	if value, ok := metadata["summary"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func displayMetadataMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

// CheckpointValueSummary renders a bounded display value and hides opaque cursor tokens.
func CheckpointValueSummary(checkpointKind string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if checkpointKind == model.IncrementalCheckpointKindCursor.String() {
		return "redacted cursor"
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "redacted"
	}
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"value", "cursor"} {
			if rawValue, ok := v[key]; ok {
				return compactDisplayValue(rawValue)
			}
		}
	case string:
		return compactDisplayValue(v)
	case json.Number, bool:
		return fmt.Sprint(v)
	}
	return "redacted"
}

func compactDisplayValue(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		return ""
	}
	if ts, err := time.Parse(time.RFC3339, text); err == nil {
		utc := ts.UTC()
		if utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0 {
			return utc.Format(time.DateOnly)
		}
		return utc.Format(time.RFC3339)
	}
	if len(text) > 80 {
		return text[:77] + "..."
	}
	return text
}

// incrementalReplaySafety exposes immediate replay blockers for failed sink
// runs. Broader sibling-sink replay validation still happens in Replay().
func incrementalReplaySafety(
	failure *pipelinerun.FailureContext,
	g *pipelinegraph.Graph,
	succeededSinks map[slug.Slug]bool,
) pipelinerun.IncrementalReplaySafety {
	if failure == nil {
		return pipelinerun.IncrementalReplaySafety{Status: "safe"}
	}
	if failure.Side == failureSideSink {
		if blockedSink, blockedReason, blocked := replayUnsafeSiblingSink(g, failure.Step, succeededSinks); blocked {
			return pipelinerun.IncrementalReplaySafety{
				Status:        checkpointStatusBlocked,
				BlockedSink:   blockedSink.String(),
				BlockedReason: blockedReason,
			}
		}
		return pipelinerun.IncrementalReplaySafety{
			Status:        checkpointStatusBlocked,
			BlockedSink:   failure.Step.String(),
			BlockedReason: failure.Error,
		}
	}
	if g == nil {
		return pipelinerun.IncrementalReplaySafety{Status: "safe"}
	}
	return pipelinerun.IncrementalReplaySafety{Status: "safe"}
}

func replayUnsafeSiblingSink(
	g *pipelinegraph.Graph,
	failedSink slug.Slug,
	succeededSinks map[slug.Slug]bool,
) (slug.Slug, string, bool) {
	if g == nil {
		return "", "", false
	}
	failedInput, ok := g.SinkInputStepSlug(failedSink)
	if !ok {
		return "", "", false
	}

	sinkSlugs := make([]slug.Slug, 0, len(succeededSinks))
	for sinkSlug := range succeededSinks {
		sinkSlugs = append(sinkSlugs, sinkSlug)
	}
	sort.Slice(sinkSlugs, func(i, j int) bool {
		return sinkSlugs[i] < sinkSlugs[j]
	})
	for _, sinkSlug := range sinkSlugs {
		if sinkSlug == failedSink {
			continue
		}
		siblingInput, ok := g.SinkInputStepSlug(sinkSlug)
		if !ok || siblingInput != failedInput {
			continue
		}
		step, ok := g.Step(sinkSlug)
		if !ok {
			continue
		}
		class := sinkIdempotencyClassForStep(step)
		if !class.IsReplaySafe() {
			return sinkSlug, fmt.Sprintf(
				"successful sibling sink %q has idempotency_class %q after sink %q failed",
				sinkSlug,
				class,
				failedSink,
			), true
		}
	}
	return "", "", false
}

// checkpointTimelineEvents records the post-sink checkpoint decision beside the
// existing execution timeline so detail pages can show commit/block points.
func checkpointTimelineEvents(
	at time.Time,
	transitions []pipelinerun.IncrementalCheckpointTransition,
	diagnostics pipelinerun.IncrementalRunDiagnostics,
) []pipelinerun.TimelineEvent {
	statusBySource := make(map[string]string, len(diagnostics.Sources))
	for _, source := range diagnostics.Sources {
		statusBySource[source.SourceStepSlug] = source.CommitResult.Status
	}
	events := make([]pipelinerun.TimelineEvent, 0, len(transitions))
	for _, transition := range transitions {
		status := statusBySource[transition.SourceStepSlug.String()]
		if status == "" {
			status = "committed"
		}
		eventType := "checkpoint_committed"
		if status == checkpointStatusBlocked {
			eventType = "checkpoint_blocked"
		}
		if status == checkpointStatusSkipped {
			eventType = "checkpoint_skipped"
		}
		events = append(events, pipelinerun.TimelineEvent{
			At:     at,
			Type:   eventType,
			Step:   transition.SourceStepSlug,
			Status: status,
		})
	}
	return events
}

// incrementalCheckpointTransitionFromObservation creates the default committed
// transition; commit gating may later downgrade it to blocked.
func incrementalCheckpointTransitionFromObservation(
	runSlugValue string,
	lookup pipelinerun.IncrementalCheckpointLookup,
	observation *pipelinerun.IncrementalCheckpointObservation,
) pipelinerun.IncrementalCheckpointTransition {
	return pipelinerun.IncrementalCheckpointTransition{
		Status:              pipelinerun.IncrementalCheckpointTransitionCommitted,
		Lookup:              lookup,
		SourceStepSlug:      observation.SourceStepSlug,
		CheckpointNamespace: observation.CheckpointNamespace,
		CheckpointKind:      observation.CheckpointKind,
		DataSourceSlug:      lookup.DataSourceSlug,
		OperationType:       lookup.OperationType,
		OperationRef:        lookup.OperationRef,
		OperationInlineHash: lookup.OperationInlineHash,
		OperationIdentity:   lookup.OperationIdentity,
		ObservedLowerBound:  observation.ObservedLowerBound,
		ObservedUpperBound:  observation.ObservedUpperBound,
		DisplayMetadata:     observation.DisplayMetadata,
		ObservedRunSlug:     runSlugValue,
		CommittingRunSlug:   runSlugValue,
		ObservedAt:          observation.ObservedAt,
	}
}

// incrementalCheckpointBlocker returns the dependent sink that prevents a source
// checkpoint from committing, if any.
func incrementalCheckpointBlocker(
	g *pipelinegraph.Graph,
	sourceSlug slug.Slug,
	succeededSinks map[slug.Slug]bool,
	failedSinks map[slug.Slug]error,
	failure *pipelinerun.FailureContext,
) (slug.Slug, string, bool) {
	dependentSinks := dependentSinkSteps(g, sourceSlug)
	for _, sinkSlug := range dependentSinks {
		if sinkErr, failed := failedSinks[sinkSlug]; failed {
			return sinkSlug, sinkErr.Error(), true
		}
	}
	for _, sinkSlug := range dependentSinks {
		if !succeededSinks[sinkSlug] {
			if failure != nil {
				return failure.Step, failure.Error, true
			}
			return sinkSlug, "dependent sink did not complete", true
		}
	}
	return "", "", false
}

// dependentSinkSteps returns all terminal sink steps reachable from a source
// through the normalized pipeline data-flow graph.
func dependentSinkSteps(g *pipelinegraph.Graph, sourceSlug slug.Slug) []slug.Slug {
	if g == nil {
		return nil
	}

	edgesBySource := make(map[slug.Slug][]pipelinegraph.Edge)
	for _, edge := range g.Edges() {
		edgesBySource[edge.FromStepSlug] = append(edgesBySource[edge.FromStepSlug], edge)
	}

	var sinks []slug.Slug
	seen := map[slug.Slug]bool{sourceSlug: true}
	queue := []slug.Slug{sourceSlug}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edgesBySource[current] {
			if seen[edge.ToStepSlug] {
				continue
			}
			seen[edge.ToStepSlug] = true
			step, ok := g.Step(edge.ToStepSlug)
			if ok && step.Sink != nil {
				sinks = append(sinks, edge.ToStepSlug)
			}
			queue = append(queue, edge.ToStepSlug)
		}
	}
	sort.Slice(sinks, func(i, j int) bool {
		return sinks[i] < sinks[j]
	})
	return sinks
}

func sinkIdempotencyClassForStep(step pipelinespec.Step) pipelinespec.SinkIdempotencyClass {
	if step.Sink == nil ||
		step.Sink.IdempotencyClass == pipelinespec.SinkIdempotencyClassUnknown {
		// Validation rejects an omitted declaration. Keep the runtime fallback
		// conservative so a malformed persisted spec can never gain retry safety.
		return pipelinespec.SinkIdempotencyClassUnsafe
	}
	return step.Sink.IdempotencyClass
}

func sinkIdempotencyKey(
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	pipelineVersionID int64,
	effectIdentity string,
	sinkSlug slug.Slug,
	g *pipelinegraph.Graph,
	observations map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation,
) (string, error) {
	sourceWindows := upstreamIncrementalSourceWindows(g, sinkSlug, observations)
	payload := struct {
		OrganizationSlug  string                                         `json:"organization_slug"`
		ProjectSlug       string                                         `json:"project_slug"`
		PipelineSlug      string                                         `json:"pipeline_slug"`
		PipelineVersionID int64                                          `json:"pipeline_version_id"`
		EffectIdentity    string                                         `json:"effect_identity"`
		SinkStepSlug      string                                         `json:"sink_step_slug"`
		SourceWindows     map[string]pipelinerun.IncrementalSourceWindow `json:"source_windows"`
	}{
		OrganizationSlug:  orgSlug.String(),
		ProjectSlug:       projectSlug.String(),
		PipelineSlug:      pipelineSlug.String(),
		PipelineVersionID: pipelineVersionID,
		EffectIdentity:    effectIdentity,
		SinkStepSlug:      sinkSlug.String(),
		SourceWindows:     sourceWindows,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	sum := sha256.Sum256(data)
	return "sink:v1:" + hex.EncodeToString(sum[:]), nil
}

// Effect identity stays at the root while retry metadata links the preceding attempt.
func sinkReplayIdentity(runSlugValue string, replayOfRunSlug *string, source pipelinerun.RunSourceSummary) string {
	if source.AttemptNumber > 1 && source.RootRunSlug != "" {
		return source.RootRunSlug.String()
	}
	if replayOfRunSlug != nil && strings.TrimSpace(*replayOfRunSlug) != "" {
		return strings.TrimSpace(*replayOfRunSlug)
	}
	return runSlugValue
}

func upstreamIncrementalSourceWindows(
	g *pipelinegraph.Graph,
	sinkSlug slug.Slug,
	observations map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation,
) map[string]pipelinerun.IncrementalSourceWindow {
	if g == nil || len(observations) == 0 {
		return map[string]pipelinerun.IncrementalSourceWindow{}
	}

	edgesByTarget := make(map[slug.Slug][]pipelinegraph.Edge)
	for _, edge := range g.Edges() {
		edgesByTarget[edge.ToStepSlug] = append(edgesByTarget[edge.ToStepSlug], edge)
	}

	windows := make(map[string]pipelinerun.IncrementalSourceWindow)
	seen := map[slug.Slug]bool{sinkSlug: true}
	queue := []slug.Slug{sinkSlug}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range edgesByTarget[current] {
			if seen[edge.FromStepSlug] {
				continue
			}
			seen[edge.FromStepSlug] = true
			if observation := observations[edge.FromStepSlug]; observation != nil {
				windows[edge.FromStepSlug.String()] = pipelinerun.IncrementalSourceWindow{
					LowerBound:      cloneRawMessage(observation.ObservedLowerBound),
					UpperBound:      cloneRawMessage(observation.ObservedUpperBound),
					DisplayMetadata: cloneRawMessage(observation.DisplayMetadata),
				}
			}
			queue = append(queue, edge.FromStepSlug)
		}
	}

	return windows
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func incrementalSourceWindowsFromObservations(
	observations map[slug.Slug]*pipelinerun.IncrementalCheckpointObservation,
) map[slug.Slug]pipelinerun.IncrementalSourceWindow {
	if len(observations) == 0 {
		return nil
	}

	windows := make(map[slug.Slug]pipelinerun.IncrementalSourceWindow, len(observations))
	for sourceSlug, observation := range observations {
		if observation == nil {
			continue
		}
		windows[sourceSlug] = pipelinerun.IncrementalSourceWindow{
			LowerBound:      cloneRawMessage(observation.ObservedLowerBound),
			UpperBound:      cloneRawMessage(observation.ObservedUpperBound),
			DisplayMetadata: cloneRawMessage(observation.DisplayMetadata),
		}
	}
	return windows
}

func cloneIncrementalSourceWindows(
	windows map[slug.Slug]pipelinerun.IncrementalSourceWindow,
) map[slug.Slug]pipelinerun.IncrementalSourceWindow {
	if len(windows) == 0 {
		return nil
	}

	cloned := make(map[slug.Slug]pipelinerun.IncrementalSourceWindow, len(windows))
	for sourceSlug, window := range windows {
		cloned[sourceSlug] = pipelinerun.IncrementalSourceWindow{
			LowerBound:      cloneRawMessage(window.LowerBound),
			UpperBound:      cloneRawMessage(window.UpperBound),
			DisplayMetadata: cloneRawMessage(window.DisplayMetadata),
		}
	}
	return cloned
}

func sinkConfigForStep(step pipelinespec.Step) map[string]any {
	if step.Sink == nil || len(step.Sink.Config) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(step.Sink.Config))
	for key, value := range step.Sink.Config {
		cloned[key] = value
	}

	return cloned
}

func resolveSinkUpstreamStepSlug(pipeline *pipelinespec.Pipeline, stepIndex int) (slug.Slug, error) {
	if pipeline == nil {
		return "", ErrNilPipeline
	}
	if stepIndex < 0 || stepIndex >= len(pipeline.Steps) {
		return "", ErrNilPipeline
	}

	step := pipeline.Steps[stepIndex]
	normalized, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return "", ErrSinkUpstreamRequired.With(err).Detail(fmt.Sprintf("for %q", step.Slug))
	}
	return resolveSinkUpstreamStepSlugFromGraph(normalized, step.Slug)
}

func resolveSinkUpstreamStepSlugFromGraph(
	normalized *pipelinegraph.Graph,
	stepSlug slug.Slug,
) (slug.Slug, error) {
	if normalized == nil {
		return "", ErrNilPipeline
	}

	upstreamStepSlug, ok := normalized.SinkInputStepSlug(stepSlug)
	if !ok {
		return "", ErrSinkUpstreamRequired.Detailf("for %q", stepSlug)
	}
	return upstreamStepSlug, nil
}

func replaySnapshotsFromResults(
	pipeline *pipelinespec.Pipeline,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	results map[slug.Slug]*pipelinerun.StepResult,
) []pipelinerun.StepSnapshot {
	if pipeline == nil || len(results) == 0 {
		return nil
	}

	snapshots := make([]pipelinerun.StepSnapshot, 0, len(results))
	for _, step := range pipeline.Steps {
		stepResult, ok := results[step.Slug]
		if !ok {
			continue
		}

		snapshot := pipelinerun.StepSnapshot{
			Step: step.Slug,
			Kind: step.Kind,
			Input: pipelinerun.StepInputSnapshot{
				OrganizationSlug: orgSlug,
				ProjectSlug:      projectSlug,
				EnvironmentSlug:  environmentSlug,
			},
			Output: snapshotOutput(stepResult),
		}

		if step.Query != nil {
			snapshot.Input.DataSourceSlug = step.Query.DataSourceSlug
			snapshot.Input.OperationType = step.Query.OperationType
			snapshot.Input.OperationRef = step.Query.OperationRef
			snapshot.Input.UsesInline = len(step.Query.OperationInline) > 0
			snapshot.Input.ParameterKeys = sortedKeys(step.Query.Parameters)
		}

		snapshots = append(snapshots, snapshot)
	}

	return snapshots
}

func queryOperationInlineForStep(step pipelinespec.Step) map[string]any {
	if step.Query == nil || len(step.Query.OperationInline) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(step.Query.OperationInline))
	for key, value := range step.Query.OperationInline {
		cloned[key] = value
	}

	return cloned
}

func resolveQueryOperationForStep(step pipelinespec.Step) (slug.Slug, map[string]any, bool, error) {
	if step.Query == nil {
		return "", nil, false, xerror.Unexpected().Detail(
			"query step is missing query configuration",
		)
	}

	queryOperationInline := queryOperationInlineForStep(step)
	hasOperationRef := step.Query.OperationRef != ""
	hasOperationInline := len(queryOperationInline) > 0

	if hasOperationRef == hasOperationInline {
		return "", nil, false, xerror.Unexpected().Detailf(
			"query step %q must set exactly one of operation_ref or operation_inline",
			step.Slug,
		)
	}

	if hasOperationRef {
		return step.Query.OperationRef, nil, false, nil
	}

	return "", queryOperationInline, true, nil
}

func cloneStepResults(
	results map[slug.Slug]*pipelinerun.StepResult,
) map[slug.Slug]*pipelinerun.StepResult {
	if len(results) == 0 {
		return map[slug.Slug]*pipelinerun.StepResult{}
	}

	cloned := make(map[slug.Slug]*pipelinerun.StepResult, len(results))
	for stepSlug, stepResult := range results {
		cloned[stepSlug] = stepResult.Clone()
	}

	return cloned
}

func snapshotOutput(stepResult *pipelinerun.StepResult) *pipelinerun.StepOutputSnapshot {
	if stepResult == nil {
		return nil
	}

	output := &pipelinerun.StepOutputSnapshot{
		ResultType: stepResult.ResultTypeString(),
		VendorType: stepResult.VendorType(),
		DurationMs: stepResult.DurationMs(),
		Details: &pipelinerun.StepOutputDetails{
			Kind: pipelinerun.StepOutputDetailKindUnknown,
		},
	}
	if stepResult.IsRunInput() {
		return output
	}

	if transformOutput, ok := stepResult.Transform(); ok {
		rowCount := transformOutput.RowCount
		output.Details = &pipelinerun.StepOutputDetails{
			Kind:          pipelinerun.StepOutputDetailKindRows,
			RowCount:      &rowCount,
			IsHeadlessCSV: transformOutput.IsHeadlessCSV,
		}
		if len(transformOutput.ColumnNames) > 0 {
			output.Details.ColumnNames = append([]string(nil), transformOutput.ColumnNames...)
		}

		return output
	}

	switch stepResult.Type() {
	case result.ExecutionResultTypeUnknown:
		return output
	case result.ExecutionResultTypeSQL:
		sqlResult, ok := stepResult.SQL()
		if !ok {
			return output
		}

		rowCount := sqlResult.RowCount
		output.Details = &pipelinerun.StepOutputDetails{
			Kind:     pipelinerun.StepOutputDetailKindRows,
			RowCount: &rowCount,
		}
		if len(sqlResult.ColumnNames) > 0 {
			output.Details.ColumnNames = append([]string(nil), sqlResult.ColumnNames...)
		}
	case result.ExecutionResultTypeMongoDB:
		mongoResult, ok := stepResult.MongoDB()
		if !ok {
			return output
		}

		output.OperationType = mongoResult.OperationType
		output.Details = &pipelinerun.StepOutputDetails{
			Kind: pipelinerun.StepOutputDetailKindDocuments,
		}
		// Prefer the connector's explicit count; otherwise use a reported Documents slice,
		// including an empty-but-non-nil slice, without inventing counts for write results.
		if mongoResult.DocumentCount != nil {
			documentCount := *mongoResult.DocumentCount
			output.Details.DocumentCount = &documentCount
		} else if mongoResult.Documents != nil {
			documentCount := len(mongoResult.Documents)
			output.Details.DocumentCount = &documentCount
		}
	case result.ExecutionResultTypeHTTP:
		httpResult, ok := stepResult.HTTP()
		if !ok {
			return output
		}

		statusCode := httpResult.StatusCode
		output.Details = &pipelinerun.StepOutputDetails{
			Kind:       pipelinerun.StepOutputDetailKindHTTPResponse,
			StatusCode: &statusCode,
			File:       filepayload.CloneFilePayloadMetadata(httpResult.File),
		}
	case result.ExecutionResultTypeObjectRows:
		objectRowsResult, ok := stepResult.ObjectRows()
		if !ok {
			return output
		}

		rowCount := objectRowsResult.RowCount
		output.OperationType = strings.TrimSpace(objectRowsResult.Operation)
		output.Details = &pipelinerun.StepOutputDetails{
			Kind:     pipelinerun.StepOutputDetailKindRows,
			RowCount: &rowCount,
		}
		if len(objectRowsResult.ColumnNames) > 0 {
			output.Details.ColumnNames = append([]string(nil), objectRowsResult.ColumnNames...)
		}
	case result.ExecutionResultTypeFTP:
		ftpResult, ok := stepResult.FTP()
		if !ok {
			return output
		}

		applyFileOutputSnapshot(output, ftpResult)
	case result.ExecutionResultTypeGit:
		gitResult, ok := stepResult.Git()
		if !ok {
			return output
		}

		applyFileOutputSnapshot(output, gitResult)
	case result.ExecutionResultTypeWorkspaceFile:
		file, ok := stepResult.File()
		if !ok {
			return output
		}
		applyFileOutputSnapshot(output, file)
	case result.ExecutionResultTypeS3:
		s3Result, ok := stepResult.S3()
		if !ok {
			return output
		}

		applyFileOutputSnapshot(output, s3Result)
	default:
		return output
	}

	return output
}

// applyFileOutputSnapshot copies safe file/object connector metadata into run detail.
func applyFileOutputSnapshot(output *pipelinerun.StepOutputSnapshot, fileResult *result.FileResult) {
	if output == nil || fileResult == nil {
		return
	}

	operation := strings.TrimSpace(fileResult.Operation)
	output.OperationType = operation
	targetPath := strings.TrimSpace(fileResult.TargetPath)
	if operation == "write" || operation == "create" || operation == "update" {
		output.Details = &pipelinerun.StepOutputDetails{
			Kind:         pipelinerun.StepOutputDetailKindFileWrite,
			File:         filepayload.CloneFilePayloadMetadata(fileResult.File),
			TargetPath:   &targetPath,
			WrittenRows:  cloneOptionalInt(fileResult.WrittenRows),
			WrittenBytes: cloneOptionalInt(fileResult.WrittenBytes),
		}
		return
	}

	output.Details = &pipelinerun.StepOutputDetails{
		Kind:          pipelinerun.StepOutputDetailKindFileRead,
		IsHeadlessCSV: fileResult.IsHeadlessCSV,
		File:          filepayload.CloneFilePayloadMetadata(fileResult.File),
	}
	if targetPath != "" {
		output.Details.TargetPath = &targetPath
	}

	if !strings.EqualFold(strings.TrimSpace(fileResult.Format), "file") {
		if len(fileResult.ColumnNames) > 0 {
			output.Details.ColumnNames = append([]string(nil), fileResult.ColumnNames...)
		}

		rowCount := fileResult.RowCount
		output.Details.RowCount = &rowCount
	}
}

func resolvedNamedInputContracts(transform *pipelinespec.TransformSpec) map[string]*payload.Contract {
	if transform.InputContracts == nil {
		return nil
	}
	contracts := make(map[string]*payload.Contract, len(transform.InputContracts))
	for name, attachment := range transform.InputContracts {
		if attachment != nil {
			contracts[name] = attachment.Contract
		}
	}
	return contracts
}

func validateCompositionProducerPayload(runtimePayload *payload.Payload, contract *payload.Contract) error {
	if runtimePayload == nil || contract == nil || runtimePayload.Representation == model.ContractRepresentationFile || runtimePayload.Representation == model.ContractRepresentationMultipartForm {
		return nil
	}
	_, rows, err := payload.ContractData(runtimePayload)
	if err != nil {
		return err
	}
	if runtimePayload.Representation == model.ContractRepresentationCSV {
		rows = payload.CoerceContractFieldValidationRows(contract, rows)
	}
	if err := transformsql.ValidateOutputContract(&payload.Output{OutputKind: model.TransformOutputKindRows, Rows: rows}, contract); err != nil {
		return xerror.Join(ErrQueryOutputContractSchemaMismatch, err)
	}
	return nil
}
