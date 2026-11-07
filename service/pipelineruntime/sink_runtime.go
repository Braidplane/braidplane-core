package pipelineruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	domainvalidate "github.com/Braidplane/braidplane-core/pkg/structvalidate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

var errSinkConfigurationInvalid = writegate.ErrPreparationFailed

var errSinkNotConfigured = xerror.Unavailable(xerror.Pipeline)

var errUnsupportedSinkBackend = xerror.Unsupported(xerror.DataSource)

var errSinkReadOnly = xerror.DataSourceReadOnly()

var errSinkReplayBlocked = xerror.ReplayUnsafe()

const sinkReplayDecisionDiagnosticCode = "SINK_REPLAY_DECISION"

type sinkQueryExecutor interface {
	Execute(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
}

type sinkRuntime struct {
	queryExecutor sinkQueryExecutor
	effectLedger  pipelinerun.SinkEffectLedger
}

type fileWriteSinkConfig struct {
	Path      *string
	Format    *string
	Delimiter *string
	HasHeader *bool
	Overwrite *bool
	Sheet     *string
	Range     *string
}

type ftpSinkConfig struct {
	fileWriteSinkConfig
}

type gitSinkConfig struct {
	fileWriteSinkConfig
	Branch  *string
	Message *string
}

type s3SinkConfig struct {
	fileWriteSinkConfig
	Bucket      *string
	Key         *string
	ContentType *string
	Metadata    map[string]string
}

type sinkPayloadBinding struct {
	name           string
	value          any
	representation model.ContractRepresentation
	file           *filepayload.FilePayloadMetadata
}

// NewSinkRuntime creates a pipeline sink runtime backed by saved or inline sink-query execution.
// The optional durable ledger is required in production and remains optional for
// hermetic connector tests that do not exercise retry behavior.
func NewSinkRuntime(
	queryExec sinkQueryExecutor,
	effectLedgers ...pipelinerun.SinkEffectLedger,
) SinkRuntime {
	var effectLedger pipelinerun.SinkEffectLedger
	if len(effectLedgers) > 0 {
		effectLedger = effectLedgers[0]
	}
	return &sinkRuntime{queryExecutor: queryExec, effectLedger: effectLedger}
}

func (s *sinkRuntime) Execute(ctx context.Context, input *SinkRuntimeInput) error {
	if input != nil {
		input.DeliveryOutcome = "failed"
	}
	if input == nil {
		return errSinkNotConfigured.Detail("runtime input is required")
	}
	ctx = pipelineruntimevars.WithSinkExecution(ctx)

	if err := validateReservedSinkConfigBindings(input.SinkConfig); err != nil {
		return err
	}
	metadata := map[string]any{}
	addSinkRuntimeMetadataParams(metadata, input)
	config, err := pipelineruntimevars.ResolveSinkValue(input.SinkConfig, metadata)
	if err != nil {
		return err
	}
	input.SinkConfig = config.(map[string]any)

	if input.SinkDataSourceReadOnly {
		return errSinkReadOnly.Detailf("sink data source %s", input.SinkDataSourceSlug)
	}

	if input.OperationSnapshot != nil {
		executor, ok := s.queryExecutor.(snapshotQueryExecutor)
		if !ok {
			return errSinkNotConfigured.Detail("immutable operation executor is required")
		}
		execution := *s
		execution.queryExecutor = capturedSinkExecutor{executor: executor, content: *input.OperationSnapshot}
		s = &execution
	}
	backend := input.SinkBackend
	if backend == model.SinkBackendUnknown {
		return errSinkNotConfigured.Detail("sink backend is unknown")
	}
	if !domainvalidate.ValidSinkBackend(backend) {
		return errUnsupportedSinkBackend.Detailf("%s", backend.String())
	}
	if err := validateSinkBackendPayloadSupport(backend, input); err != nil {
		return err
	}
	if backend == model.SinkBackendS3 {
		if err := pipelinevalidate.S3SinkConfig(input.SinkConfig); err != nil {
			return errSinkConfigurationInvalid.With(err).WithPublicDetail(xerror.PublicDetail(err))
		}
	}
	if input.EmptyInputPolicy != "" && input.EmptyInputPolicy != "skip" && input.EmptyInputPolicy != "reject" {
		return errSinkNotConfigured.Detail("empty_input_policy must be skip or reject")
	}
	if payload, ok := input.SourceResult.Payload(); ok && payload.Representation == model.ContractRepresentationRows && payload.RowCount == 0 {
		if input.EmptyInputPolicy == "reject" {
			return xerror.InvalidInput(xerror.Pipeline).Detail("sink input has zero rows; empty_input_policy rejects unexpected empty data")
		}
		if input.EmptyInputPolicy == "skip" || backend == model.SinkBackendDB {
			input.ReplayDecision = "skip_empty_input"
			input.DeliveryOutcome = "no_op"
			return nil
		}
	}

	var reachedWrite, skipWrite bool
	var claimErr error
	if backend == model.SinkBackendS3 {
		ctx = writegate.WithBeforeWrite(ctx, func(ctx context.Context) error {
			reachedWrite = true
			skipWrite, claimErr = s.beginEffect(ctx, input)
			if claimErr != nil {
				return claimErr
			}
			if skipWrite {
				return writegate.ErrSkipped
			}
			input.DeliveryOutcome = "unknown"
			return nil
		})
	} else {
		if skip, err := s.beginEffect(ctx, input); err != nil || skip {
			return err
		}
		input.DeliveryOutcome = "unknown"
	}
	var execErr error
	switch backend {
	case model.SinkBackendFTP:
		execErr = s.executeFTPSink(ctx, input)
	case model.SinkBackendGit:
		execErr = s.executeGitSink(ctx, input)
	case model.SinkBackendS3:
		execErr = s.executeS3Sink(ctx, input)
	case model.SinkBackendDB:
		execErr = s.executeDBSink(ctx, input)
	case model.SinkBackendHTTP:
		execErr = s.executeHTTPSink(ctx, input)
	case model.SinkBackendMongo:
		execErr = s.executeMongoSink(ctx, input)
	default:
		execErr = errUnsupportedSinkBackend.Detailf("%s", backend)
	}
	if backend == model.SinkBackendS3 {
		if claimErr != nil {
			return claimErr
		}
		if skipWrite {
			return nil
		}
		if !reachedWrite {
			// Keep established credential, scope and connection causes; local preparation has no delivery claim.
			if execErr != nil && sourceFailureReasonCode(execErr) != pipelinerun.FailureReasonCodeSourceQueryFailed {
				return execErr
			}
			return errSinkConfigurationInvalid.With(execErr).WithPublicDetail("S3 sink preparation failed before provider dispatch. Review the selected operation fields and payload format.")
		}
	}
	if execErr != nil {
		return execErr
	}
	if err := s.markEffectSucceeded(ctx, input); err != nil {
		return err
	}
	input.DeliveryOutcome = "confirmed"
	return nil
}

// beginEffect writes the durable pre-dispatch claim and applies the declared
// replay strategy before a connector can cause an external side effect.
func (s *sinkRuntime) beginEffect(ctx context.Context, input *SinkRuntimeInput) (bool, error) {
	if input == nil || s.effectLedger == nil {
		return false, nil
	}

	input.ReplayDecision = "claim_effect_attempt"
	attempt := sinkEffectAttempt(input)
	effect, err := s.effectLedger.BeginOrGet(
		ctx,
		input.OrgSlug,
		input.ProjectSlug,
		input.EnvironmentSlug,
		attempt,
	)
	if err != nil {
		return false, xerror.Wrapf("begin durable sink effect: %w", err)
	}
	if effect == nil {
		return false, xerror.Unexpected().Detail("begin durable sink effect: empty effect")
	}
	input.EffectState = effect.State
	input.EffectStartedAt = effect.StartedAt
	if !effect.Claimed {
		input.DeliveryOutcome = "unknown"
		if effect.State == pipelinerun.SinkEffectStateSucceeded && input.IdempotencyClass.IsReplaySafe() {
			input.DeliveryOutcome = "confirmed"
			input.ReplayDecision = "skip_ledger_succeeded"
			return true, nil
		}
	}
	if effect.Claimed {
		input.ReplayDecision = "execute_first_attempt"
		return false, nil
	}

	switch input.IdempotencyClass {
	case pipelinespec.SinkIdempotencyClassNaturallyIdempotent:
		input.ReplayDecision = "execute_naturally_idempotent_replay"
		return false, nil
	case pipelinespec.SinkIdempotencyClassIdempotentWithKey:
		input.ReplayDecision = "execute_idempotency_key_replay"
		return false, nil
	case pipelinespec.SinkIdempotencyClassDeduplicatedByLedger:
		if effect.State == pipelinerun.SinkEffectStateSucceeded {
			input.ReplayDecision = "skip_ledger_succeeded"
			return true, nil
		}
		input.ReplayDecision = "blocked_ledger_outcome_unknown"
	case pipelinespec.SinkIdempotencyClassUnsafe:
		input.ReplayDecision = "blocked_unsafe_replay"
	default:
		input.ReplayDecision = "blocked_unknown_replay_strategy"
	}

	return false, errSinkReplayBlocked.Detailf(
		"sink step %q replay is blocked because a prior effect attempt is %s",
		input.SinkStepSlug,
		effect.State,
	)
}

// markEffectSucceeded closes the durable claim only after the connector has
// returned success. A persistence failure deliberately leaves an unknown
// attempt behind so ledger-guarded and unsafe retries cannot duplicate it.
func (s *sinkRuntime) markEffectSucceeded(ctx context.Context, input *SinkRuntimeInput) error {
	if input == nil || s.effectLedger == nil {
		return nil
	}
	// Safe replays may repeat the provider write, but the first completed ledger
	// record is immutable and must not be transitioned to succeeded again.
	if input.EffectState == pipelinerun.SinkEffectStateSucceeded {
		return nil
	}
	if err := s.effectLedger.MarkSucceeded(
		ctx,
		input.OrgSlug,
		input.ProjectSlug,
		input.EnvironmentSlug,
		sinkEffectAttempt(input),
	); err != nil {
		return xerror.Wrapf("mark durable sink effect succeeded: %w", err)
	}
	input.EffectState = pipelinerun.SinkEffectStateSucceeded
	return nil
}

// sinkEffectAttempt maps runtime-only fields into the safe durable ledger shape.
func sinkEffectAttempt(input *SinkRuntimeInput) pipelinerun.SinkEffectAttempt {
	return pipelinerun.SinkEffectAttempt{
		ReplayIdentity:    input.ReplayIdentity,
		SinkStepSlug:      input.SinkStepSlug,
		IdempotencyKey:    input.IdempotencyKey,
		IdempotencyClass:  input.IdempotencyClass.String(),
		ReplayMethod:      sinkReplayMethod(input.IdempotencyClass),
		ReplayDecision:    input.ReplayDecision,
		DiagnosticCode:    sinkReplayDecisionDiagnosticCode,
		DiagnosticSummary: input.ReplayDecision,
	}
}

// sinkReplayMethod provides an operator-safe label without exposing the key.
func sinkReplayMethod(class pipelinespec.SinkIdempotencyClass) string {
	switch class {
	case pipelinespec.SinkIdempotencyClassNaturallyIdempotent:
		return "deterministic_external_write"
	case pipelinespec.SinkIdempotencyClassIdempotentWithKey:
		return "provider_idempotency_key"
	case pipelinespec.SinkIdempotencyClassDeduplicatedByLedger:
		return "durable_effect_ledger"
	default:
		return "unsafe_refuse_after_attempt"
	}
}

// sinkReplayStrategy returns the declared strategy for safe run diagnostics.
func sinkReplayStrategy(input *SinkRuntimeInput) string {
	if input == nil {
		return ""
	}
	return input.IdempotencyClass.String()
}

// sinkReplayMethodForInput returns the mechanism without exposing a key or payload.
func sinkReplayMethodForInput(input *SinkRuntimeInput) string {
	if input == nil {
		return ""
	}
	return sinkReplayMethod(input.IdempotencyClass)
}

// sinkReplayDecision returns the persisted-safe delivery decision.
func sinkReplayDecision(input *SinkRuntimeInput) string {
	if input == nil {
		return ""
	}
	return input.ReplayDecision
}

// sinkEffectState returns the ledger state used to explain a retry decision.
func sinkEffectState(input *SinkRuntimeInput) pipelinerun.SinkEffectState {
	if input == nil {
		return pipelinerun.SinkEffectStateUnknown
	}
	return input.EffectState
}

func (s *sinkRuntime) executeMongoSink(
	ctx context.Context,
	input *SinkRuntimeInput,
) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for mongo backend")
	}
	if input.SinkOperationRef == "" {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for mongo backend")
	}

	binding, err := buildSinkPayloadBinding(input)
	if err != nil {
		return err
	}

	params := make(map[string]any, len(input.SinkConfig)+1)
	for key, value := range input.SinkConfig {
		params[key] = value
	}
	params[binding.name] = binding.value
	addSinkPayloadBindingMetadataParams(params, binding)
	addSinkRuntimeMetadataParams(params, input)

	_, err = s.queryExecutor.Execute(
		ctx,
		input.OrgSlug,
		input.SinkDataSourceSlug,
		input.SinkOperationRef,
		model.SystemEventActor(),
		params,
	)
	if err != nil {
		return xerror.Wrapf("execute mongo sink query: %w", err)
	}

	return nil
}

