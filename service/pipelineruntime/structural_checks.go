package pipelineruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinegraph "github.com/Braidplane/braidplane-core/pkg/pipeline/graph"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	errCheckPersistence   = xerror.New(xerror.KindInternal, "RUN_PROGRESS_PERSISTENCE_FAILED", "run execution evidence could not be persisted")
	errAttemptInterrupted = xerror.New(xerror.KindInternal, "RUN_ATTEMPT_INTERRUPTED", "run attempt interrupted; retry with a new attempt identity")
)

const maxStructuralChecks = 4096

// checkJournal writes through the existing run store so access, retention and
// deletion stay identical to run history. It never stores evaluated values.
type checkJournal struct {
	runner            *Runner
	request           RunRequest
	pipeline          *pipelinespec.Pipeline
	timeline          *pipelinerun.RunTimeline
	started           time.Time
	redactor          runtimeSecretRedactor
	structures        map[int]*payload.Structure
	contracts         map[int]*payload.Contract
	strictProjections bool
}

func checkDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newCheckJournal(r *Runner, req RunRequest, pipeline *pipelinespec.Pipeline, operations map[slug.Slug]contractplan.Operation, timeline *pipelinerun.RunTimeline, started time.Time, redactor runtimeSecretRedactor) (*checkJournal, error) {
	j := &checkJournal{runner: r, request: req, pipeline: pipeline, timeline: timeline, started: started, redactor: redactor, structures: map[int]*payload.Structure{}, contracts: map[int]*payload.Contract{}}
	if len(req.CompiledPlan) == 0 && !pipeline.UsesContractComposition() {
		return j, nil
	}
	graph, err := pipelinegraph.Normalize(pipeline)
	if err != nil {
		return nil, err
	}
	sources := map[slug.Slug][]slug.Slug{}
	for _, step := range graph.TopologicalSteps() {
		switch {
		case step.Query != nil || step.Input != nil:
			sources[step.Slug] = []slug.Slug{step.Slug}
		case step.Transform != nil:
			inputs, err := resolveTransformInputStepSlugsFromGraph(graph, step.Slug)
			if err != nil {
				return nil, err
			}
			for _, upstream := range inputs {
				sources[step.Slug] = append(sources[step.Slug], sources[upstream]...)
			}
		case step.Sink != nil:
			upstream, err := resolveSinkUpstreamStepSlugFromGraph(graph, step.Slug)
			if err != nil {
				return nil, err
			}
			sources[step.Slug] = append(sources[step.Slug], sources[upstream]...)
		}
		slices.Sort(sources[step.Slug])
		sources[step.Slug] = slices.Compact(sources[step.Slug])
	}
	var projection *odcs.ProjectionPlan
	var declarations map[slug.Slug]pipelinespec.Step
	if len(req.CompiledPlan) > 0 {
		plan, err := contractplan.Read(req.CompiledPlan, req.CompiledPlanDigest)
		if err != nil {
			return nil, err
		}
		projection = plan.Projection
		j.strictProjections = plan.FormatVersion >= 4
		var authored pipelinespec.Pipeline
		if err := json.Unmarshal(plan.Authored, &authored); err != nil {
			return nil, contractplan.ErrInvalid.With(err)
		}
		declarations = make(map[slug.Slug]pipelinespec.Step, len(authored.Steps))
		for _, step := range authored.Steps {
			declarations[step.Slug] = step
		}
	}
	add := func(step pipelinespec.Step, origin, phase, input string, contract *payload.Contract, ref *pipelinespec.ODCSContractRef, target slug.Slug) error {
		if contract == nil {
			return nil
		}
		if declarations != nil && origin == "step" {
			// RuntimePipeline consumes verified pins and clears refs, and also
			// installs operation defaults. Recover only authored step obligations.
			declared, authoredRef := structuralCheckDeclaration(declarations[step.Slug], phase, input)
			if declared == nil && authoredRef == nil {
				return nil
			}
			ref = authoredRef
		}
		if len(timeline.StructuralChecks) >= maxStructuralChecks {
			return xerror.InvalidInput(xerror.Pipeline).Detail("too many structural check obligations")
		}
		digest, err := checkDigest(contract)
		if err != nil {
			return err
		}
		declaration := origin + "/" + phase
		if input != "" {
			declaration += "/" + input
		}
		if ref != nil {
			declaration += "/" + ref.ContractSlug.String() + "/" + ref.RevisionSlug.String() + "/" + ref.SchemaObjectID
			structure, projectionDigest, err := structuralCheckProjection(projection, ref, contract)
			if err != nil {
				return err
			}
			if structure != nil {
				j.structures[len(timeline.StructuralChecks)] = structure
				digest = projectionDigest
			}
		}
		id, err := checkDigest([]string{timeline.RunSlug, step.Slug.String(), origin, phase, input, digest})
		if err != nil {
			return err
		}
		window := pipelinerun.CheckWindow{Kind: "full", SourceSteps: append([]slug.Slug{}, sources[step.Slug]...)}
		if pipelineHasIncrementalSource(pipeline) || len(req.Incremental.SourceWindows) > 0 || req.Incremental.BackfillSeriesSlug != "" {
			window.Kind = incrementalRunMode(req.Incremental)
		}
		if window.Kind != "incremental" && window.Kind != "backfill" {
			window.Kind = "full"
		}
		for _, source := range window.SourceSteps {
			if sourceWindow, ok := req.Incremental.SourceWindows[source]; ok {
				window.HasLowerBound = window.HasLowerBound || len(sourceWindow.LowerBound) > 0
				window.HasUpperBound = window.HasUpperBound || len(sourceWindow.UpperBound) > 0
			}
		}
		window.BackfillSeriesSlug = req.Incremental.BackfillSeriesSlug.String()
		window.BackfillChunkIndex = req.Incremental.BackfillChunkIndex
		j.contracts[len(timeline.StructuralChecks)] = contract
		timeline.StructuralChecks = append(timeline.StructuralChecks, pipelinerun.StructuralCheck{
			ID: id, Step: step.Slug, Input: input, Origin: origin, Phase: phase, Declaration: declaration,
			ProjectionDigest: digest, CompiledPlanDigest: req.CompiledPlanDigest, DeploymentResolutionDigest: req.DeploymentResolutionDigest,
			PipelineVersionID: req.PipelineVersionID, AttemptNumber: req.Source.AttemptNumber,
			Evaluator: "braidplane/structural-runtime/v1", Policy: contractplan.StructuralPolicyID, Target: target,
			Status: pipelinerun.CheckStatusUnavailable, ReasonCode: "not_reached", Window: window,
		})
		return nil
	}
	for _, step := range pipeline.Steps {
		if step.Input != nil {
			if err := add(step, "step", "event_admission", "", step.Input.OutputContract, step.Input.OutputContractRef, ""); err != nil {
				return nil, err
			}
		}
		if op, ok := operations[step.Slug]; ok {
			in, out, err := contractplan.OperationContracts(op)
			if err != nil {
				return nil, err
			}
			var content contractplan.OperationContent
			if err := json.Unmarshal(op.Content, &content); err != nil {
				return nil, contractplan.ErrInvalid.With(err)
			}
			if op.Role == "query" {
				err = add(step, "operation", "producer_output", "", out, content.OutputContractRef, op.DataSource)
			} else {
				err = add(step, "operation", "pre_sink", "", in, content.ExpectedInputContractRef, op.DataSource)
			}
			if err != nil {
				return nil, err
			}
		}
		if step.Query != nil {
			if err := add(step, "step", "producer_output", "", step.Query.OutputContract, step.Query.OutputContractRef, step.Query.DataSourceSlug); err != nil {
				return nil, err
			}
		}
		if step.Transform != nil {
			names := make([]string, 0, len(step.Transform.InputContracts))
			for name := range step.Transform.InputContracts {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				attachment := step.Transform.InputContracts[name]
				if attachment != nil {
					if err := add(step, "step", "normalized_input", name, attachment.Contract, attachment.ContractRef, step.Transform.Inputs[name].FromStep); err != nil {
						return nil, err
					}
				}
			}
			if step.Transform.InputContract != nil {
				names = names[:0]
				for name := range step.Transform.Inputs {
					names = append(names, name)
				}
				// Implicit linear transforms have one conventional input.
				if len(names) == 0 {
					names = append(names, "input")
				}
				sort.Strings(names)
				for _, name := range names {
					contract := transformsql.ContractForNamedInput(step.Transform.InputContract, name, len(names))
					if err := add(step, "step", "normalized_input", name, contract, step.Transform.InputContractRef, step.Transform.Inputs[name].FromStep); err != nil {
						return nil, err
					}
				}
			}
			if err := add(step, "step", "producer_output", "", step.Transform.OutputContract, step.Transform.OutputContractRef, ""); err != nil {
				return nil, err
			}
		}
		if step.Sink != nil {
			if err := add(step, "step", "pre_sink", "", step.Sink.InputContract, step.Sink.InputContractRef, step.Sink.DataSourceSlug); err != nil {
				return nil, err
			}
		}
	}
	return j, nil
}