func (s *sinkRuntime) executeHTTPSink(
	ctx context.Context,
	input *SinkRuntimeInput,
) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for http backend")
	}
	usesInline := len(input.SinkOperationInline) > 0
	if input.SinkOperationRef == "" && !usesInline {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for http backend")
	}
	if usesInline && input.SinkOperationRef != "" {
		return errSinkNotConfigured.Detail("HTTP sink must select exactly one saved or inline operation")
	}

	var binding sinkPayloadBinding
	var err error
	if catalog.SinkPayloadBindingForOperationType(strings.TrimSpace(input.SinkOperationType)) ==
		catalog.SinkPayloadBindingRowObject {
		binding, err = rowObjectSinkPayloadBinding(input)
	} else {
		binding, err = buildSinkPayloadBinding(input)
	}
	if err != nil {
		return err
	}

	params := make(map[string]any, len(input.SinkConfig)+1)
	for key, value := range input.SinkConfig {
		params[key] = value
	}
	params[binding.name] = binding.value
	params[sinkbinding.InternalInputRepresentation] = binding.representation.String()
	addSinkPayloadBindingMetadataParams(params, binding)
	addSinkRuntimeMetadataParams(params, input)
	if strings.TrimSpace(input.SinkOperationType) == catalog.OperationTypeGoogleWorkspace {
		params[sinkbinding.InternalIdempotencyClass] = input.IdempotencyClass.String()
	}

	var execResult *result.ExecutionResult
	if usesInline {
		executor, ok := s.queryExecutor.(interface {
			ExecuteInline(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
		})
		if !ok {
			return errSinkNotConfigured.Detail("HTTP inline sink executor is required")
		}
		execResult, err = executor.ExecuteInline(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationInline, params)
	} else {
		execResult, err = s.queryExecutor.Execute(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationRef, model.SystemEventActor(), params, queriessvc.WithoutExecutionRecording())
	}
	if err != nil {
		return xerror.Wrapf("execute http sink request: %w", err)
	}

	if execResult == nil {
		return xerror.Unexpected().Detail("execute http sink request: empty execution result")
	}

	if execResult.Error() != nil {
		return xerror.Wrapf("execute http sink request: %w", execResult.Error())
	}
	if strings.TrimSpace(input.SinkOperationType) == catalog.OperationTypeGoogleWorkspace {
		if f, ok := execResult.File(); ok && f != nil && (f.Operation == "create" || f.Operation == "update") {
			captureSinkExecutionResult(input, execResult)
			return nil
		}
		if native, ok := execResult.ObjectRows(); ok && native != nil &&
			(native.ObjectType == "document" || native.ObjectType == "presentation") &&
			(native.Operation == "create" || native.Operation == "update") {
			input.SinkResult = execResult.Clone()
			return nil
		}
	}
	resultCapability := catalog.SinkResultForOperationType(
		strings.TrimSpace(input.SinkOperationType),
	)
	if resultCapability.Kind == catalog.SinkResultKindObjectRows {
		if objectResult, ok := execResult.ObjectRows(); !ok || objectResult == nil ||
			strings.TrimSpace(objectResult.Operation) != resultCapability.Operation {
			return xerror.Unexpected().Detail(
				"execute object-row sink: connector returned an unexpected operation result",
			)
		}
		captureSinkExecutionResult(input, execResult)
		return nil
	}

	httpResult, ok := execResult.HTTP()
	if !ok || httpResult == nil {
		return xerror.Unexpected().Detail(
			"execute http sink request: connector did not return HTTP result",
		)
	}

	if httpResult.StatusCode < 200 || httpResult.StatusCode >= 300 {
		status := strings.TrimSpace(strconv.Itoa(httpResult.StatusCode) + " " + http.StatusText(httpResult.StatusCode))
		return xerror.Unexpected().Detailf(
			"http sink returned non-2xx status %d",
			httpResult.StatusCode,
		).WithPublicDetail("HTTP destination returned status " + status + ". Review the destination request settings and verify remote state before retrying.")
	}

	return nil
}