func (j *checkJournal) persist(ctx context.Context) error {
	if admissions := deploymentplan.Admissions(ctx); len(admissions) > 0 {
		j.timeline.Admissions = admissions
	}
	summary := fullIncrementalRunSummary()
	if pipelineHasIncrementalSource(j.pipeline) || j.request.Incremental.BackfillSeriesSlug != "" {
		summary.Mode = incrementalRunMode(j.request.Incremental)
		summary.CheckpointStatus = "skipped"
		summary.BackfillSeriesSlug = j.request.Incremental.BackfillSeriesSlug.String()
		summary.BackfillChunkIndex = j.request.Incremental.BackfillChunkIndex
		summary.BackfillChunkCount = j.request.Incremental.BackfillChunkCount
	}
	j.timeline.Revision++
	observedAt := j.started
	if n := len(j.timeline.Events); n > 0 {
		observedAt = j.timeline.Events[n-1].At
	}
	result := &pipelinerun.RunResult{Metadata: pipelinerun.RunMetadata{
		RunSlug: j.timeline.RunSlug, ProjectSlug: j.request.ProjectSlug, EnvironmentSlug: j.request.EnvironmentSlug,
		DeploymentID: j.request.DeploymentID, DeploymentRevision: j.request.DeploymentRevision,
		Source: j.request.Source, Status: j.timeline.Status, PipelineVersionID: j.request.PipelineVersionID,
		DeploymentResolutionDigest: j.request.DeploymentResolutionDigest, StepCount: len(j.pipeline.Steps),
		DurationMs: durationMilliseconds(j.started, observedAt), Incremental: summary,
		ReplayOfRunSlug: j.request.ReplayOfRunSlug, ReplayFromStep: j.request.ReplayFromStep,
	}, Timeline: *j.timeline, IncrementalDiagnostics: emptyIncrementalRunDiagnostics()}
	redactRunResultSecrets(result, j.redactor)
	if err := j.runner.store.PersistRun(ctx, j.request.OrgSlug, j.request.ProjectSlug, j.request.EnvironmentSlug, j.pipeline, result); err != nil {
		j.timeline.Revision--
		return errCheckPersistence.With(err)
	}
	return nil
}