func (s *sinkRuntime) executeDBSink(ctx context.Context, input *SinkRuntimeInput) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for db backend")
	}
	usesInline := len(input.SinkOperationInline) > 0
	if input.SinkOperationRef == "" && !usesInline {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for db backend")
	}
	if input.SinkOperationRef != "" && usesInline {
		return errSinkNotConfigured.Detail("DB sink must select exactly one saved or inline operation")
	}

	binding, err := dbSinkPayloadBinding(input)
	if err != nil {
		return err
	}

	// Merge SinkConfig first so the reserved runtime binding key is always set last and
	// cannot be overridden by caller-supplied config.
	params := make(map[string]any, len(input.SinkConfig)+3)
	for key, value := range input.SinkConfig {
		params[key] = value
	}
	params[binding.name] = binding.value
	addSinkPayloadBindingMetadataParams(params, binding)

	// Populate the native input relation bindings so SQL connectors can materialize
	// FROM input / JOIN input as a typed CTE. All three upstream payload types are
	// normalized to []map[string]any rows here:
	//   - Rows: already tabular, cloned directly.
	//   - JSON: arrays → one row per element; objects → single row. Scalars/null
	//     are rejected by ParseJSONBodyToRows and the bindings are skipped, leaving
	//     the public {{input}} binding as the only available value.
	//   - CSV: first row is the header; each subsequent row becomes a record.
	//     Malformed CSV is rejected and the bindings are skipped in the same way.
	if columns, rows, err := pipelinerun.ContractData(input.SourceResult); err == nil &&
		(len(columns) > 0 || len(rows) > 0) {
		params[sinkbinding.InternalInputRows] = cloneSinkInputRows(rows)
		params[sinkbinding.InternalInputColumns] = append([]string(nil), columns...)
	}
	addSinkRuntimeMetadataParams(params, input)

	var execResult *result.ExecutionResult
	if usesInline {
		executor, ok := s.queryExecutor.(interface {
			ExecuteInline(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
		})
		if !ok {
			return errSinkNotConfigured.Detail("DB inline sink executor is required")
		}
		execResult, err = executor.ExecuteInline(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationInline, params)
	} else {
		execResult, err = s.queryExecutor.Execute(
			ctx,
			input.OrgSlug,
			input.SinkDataSourceSlug,
			input.SinkOperationRef,
			model.SystemEventActor(),
			params,
		)
	}
	if err != nil {
		return xerror.Wrapf("execute db sink query: %w", err)
	}

	captureSinkExecutionResult(input, execResult)
	return nil
}

func (s *sinkRuntime) executeFTPSink(ctx context.Context, input *SinkRuntimeInput) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for ftp backend")
	}

	config, err := decodeFTPSinkConfig(input.SinkConfig)
	if err != nil {
		return err
	}

	if config.Path == nil || strings.TrimSpace(*config.Path) == "" {
		return errSinkNotConfigured.Detail("sink.config.path is required for ftp backend")
	}
	if input.SinkOperationRef == "" {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for ftp backend")
	}

	binding, err := buildSinkPayloadBinding(input)
	if err != nil {
		return err
	}

	params := map[string]any{binding.name: binding.value}
	addSinkPayloadBindingMetadataParams(params, binding)
	params["path"] = *config.Path
	if config.Format != nil {
		params["format"] = *config.Format
	}
	if config.Delimiter != nil {
		params["delimiter"] = *config.Delimiter
	}
	if config.HasHeader != nil {
		params["has_header"] = *config.HasHeader
	}
	if config.Overwrite != nil {
		params["overwrite"] = *config.Overwrite
	}
	if config.Sheet != nil {
		params["sheet"] = *config.Sheet
	}
	if config.Range != nil {
		params["range"] = *config.Range
	}
	addSinkRuntimeMetadataParams(params, input)

	execResult, err := s.queryExecutor.Execute(
		ctx,
		input.OrgSlug,
		input.SinkDataSourceSlug,
		input.SinkOperationRef,
		model.SystemEventActor(),
		params,
	)
	captureSinkExecutionResult(input, execResult)
	if err != nil {
		return xerror.Wrapf("execute ftp sink query: %w", err)
	}

	return nil
}

func (s *sinkRuntime) executeGitSink(ctx context.Context, input *SinkRuntimeInput) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for git backend")
	}

	config, err := decodeGitSinkConfig(input.SinkConfig)
	if err != nil {
		return err
	}

	if config.Path == nil || strings.TrimSpace(*config.Path) == "" {
		return errSinkNotConfigured.Detail("sink.config.path is required for git backend")
	}
	if input.SinkOperationRef == "" {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for git backend")
	}

	binding, err := buildSinkPayloadBinding(input)
	if err != nil {
		return err
	}

	params := map[string]any{binding.name: binding.value}
	addSinkPayloadBindingMetadataParams(params, binding)
	params["path"] = *config.Path
	if config.Format != nil {
		params["format"] = *config.Format
	}
	if config.Delimiter != nil {
		params["delimiter"] = *config.Delimiter
	}
	if config.HasHeader != nil {
		params["has_header"] = *config.HasHeader
	}
	if config.Overwrite != nil {
		params["overwrite"] = *config.Overwrite
	}
	if config.Sheet != nil {
		params["sheet"] = *config.Sheet
	}
	if config.Range != nil {
		params["range"] = *config.Range
	}
	if config.Branch != nil {
		params["branch"] = *config.Branch
	}
	if config.Message != nil {
		params["message"] = *config.Message
	}
	addSinkRuntimeMetadataParams(params, input)

	execResult, err := s.queryExecutor.Execute(
		ctx,
		input.OrgSlug,
		input.SinkDataSourceSlug,
		input.SinkOperationRef,
		model.SystemEventActor(),
		params,
	)
	captureSinkExecutionResult(input, execResult)
	if err != nil {
		return xerror.Wrapf("execute git sink query: %w", err)
	}

	return nil
}