func (j *checkJournal) evaluate(ctx context.Context, step slug.Slug, origin, phase, input string, source *pipelinerun.StepResult, evaluate func() error) error {
	index := -1
	for i, c := range j.timeline.StructuralChecks {
		if c.Step == step && c.Origin == origin && c.Phase == phase && c.Input == input {
			index = i
			break
		}
	}
	if index < 0 {
		return evaluate()
	}
	check := &j.timeline.StructuralChecks[index]
	at := j.runner.clock.Now()
	check.StartedAt = &at
	check.Status, check.ReasonCode = pipelinerun.CheckStatusUnavailable, "evaluating"
	if err := j.persist(ctx); err != nil {
		check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "persistence_failed"
		return err
	}
	err := ctx.Err()
	if err == nil {
		err = evaluate()
	}
	if err == nil && source == nil {
		err = xerror.Unsupported(xerror.Pipeline).Detail("structural payload unavailable")
	}

	var rows []map[string]any
	if source != nil {
		var dataErr error
		_, rows, dataErr = pipelinerun.ContractData(source)
		if err == nil && dataErr != nil {
			err = xerror.Unsupported(xerror.Pipeline).With(dataErr)
		}
	}
	if err == nil && j.structures[index] != nil {
		runtimePayload, ok := source.Payload()
		if !ok {
			err = xerror.Unsupported(xerror.Pipeline).Detail("structural payload unavailable")
		} else {
			if runtimePayload.Representation == model.ContractRepresentationFile || runtimePayload.Representation == model.ContractRepresentationMultipartForm {
				err = xerror.Unsupported(xerror.Pipeline)
			} else {
				contract := j.contracts[index]
				if !j.strictProjections {
					if runtimePayload.Representation == model.ContractRepresentationCSV {
						rows = payload.CoerceContractFieldValidationRows(contract, rows)
					}
					rows, err = payload.CoerceDecimalRows(contract, rows)
				}
				if err == nil {
					err = validateProjectedBatch(j.structures[index], rows)
				}
			}
		}
	}
	completed := j.runner.clock.Now()
	check.CompletedAt = &completed
	check.Status, check.ReasonCode = pipelinerun.CheckStatusPassed, "conformant"
	if err == nil && len(rows) == 0 {
		check.ReasonCode = "empty_batch"
	}
	if err != nil {
		check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "evaluation_error"
		var typed *xerror.Error
		if xerror.As(err, &typed) {
			switch typed.Kind() {
			case xerror.KindUnsupported, xerror.KindUnavailable:
				check.Status, check.ReasonCode = pipelinerun.CheckStatusUnavailable, "evaluator_unavailable"
			case xerror.KindInvalidInput:
				check.Status, check.ReasonCode = pipelinerun.CheckStatusFailed, "contract_violation"
			case xerror.KindInternal, xerror.KindNotFound, xerror.KindAlreadyExists, xerror.KindUnauthorized, xerror.KindConnectionFailed, xerror.KindTimeout, xerror.KindForbidden:
				check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "evaluation_error"
			}
		}
		var structural *payload.StructuralError
		if xerror.As(err, &structural) {
			check.Status, check.ReasonCode = pipelinerun.CheckStatusFailed, "contract_violation"
			switch structural.Code {
			case payload.StructureUnsupported:
				check.Status, check.ReasonCode = pipelinerun.CheckStatusUnavailable, "evaluator_unavailable"
			case payload.StructuralCodeUnknown, payload.StructureInvalid, payload.StructureLimit, payload.StructureAmbiguous:
				check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "evaluation_error"
			case payload.StructurePresence, payload.StructureNull, payload.StructureType, payload.StructureConstraint, payload.StructureEncoding:
			}
			if check.Status == pipelinerun.CheckStatusFailed {
				switch phase {
				case "producer_output":
					if _, transform := source.Transform(); transform {
						err = xerror.Join(xerror.TransformOutputContractViolation(), err)
					} else {
						err = xerror.Join(xerror.QueryOutputContractSchemaMismatch(), err)
					}
				case "normalized_input":
					err = xerror.Join(xerror.TransformInputContractViolation(), err)
				case "pre_sink":
					err = xerror.Join(xerror.SinkInputContractViolation(), err)
				case "event_admission":
					err = xerror.InvalidInput(xerror.Event).With(err)
				}
			}
		}
		if source == nil {
			check.Status, check.ReasonCode = pipelinerun.CheckStatusUnavailable, "payload_unavailable"
		}
		if ctx.Err() != nil || xerror.Is(err, context.Canceled) || xerror.Is(err, context.DeadlineExceeded) {
			check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "evaluation_interrupted"
		}
	}
	evidence := &pipelinerun.CheckEvidence{Rows: min(len(rows), 1000000000), Empty: len(rows) == 0, DurationMs: min(max(durationMilliseconds(at, completed), 0), 86400000), BatchScope: "observed_batch_completeness_unknown"}
	if source != nil {
		if objects, ok := source.ObjectRows(); ok && objects.HasMore {
			evidence.BatchScope = "truncated_batch"
		}
	}
	check.Evidence = evidence
	if check.Status == pipelinerun.CheckStatusFailed {
		check.ContractFailure = contractFailureDiagnostic(err)
	}
	check.EvidenceDigest, _ = checkDigest(evidence)
	if check.Status == pipelinerun.CheckStatusUnavailable {
		check.Evidence, check.EvidenceDigest = nil, ""
	}
	if persistErr := j.persist(ctx); persistErr != nil {
		check.Status, check.ReasonCode = pipelinerun.CheckStatusError, "persistence_failed"
		check.Evidence, check.EvidenceDigest = nil, ""
		return xerror.Join(persistErr, err)
	}
	return err
}