// executeS3Sink binds an upstream runtime payload into a saved S3 write operation.
func (s *sinkRuntime) executeS3Sink(ctx context.Context, input *SinkRuntimeInput) error {
	if input.SinkDataSourceSlug == "" {
		return errSinkNotConfigured.Detail("sink data_source_slug is required for s3 backend")
	}

	config, err := decodeS3SinkConfig(input.SinkConfig)
	if err != nil {
		return err
	}

	if config.Key == nil || strings.TrimSpace(*config.Key) == "" {
		return errSinkNotConfigured.Detail("sink.config.key is required for s3 backend")
	}
	usesInline := len(input.SinkOperationInline) > 0
	if input.SinkOperationRef == "" && !usesInline {
		return errSinkNotConfigured.Detail("sink.operation_ref is required for s3 backend")
	}
	if input.SinkOperationRef != "" && usesInline {
		return errSinkNotConfigured.Detail("S3 sink must select exactly one saved or inline operation")
	}

	binding, err := buildSinkPayloadBinding(input)
	if err != nil {
		return err
	}

	params := map[string]any{binding.name: binding.value}
	addSinkPayloadBindingMetadataParams(params, binding)
	params["key"] = *config.Key
	if config.Bucket != nil {
		params["bucket"] = *config.Bucket
	}
	if config.Format != nil {
		params["format"] = *config.Format
	}
	if config.Delimiter != nil {
		params["delimiter"] = *config.Delimiter
	}
	if config.HasHeader != nil {
		params["has_header"] = *config.HasHeader
	}
	if config.Overwrite != nil {
		params["overwrite"] = *config.Overwrite
	}
	if config.Sheet != nil {
		params["sheet"] = *config.Sheet
	}
	if config.Range != nil {
		params["range"] = *config.Range
	}
	if config.ContentType != nil {
		params["content_type"] = *config.ContentType
	}
	if len(config.Metadata) > 0 {
		params["metadata"] = config.Metadata
	}
	addSinkRuntimeMetadataParams(params, input)

	var execResult *result.ExecutionResult
	if usesInline {
		executor, ok := s.queryExecutor.(interface {
			ExecuteInline(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
		})
		if !ok {
			return errSinkNotConfigured.Detail("S3 inline sink executor is required")
		}
		execResult, err = executor.ExecuteInline(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationInline, params)
	} else {
		execResult, err = s.queryExecutor.Execute(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationRef, model.SystemEventActor(), params)
	}
	captureSinkExecutionResult(input, execResult)
	if err != nil {
		return xerror.Wrapf("execute s3 sink query: %w", err)
	}

	return nil
}

func captureSinkExecutionResult(input *SinkRuntimeInput, execResult *result.ExecutionResult) {
	if input == nil || execResult == nil {
		return
	}

	switch execResult.Type() {
	case result.ExecutionResultTypeSQL:
		if sqlResult, ok := execResult.SQL(); ok && sqlResult != nil {
			input.SinkResult = result.NewSQLResult(execResult.VendorType(), &result.SQLResult{
				RowCount:    sqlResult.RowCount,
				ColumnNames: append([]string(nil), sqlResult.ColumnNames...),
			}, execResult.DurationMs(), execResult.Error())
		}
	case result.ExecutionResultTypeFTP:
		fileResult, ok := execResult.FTP()
		if ok {
			input.SinkResult = result.NewFTPResult(
				sinkOutputMetadataResult(fileResult),
				execResult.DurationMs(),
				execResult.Error(),
			)
		}
	case result.ExecutionResultTypeGit:
		fileResult, ok := execResult.Git()
		if ok {
			input.SinkResult = result.NewGitResult(
				sinkOutputMetadataResult(fileResult),
				execResult.DurationMs(),
				execResult.Error(),
			)
		}
	case result.ExecutionResultTypeS3:
		fileResult, ok := execResult.S3()
		if ok {
			input.SinkResult = result.NewS3Result(
				sinkOutputMetadataResult(fileResult),
				execResult.DurationMs(),
				execResult.Error(),
			)
		}
	case result.ExecutionResultTypeWorkspaceFile:
		f, ok := execResult.File()
		if ok {
			input.SinkResult = result.NewWorkspaceFileResult(sinkOutputMetadataResult(f), execResult.DurationMs(), execResult.Error())
		}

	default:
		return
	}
}

func sinkOutputMetadataResult(fileResult *result.FileResult) *result.FileResult {
	if fileResult == nil {
		return nil
	}

	return &result.FileResult{
		ResourceID: fileResult.ResourceID, Name: fileResult.Name, MIMEType: fileResult.MIMEType,
		ColumnNames:           append([]string(nil), fileResult.ColumnNames...),
		RowCount:              fileResult.RowCount,
		Format:                fileResult.Format,
		IsHeadlessCSV:         fileResult.IsHeadlessCSV,
		IsHeadlessSpreadsheet: fileResult.IsHeadlessSpreadsheet,
		Sheet:                 fileResult.Sheet,
		Range:                 fileResult.Range,
		Operation:             strings.TrimSpace(fileResult.Operation),
		TargetPath:            strings.TrimSpace(fileResult.TargetPath),
		File:                  filepayload.CloneFilePayloadMetadata(fileResult.File),
		WrittenBytes:          cloneOptionalInt(fileResult.WrittenBytes),
		WrittenRows:           cloneOptionalInt(fileResult.WrittenRows),
	}
}

// cloneOptionalInt copies optional connector metadata without sharing pointers.
func cloneOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}

	cloned := *value
	return &cloned
}

// dbSinkPayloadBinding chooses the public input binding shape for DB-family sinks.
// SQL sinks keep the JSON text binding used by native input CTE materialization,
// while catalog-declared row-native operations need the row objects themselves.
func dbSinkPayloadBinding(input *SinkRuntimeInput) (sinkPayloadBinding, error) {
	if input != nil &&
		catalog.SinkPayloadBindingForOperationType(strings.TrimSpace(input.SinkOperationType)) ==
			catalog.SinkPayloadBindingRowObject {
		return rowObjectSinkPayloadBinding(input)
	}

	return sinkEncodedPayloadBinding(input)
}

// rowObjectSinkPayloadBinding exposes upstream data as row objects for row-native sinks.
// It uses the shared contract-data parser so JSON and CSV payloads can flow into
// connectors such as BigQuery and HubSpot when they can be normalized to rows.
func rowObjectSinkPayloadBinding(input *SinkRuntimeInput) (sinkPayloadBinding, error) {
	if columns, rows, err := pipelinerun.ContractData(input.SourceResult); err != nil {
		return sinkPayloadBinding{}, xerror.Wrapf("normalize row-object sink rows: %w", err)
	} else if rows != nil || len(columns) > 0 {
		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          rowsAsJSONArray(cloneSinkInputRows(rows)),
			representation: model.ContractRepresentationRows,
		}, nil
	}

	return buildSinkPayloadBinding(input)
}

func sinkEncodedPayloadBinding(input *SinkRuntimeInput) (sinkPayloadBinding, error) {
	binding, err := buildSinkPayloadBinding(input)
	if err != nil {
		return sinkPayloadBinding{}, err
	}

	if binding.representation == model.ContractRepresentationCSV {
		return binding, nil
	}
	if binding.representation == model.ContractRepresentationFile {
		return binding, nil
	}
	if binding.representation == model.ContractRepresentationMultipartForm {
		return sinkPayloadBinding{}, errUnsupportedSinkBackend.Detail(
			"multipart_form sink payloads are only supported by http backend",
		)
	}

	contentJSON, err := json.Marshal(binding.value)
	if err != nil {
		return sinkPayloadBinding{}, xerror.Unexpected().With(err)
	}

	return sinkPayloadBinding{
		name:           sinkbinding.Input,
		value:          string(contentJSON),
		representation: binding.representation,
	}, nil
}

func buildSinkPayloadBinding(input *SinkRuntimeInput) (sinkPayloadBinding, error) {
	if input == nil || input.SourceResult == nil {
		return sinkPayloadBinding{}, errSinkNotConfigured.Detail("sink source result is required")
	}

	payload, ok := input.SourceResult.Payload()
	if !ok || payload == nil {
		return sinkPayloadBinding{}, errSinkNotConfigured.Detail("sink source payload is required")
	}

	switch payload.Representation {
	case model.ContractRepresentationRows:
		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          rowsAsJSONArray(payload.Rows),
			representation: payload.Representation,
		}, nil
	case model.ContractRepresentationJSON:
		if len(payload.Document) == 0 {
			return sinkPayloadBinding{}, errSinkNotConfigured.Detail("json payload is empty")
		}

		var documentPayload any
		if err := jsonvalue.Unmarshal(payload.Document, &documentPayload); err != nil {
			return sinkPayloadBinding{}, xerror.Unexpected().With(err)
		}

		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          documentPayload,
			representation: payload.Representation,
		}, nil
	case model.ContractRepresentationCSV:
		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          payload.CSV,
			representation: payload.Representation,
		}, nil
	case model.ContractRepresentationFile:
		if payload.File == nil {
			return sinkPayloadBinding{}, errSinkNotConfigured.Detail(
				"file payload metadata is required",
			)
		}
		if payload.File.SizeBytes > 0 && len(payload.FileBytes) == 0 {
			return sinkPayloadBinding{}, errSinkNotConfigured.Detail(
				"file payload bytes are not available",
			)
		}
		if int64(len(payload.FileBytes)) != payload.File.SizeBytes {
			return sinkPayloadBinding{}, errSinkNotConfigured.Detail(
				"file payload byte count does not match metadata",
			)
		}

		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          append([]byte(nil), payload.FileBytes...),
			representation: payload.Representation,
			file:           filepayload.CloneFilePayloadMetadata(payload.File),
		}, nil
	case model.ContractRepresentationMultipartForm:
		if payload.Form == nil {
			return sinkPayloadBinding{}, errSinkNotConfigured.Detail(
				"multipart_form payload is empty",
			)
		}

		return sinkPayloadBinding{
			name:           sinkbinding.Input,
			value:          payload.Form.Clone(),
			representation: payload.Representation,
		}, nil
	default:
		return sinkPayloadBinding{}, errSinkNotConfigured.Detailf(
			"unsupported sink payload representation %q",
			payload.Representation,
		)
	}
}

// validateSinkBackendPayloadSupport rejects representations a sink backend cannot serialize.
func validateSinkBackendPayloadSupport(
	backend model.SinkBackend,
	input *SinkRuntimeInput,
) error {
	if backend == model.SinkBackendHTTP || input == nil || input.SourceResult == nil {
		return nil
	}

	payload, ok := input.SourceResult.Payload()
	if !ok || payload == nil || payload.Representation != model.ContractRepresentationMultipartForm {
		return nil
	}

	return errUnsupportedSinkBackend.Detail(
		"multipart_form sink payloads are only supported by http backend",
	)
}