func validateProjectedBatch(structure *payload.Structure, rows []map[string]any) error {
	// The structural evaluator consumes logical values; normalize encoded payloads
	// through the same contract-data path used by the runtime boundary.
	if structure.Root == nil {
		return contractplan.ErrInvalid
	}
	if structure.Root.Kind == payload.StructuralArray {
		values := make([]any, len(rows))
		for i, row := range rows {
			values[i] = row
		}
		return payload.ValidateStructuralValue(structure, values, payload.DefaultStructuralLimits())
	}
	batch := *structure
	batch.Root = &payload.StructuralType{Kind: payload.StructuralArray, Items: structure.Root}
	values := make([]any, len(rows))
	for i, row := range rows {
		values[i] = row
	}
	return payload.ValidateStructuralValue(&batch, values, payload.DefaultStructuralLimits())
}

func (j *checkJournal) observeWindow(source slug.Slug, input *pipelinerun.IncrementalCheckpointInput) {
	if input == nil {
		return
	}
	for i := range j.timeline.StructuralChecks {
		window := &j.timeline.StructuralChecks[i].Window
		if slices.Contains(window.SourceSteps, source) {
			window.HasLowerBound = window.HasLowerBound || len(input.LowerBound) > 0
			window.HasUpperBound = window.HasUpperBound || len(input.UpperBound) > 0
		}
	}
}

func structuralCheckProjection(plan *odcs.ProjectionPlan, ref *pipelinespec.ODCSContractRef, contract *payload.Contract) (*payload.Structure, string, error) {
	return contractplan.SelectStructuralProjection(plan, ref, contract)
}

func structuralCheckDeclaration(step pipelinespec.Step, phase, input string) (*payload.Contract, *pipelinespec.ODCSContractRef) {
	switch phase {
	case "event_admission":
		if step.Input != nil {
			return step.Input.OutputContract, step.Input.OutputContractRef
		}
	case "producer_output":
		if step.Query != nil {
			return step.Query.OutputContract, step.Query.OutputContractRef
		}
		if step.Transform != nil {
			return step.Transform.OutputContract, step.Transform.OutputContractRef
		}
	case "normalized_input":
		if step.Transform != nil {
			if step.Transform.InputContracts != nil {
				if attachment := step.Transform.InputContracts[input]; attachment != nil {
					return attachment.Contract, attachment.ContractRef
				}
			} else {
				return step.Transform.InputContract, step.Transform.InputContractRef
			}
		}
	case "pre_sink":
		if step.Sink != nil {
			return step.Sink.InputContract, step.Sink.InputContractRef
		}
	}
	return nil, nil
}