func validateReservedSinkConfigBindings(config map[string]any) error {
	for _, reserved := range []string{
		sinkbinding.Input,
		sinkbinding.InternalInputRows,
		sinkbinding.InternalInputColumns,
		sinkbinding.InternalInputRepresentation,
		sinkbinding.InternalInputFileMetadata,
		sinkbinding.InternalInputFileFilename,
		sinkbinding.InternalInputFileMIMEType,
		sinkbinding.InternalInputFileSizeBytes,
		sinkbinding.InternalInputFileDigestAlgorithm,
		sinkbinding.InternalInputFileDigest,
		sinkbinding.InternalIdempotencyKey,
		sinkbinding.InternalIdempotencyClass,
		sinkbinding.InternalEffectStartedAt,
		sinkbinding.InternalReplayIdentity,
	} {
		if _, exists := config[reserved]; exists {
			return errSinkNotConfigured.Detailf(
				"sink.config.%s is reserved for runtime payload binding",
				reserved,
			)
		}
	}

	return nil
}

// addSinkPayloadBindingMetadataParams exposes safe file metadata beside raw sink input bytes.
func addSinkPayloadBindingMetadataParams(params map[string]any, binding sinkPayloadBinding) {
	if params == nil || binding.file == nil {
		return
	}

	file := binding.file
	params[sinkbinding.InternalInputFileMetadata] = map[string]any{
		"filename":         file.Filename,
		"mime_type":        file.MIMEType,
		"size_bytes":       file.SizeBytes,
		"digest_algorithm": file.DigestAlgorithm,
		"digest":           file.Digest,
	}
	params[sinkbinding.InternalInputFileFilename] = file.Filename
	params[sinkbinding.InternalInputFileMIMEType] = file.MIMEType
	params[sinkbinding.InternalInputFileSizeBytes] = file.SizeBytes
	params[sinkbinding.InternalInputFileDigestAlgorithm] = file.DigestAlgorithm
	params[sinkbinding.InternalInputFileDigest] = file.Digest
}

func addSinkRuntimeMetadataParams(params map[string]any, input *SinkRuntimeInput) {
	if params == nil || input == nil {
		return
	}
	if input.RunSlug != "" {
		params["runtime.run.slug"] = input.RunSlug
	}
	if !input.EffectStartedAt.IsZero() {
		params[sinkbinding.InternalEffectStartedAt] = input.EffectStartedAt.UTC().Format(time.RFC3339Nano)
	}
	if input.IdempotencyKey != "" {
		params[sinkbinding.InternalIdempotencyKey] = input.IdempotencyKey
		params["runtime.sink.idempotency_key"] = input.IdempotencyKey
	}
	if input.IdempotencyClass != pipelinespec.SinkIdempotencyClassUnknown {
		value := input.IdempotencyClass.String()
		params[sinkbinding.InternalIdempotencyClass] = value
		params["runtime.sink.idempotency_class"] = value
	}
	if input.ReplayIdentity != "" {
		params[sinkbinding.InternalReplayIdentity] = input.ReplayIdentity
		params["runtime.sink.replay_identity"] = input.ReplayIdentity
	}
}

func rowsAsJSONArray(rows []map[string]any) []any {
	if rows == nil {
		return nil
	}

	values := make([]any, len(rows))
	for i := range rows {
		values[i] = rows[i]
	}

	return values
}

func cloneSinkInputRows(rows []map[string]any) []map[string]any {
	if rows == nil {
		return nil
	}

	cloned := make([]map[string]any, len(rows))
	for index, row := range rows {
		copied := make(map[string]any, len(row))
		for key, value := range row {
			copied[key] = cloneSinkInputValue(value)
		}
		cloned[index] = copied
	}

	return cloned
}

// cloneSinkInputValue preserves fractional tokens while retaining concrete 64-bit integers.
func cloneSinkInputValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if integer, err := strconv.ParseUint(typed.String(), 10, 64); err == nil {
			return integer
		}
		return typed
	default:
		return value
	}
}

func decodeFTPSinkConfig(config map[string]any) (*ftpSinkConfig, error) {
	path, hasPath, err := sinkConfigOptionalString(config, "path", "ftp")
	if err != nil {
		return nil, err
	}
	format, hasFormat, err := sinkConfigOptionalString(config, "format", "ftp")
	if err != nil {
		return nil, err
	}
	delimiter, hasDelimiter, err := sinkConfigOptionalString(config, "delimiter", "ftp")
	if err != nil {
		return nil, err
	}
	hasHeader, hasHasHeader, err := sinkConfigOptionalBool(config, "has_header", "ftp")
	if err != nil {
		return nil, err
	}
	overwrite, hasOverwrite, err := sinkConfigOptionalBool(config, "overwrite", "ftp")
	if err != nil {
		return nil, err
	}
	sheet, hasSheet, err := sinkConfigOptionalString(config, "sheet", "ftp")
	if err != nil {
		return nil, err
	}
	cellRange, hasRange, err := sinkConfigOptionalString(config, "range", "ftp")
	if err != nil {
		return nil, err
	}

	decoded := &ftpSinkConfig{}
	if hasPath {
		decoded.Path = &path
	}
	if hasFormat {
		decoded.Format = &format
	}
	if hasDelimiter {
		decoded.Delimiter = &delimiter
	}
	if hasHasHeader {
		decoded.HasHeader = &hasHeader
	}
	if hasOverwrite {
		decoded.Overwrite = &overwrite
	}
	if hasSheet {
		decoded.Sheet = &sheet
	}
	if hasRange {
		decoded.Range = &cellRange
	}

	return decoded, nil
}

func decodeGitSinkConfig(config map[string]any) (*gitSinkConfig, error) {
	path, hasPath, err := sinkConfigOptionalString(config, "path", "git")
	if err != nil {
		return nil, err
	}
	format, hasFormat, err := sinkConfigOptionalString(config, "format", "git")
	if err != nil {
		return nil, err
	}
	delimiter, hasDelimiter, err := sinkConfigOptionalString(config, "delimiter", "git")
	if err != nil {
		return nil, err
	}
	hasHeader, hasHasHeader, err := sinkConfigOptionalBool(config, "has_header", "git")
	if err != nil {
		return nil, err
	}
	overwrite, hasOverwrite, err := sinkConfigOptionalBool(config, "overwrite", "git")
	if err != nil {
		return nil, err
	}
	sheet, hasSheet, err := sinkConfigOptionalString(config, "sheet", "git")
	if err != nil {
		return nil, err
	}
	cellRange, hasRange, err := sinkConfigOptionalString(config, "range", "git")
	if err != nil {
		return nil, err
	}
	branch, hasBranch, err := sinkConfigOptionalString(config, "branch", "git")
	if err != nil {
		return nil, err
	}
	message, hasMessage, err := sinkConfigOptionalString(config, "message", "git")
	if err != nil {
		return nil, err
	}

	decoded := &gitSinkConfig{}
	if hasPath {
		decoded.Path = &path
	}
	if hasFormat {
		decoded.Format = &format
	}
	if hasDelimiter {
		decoded.Delimiter = &delimiter
	}
	if hasHasHeader {
		decoded.HasHeader = &hasHeader
	}
	if hasOverwrite {
		decoded.Overwrite = &overwrite
	}
	if hasSheet {
		decoded.Sheet = &sheet
	}
	if hasRange {
		decoded.Range = &cellRange
	}
	if hasBranch {
		decoded.Branch = &branch
	}
	if hasMessage {
		decoded.Message = &message
	}

	return decoded, nil
}

// decodeS3SinkConfig validates S3 sink config keys before they become query parameters.
func decodeS3SinkConfig(config map[string]any) (*s3SinkConfig, error) {
	key, hasKey, err := sinkConfigOptionalString(config, "key", "s3")
	if err != nil {
		return nil, err
	}
	bucket, hasBucket, err := sinkConfigOptionalString(config, "bucket", "s3")
	if err != nil {
		return nil, err
	}
	format, hasFormat, err := sinkConfigOptionalString(config, "format", "s3")
	if err != nil {
		return nil, err
	}
	delimiter, hasDelimiter, err := sinkConfigOptionalString(config, "delimiter", "s3")
	if err != nil {
		return nil, err
	}
	hasHeader, hasHasHeader, err := sinkConfigOptionalBool(config, "has_header", "s3")
	if err != nil {
		return nil, err
	}
	overwrite, hasOverwrite, err := sinkConfigOptionalBool(config, "overwrite", "s3")
	if err != nil {
		return nil, err
	}
	sheet, hasSheet, err := sinkConfigOptionalString(config, "sheet", "s3")
	if err != nil {
		return nil, err
	}
	cellRange, hasRange, err := sinkConfigOptionalString(config, "range", "s3")
	if err != nil {
		return nil, err
	}
	contentType, hasContentType, err := sinkConfigOptionalString(config, "content_type", "s3")
	if err != nil {
		return nil, err
	}
	metadata, err := sinkConfigOptionalStringMap(config, "metadata", "s3")
	if err != nil {
		return nil, err
	}

	decoded := &s3SinkConfig{}
	if hasKey {
		decoded.Key = &key
	}
	if hasBucket {
		decoded.Bucket = &bucket
	}
	if hasFormat {
		decoded.Format = &format
	}
	if hasDelimiter {
		decoded.Delimiter = &delimiter
	}
	if hasHasHeader {
		decoded.HasHeader = &hasHeader
	}
	if hasOverwrite {
		decoded.Overwrite = &overwrite
	}
	if hasSheet {
		decoded.Sheet = &sheet
	}
	if hasRange {
		decoded.Range = &cellRange
	}
	if hasContentType {
		decoded.ContentType = &contentType
	}
	decoded.Metadata = metadata

	return decoded, nil
}

func sinkConfigOptionalString(config map[string]any, key, backend string) (string, bool, error) {
	if len(config) == 0 {
		return "", false, nil
	}

	raw, exists := config[key]
	if !exists {
		return "", false, nil
	}

	value, ok := raw.(string)
	if !ok {
		return "", false, errSinkNotConfigured.Detailf(
			"sink.config.%s must be a string for %s backend",
			key,
			backend,
		)
	}

	return value, true, nil
}

// sinkConfigOptionalStringMap extracts optional string-only config maps for provider metadata.
func sinkConfigOptionalStringMap(config map[string]any, key, backend string) (map[string]string, error) {
	if len(config) == 0 {
		return map[string]string{}, nil
	}

	raw, exists := config[key]
	if !exists {
		return map[string]string{}, nil
	}

	values, ok := raw.(map[string]any)
	if !ok {
		if typed, ok := raw.(map[string]string); ok {
			return cloneStringMap(typed), nil
		}
		return nil, errSinkNotConfigured.Detailf(
			"sink.config.%s must be an object with string values for %s backend",
			key,
			backend,
		)
	}

	decoded := make(map[string]string, len(values))
	for name, value := range values {
		stringValue, ok := value.(string)
		if !ok {
			return nil, errSinkNotConfigured.Detailf(
				"sink.config.%s.%s must be a string for %s backend",
				key,
				name,
				backend,
			)
		}
		decoded[name] = stringValue
	}

	return decoded, nil
}

func sinkConfigOptionalBool(config map[string]any, key, backend string) (bool, bool, error) {
	if len(config) == 0 {
		return false, false, nil
	}

	raw, exists := config[key]
	if !exists {
		return false, false, nil
	}

	value, ok := raw.(bool)
	if !ok {
		return false, false, errSinkNotConfigured.Detailf(
			"sink.config.%s must be a bool for %s backend",
			key,
			backend,
		)
	}

	return value, true, nil
}

// cloneStringMap copies string maps passed between sink config and connector params.
func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

func sinkDeliveryOutcome(input *SinkRuntimeInput) string {
	if input == nil {
		return "failed"
	}
	return input.DeliveryOutcome
}
