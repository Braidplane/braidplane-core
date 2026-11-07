package datasourcerefssvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

func (s *service) CreateDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	newRef model.NewDataSourceRef,
	actor model.EventActor,
) (*model.DataSourceRef, error) {
	if err := validate.Default().Struct(newRef); err != nil {
		return nil, ErrInvalidDataSourceRefInput.With(err)
	}
	if err := validateRefCapabilities(newRef.DataSourceType, newRef.RequiredCapabilities); err != nil {
		return nil, err
	}

	slugWasProvided := newRef.Slug != ""
	refSlug, err := newRef.Slug.ValidateOrGenerate(newRef.Name)
	if err != nil {
		return nil, ErrInvalidDataSourceRefInput.Detailf("failed to generate slug: %w", err)
	}
	newRef.Slug = refSlug

	var created *model.DataSourceRef
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoRef, err := s.repo.CreateDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, datasourcerefs.CreateDataSourceRefParams{
			Slug:                 newRef.Slug,
			Name:                 newRef.Name,
			DataSourceType:       newRef.DataSourceType,
			RequiredCapabilities: newRef.RequiredCapabilities,
		})
		if err != nil && xerror.Is(err, ErrDataSourceRefSlugTaken) && !slugWasProvided {
			newRef.Slug, err = slug.WithRandomSuffix(refSlug, 6)
			if err != nil {
				return ErrInvalidDataSourceRefInput.Detailf("failed to generate slug: %w", err)
			}
			repoRef, err = s.repo.CreateDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, datasourcerefs.CreateDataSourceRefParams{
				Slug:                 newRef.Slug,
				Name:                 newRef.Name,
				DataSourceType:       newRef.DataSourceType,
				RequiredCapabilities: newRef.RequiredCapabilities,
			})
		}
		if err != nil {
			return err
		}
		created = toModelRef(repoRef)
		return s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, created, model.EventActionCreate)
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *service) GetDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
) (*model.DataSourceRef, error) {
	var ref *model.DataSourceRef
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoRef, err := s.repo.FindDataSourceRefBySlug(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		ref = toModelRef(repoRef)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ref, nil
}

func (s *service) ListDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.DataSourceRef], error) {
	normalized, _ := model.PrepareListQuery(req, model.ListSortCreatedAt)
	var refs []model.DataSourceRef
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoRefs, err := s.repo.ListDataSourceRefs(ctx, tx, orgSlug, projectSlug, req)
		if err != nil {
			return err
		}
		refs = make([]model.DataSourceRef, 0, len(repoRefs))
		for _, ref := range repoRefs {
			refs = append(refs, *toModelRef(ref))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.DataSourceRef]{}, err
	}
	refs, hasMore := model.FinalizePage(refs, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		refs,
		normalized,
		hasMore,
		func(item model.DataSourceRef, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			if sortBy == model.ListSortUpdatedAt {
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			}
			return cursorValue, item.PipelineSlug.String() + "/" + item.Slug.String()
		},
	)
	return model.ListResponse[model.DataSourceRef]{
		Items:      refs,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) UpdateDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
	update model.UpdateDataSourceRef,
	actor model.EventActor,
) (*model.DataSourceRef, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, ErrInvalidDataSourceRefInput.With(err)
	}
	if err := validateRefCapabilities(update.DataSourceType, update.RequiredCapabilities); err != nil {
		return nil, err
	}

	var updated *model.DataSourceRef
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		protections, err := s.repo.ListProtectedBindingEnvironmentsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		if len(protections) > 1 {
			return ErrInvalidDataSourceRefInput.Detailf(
				"data source ref %s is bound in multiple protected environments; delete those bindings with protected confirmation before updating the ref contract",
				refSlug,
			)
		}
		if len(protections) == 1 {
			if err := protectedenv.Confirm(&protections[0], update.ProtectedEnvironmentConfirmation); err != nil {
				return err
			}
		}
		existingBindings, err := s.repo.ListEnvironmentBindingsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		nextRef := &model.DataSourceRef{
			PipelineSlug:         pipelineSlug,
			Slug:                 refSlug,
			Name:                 update.Name,
			DataSourceType:       update.DataSourceType,
			RequiredCapabilities: update.RequiredCapabilities,
		}
		for _, binding := range existingBindings {
			target := &datasourcessvc.DataSource{
				Slug:     binding.DataSourceSlug,
				Name:     binding.DataSourceName,
				Type:     binding.DataSourceType,
				IsActive: binding.DataSourceActive,
				ReadOnly: binding.DataSourceReadOnly,
			}
			if err := validateConcreteTarget(nextRef, target); err != nil {
				return err
			}
		}
		repoRef, err := s.repo.UpdateDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug, datasourcerefs.UpdateDataSourceRefParams{
			Name:                 update.Name,
			DataSourceType:       update.DataSourceType,
			RequiredCapabilities: update.RequiredCapabilities,
		})
		if err != nil {
			return err
		}
		updated = toModelRef(repoRef)
		return s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, updated, model.EventActionUpdate)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *service) DeleteDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		ref, err := s.repo.FindDataSourceRefBySlug(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		protectedBindings, err := s.repo.ListProtectedBindingEnvironmentsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		if len(protectedBindings) > 0 {
			return ErrInvalidDataSourceRefInput.Detailf(
				"data source ref %s is bound in protected environment %s; delete that binding with protected confirmation before deleting the ref",
				refSlug,
				protectedBindings[0].EnvironmentSlug,
			)
		}
		if err := s.repo.DeleteDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug); err != nil {
			return err
		}
		return s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, toModelRef(ref), model.EventActionDelete)
	})
}

func (s *service) UpsertEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
	binding model.UpsertDataSourceBinding,
	actor model.EventActor,
) (*model.DataSourceBinding, error) {
	if err := validate.Default().Struct(binding); err != nil {
		return nil, ErrInvalidDataSourceRefInput.With(err)
	}

	target, err := s.dataSources.FindBySlug(ctx, orgSlug, binding.DataSourceSlug)
	if err != nil {
		return nil, err
	}

	var upserted *model.DataSourceBinding
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		enabled, err := s.repo.ProjectEnvironmentEnabled(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if err != nil {
			return err
		}
		if !enabled {
			return ErrProjectEnvironmentNotEnabled
		}
		protection, err := s.repo.FindProjectEnvironmentProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if err != nil {
			return err
		}
		if err := protectedenv.Confirm(protection, binding.ProtectedEnvironmentConfirmation); err != nil {
			return err
		}
		ref, err := s.repo.FindDataSourceRefBySlug(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug)
		if err != nil {
			return err
		}
		if err := validateConcreteTarget(toModelRef(ref), target); err != nil {
			return err
		}
		repoBinding, err := s.repo.UpsertEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, datasourcerefs.UpsertEnvironmentBindingParams{
			DataSourceSlug: binding.DataSourceSlug,
		})
		if err != nil {
			return err
		}
		upserted = toModelBinding(repoBinding)
		return s.recordDataSourceBindingEvent(ctx, tx, orgSlug, projectSlug, pipelineSlug, actor, upserted, model.EventActionUpdate, protection.Protected)
	})
	if err != nil {
		return nil, err
	}
	return upserted, nil
}

func (s *service) GetEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
) (*model.DataSourceBinding, error) {
	var binding *model.DataSourceBinding
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.requireEnabledProjectEnvironment(ctx, tx, orgSlug, projectSlug, environmentSlug); err != nil {
			return err
		}
		repoBinding, err := s.repo.FindEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
		if err != nil {
			return err
		}
		binding = toModelBinding(repoBinding)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return binding, nil
}

func (s *service) ListEnvironmentBindings(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.DataSourceBinding], error) {
	normalized, _ := model.PrepareListQuery(req, model.ListSortUpdatedAt)
	var bindings []model.DataSourceBinding
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.requireEnabledProjectEnvironment(ctx, tx, orgSlug, projectSlug, environmentSlug); err != nil {
			return err
		}
		repoBindings, err := s.repo.ListEnvironmentBindings(ctx, tx, orgSlug, projectSlug, environmentSlug, req)
		if err != nil {
			return err
		}
		bindings = make([]model.DataSourceBinding, 0, len(repoBindings))
		for _, binding := range repoBindings {
			bindings = append(bindings, *toModelBinding(binding))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.DataSourceBinding]{}, err
	}
	bindings, hasMore := model.FinalizePage(bindings, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		bindings,
		normalized,
		hasMore,
		func(item model.DataSourceBinding, _ string) (string, string) {
			return item.UpdatedAt.Format(time.RFC3339Nano), item.PipelineSlug.String() + "/" + item.DataSourceRefSlug.String()
		},
	)
	return model.ListResponse[model.DataSourceBinding]{
		Items:      bindings,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) DeleteEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		protection, protectionErr := s.repo.FindProjectEnvironmentProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if protectionErr != nil {
			return protectionErr
		}
		if err := protectedenv.Confirm(protection, confirmation); err != nil {
			return err
		}
		binding, err := s.repo.FindEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
		if err != nil {
			return err
		}
		if err := s.repo.DeleteEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug); err != nil {
			return err
		}
		return s.recordDataSourceBindingEvent(ctx, tx, orgSlug, projectSlug, pipelineSlug, actor, toModelBinding(binding), model.EventActionDelete, protection.Protected)
	})
}

func (s *service) ResolvePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*ResolvedPipeline, error) {
	var resolved *ResolvedPipeline
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		resolved, err = s.resolvePipelineDataSourceRefs(ctx, tx, orgSlug, projectSlug, environmentSlug, pipeline)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// resolvePipelineDataSourceRefs resolves logical refs using the supplied DB handle.
func (s *service) resolvePipelineDataSourceRefs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*ResolvedPipeline, error) {
	resolved, err := clonePipeline(pipeline)
	if err != nil {
		return nil, err
	}
	if err := s.requireEnabledProjectEnvironment(ctx, tx, orgSlug, projectSlug, environmentSlug); err != nil {
		return nil, err
	}
	variables, err := s.repo.ListRuntimeVariables(ctx, tx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, err
	}
	inputs := []deploymentplan.Input{}
	for _, key := range pipelineruntime.EnvironmentKeys(resolved) {
		for _, variable := range variables {
			if variable.Key == key {
				inputs = append(inputs, deploymentplan.Input{Key: key, Type: variable.ValueType, Value: variable.Value, Revision: variable.UpdatedAt.UTC().Format(time.RFC3339Nano)})
			}
		}
	}
	if err := pipelineruntime.ResolvePipeline(resolved, variables); err != nil {
		return nil, err
	}
	pipelineSlug := resolved.Metadata.Slug
	if pipelineSlug == "" {
		return nil, ErrInvalidDataSourceRefInput.Detail("pipeline metadata.slug is required")
	}

	resolutions := make([]model.ResolvedPipelineDataSourceRef, 0)
	for i := range resolved.Steps {
		step := &resolved.Steps[i]
		if step.Query != nil && step.Query.DataSourceSlug != "" {
			resolution, err := s.resolveStepDataSource(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, step, i, "query", step.Query.DataSourceSlug, step.Query.OperationType, model.DataSourceRefCapabilityPipelineSource)
			if err != nil {
				return nil, err
			}
			step.Query.DataSourceSlug = resolution.DataSourceSlug
			resolutions = append(resolutions, resolution)
		}
		if step.Sink != nil && step.Sink.DataSourceSlug != "" {
			resolution, err := s.resolveStepDataSource(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, step, i, "sink", step.Sink.DataSourceSlug, step.Sink.OperationType, model.DataSourceRefCapabilityPipelineSink)
			if err != nil {
				return nil, err
			}
			if !pipelinespec.SupportsSinkIdempotencyClass(resolution.DataSourceType, step.Sink.IdempotencyClass) {
				return nil, stepResolutionError(
					ErrDataSourceRefIncompatible, i, step.Slug, "sink.idempotency_class",
					step.Sink.DataSourceSlug, environmentSlug, model.DataSourceRefCapabilityPipelineSink,
					fmt.Sprintf("sink data source type %q does not support idempotency_class %q", resolution.DataSourceType, step.Sink.IdempotencyClass),
				)
			}
			step.Sink.DataSourceSlug = resolution.DataSourceSlug
			resolutions = append(resolutions, resolution)
		}
	}
	return &ResolvedPipeline{Pipeline: resolved, Resolutions: resolutions, Inputs: inputs}, nil
}

func (s *service) resolveStepDataSource(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug slug.Slug,
	step *pipelinespec.Step,
	stepIndex int,
	field string,
	refSlug slug.Slug,
	operationType string,
	requiredCapability model.DataSourceRefCapability,
) (model.ResolvedPipelineDataSourceRef, error) {
	if _, err := s.repo.FindDataSourceRefBySlug(ctx, tx, orgSlug, projectSlug, pipelineSlug, refSlug); err != nil {
		return model.ResolvedPipelineDataSourceRef{}, stepResolutionError(
			err,
			stepIndex,
			step.Slug,
			field,
			refSlug,
			environmentSlug,
			requiredCapability,
			fmt.Sprintf("data source ref %s does not exist for pipeline %s", refSlug, pipelineSlug),
		)
	}
	binding, err := s.repo.ResolveEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
	if err != nil {
		return model.ResolvedPipelineDataSourceRef{}, stepResolutionError(
			err,
			stepIndex,
			step.Slug,
			field,
			refSlug,
			environmentSlug,
			requiredCapability,
			fmt.Sprintf("data source binding %s is not configured for environment %s", refSlug, environmentSlug),
		)
	}
	modelBinding := toModelBinding(binding)
	candidateTypes := candidateDataSourceTypes(strings.TrimSpace(operationType), requiredCapability)
	if err := validateResolvedBinding(modelBinding, requiredCapability, strings.TrimSpace(operationType), candidateTypes); err != nil {
		return model.ResolvedPipelineDataSourceRef{}, stepResolutionErrorForBinding(
			err,
			stepIndex,
			step.Slug,
			field,
			modelBinding,
			candidateTypes,
			requiredCapability,
		)
	}
	return model.ResolvedPipelineDataSourceRef{
		BindingRevision: modelBinding.UpdatedAt.UTC().Format(time.RFC3339Nano),
		PipelineSlug:    pipelineSlug,
		RefSlug:         refSlug,
		DataSourceSlug:  binding.DataSourceSlug,
		DataSourceType:  binding.DataSourceType,
	}, nil
}

func (s *service) requireEnabledProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) error {
	enabled, err := s.repo.ProjectEnvironmentEnabled(ctx, tx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrProjectEnvironmentNotEnabled
	}
	return nil
}

func validateRefCapabilities(dsType model.DataSourceType, required []model.DataSourceRefCapability) error {
	if dsType == model.DataSourceTypeUnknown {
		return ErrInvalidDataSourceRefInput.Detail("data_source_type is required")
	}
	descriptor, ok := catalog.DescriptorFor(dsType)
	if !ok {
		return ErrDataSourceRefIncompatible.Detailf("unsupported data_source_type %s", dsType)
	}
	for _, capability := range required {
		switch capability {
		case model.DataSourceRefCapabilityPipelineSource:
			if !descriptor.SupportsPipelineSource {
				return ErrDataSourceRefIncompatible.Detailf("%s does not support pipeline_source", dsType)
			}
		case model.DataSourceRefCapabilityPipelineSink:
			if !descriptor.SupportsPipelineSink {
				return ErrDataSourceRefIncompatible.Detailf("%s does not support pipeline_sink", dsType)
			}
		default:
			return ErrInvalidDataSourceRefInput.Detail("unsupported data source ref capability")
		}
	}
	return nil
}

func validateConcreteTarget(ref *model.DataSourceRef, target *datasourcessvc.DataSource) error {
	if ref == nil {
		return ErrDataSourceRefNotFound
	}
	if target == nil {
		return datasourcessvc.ErrDataSourceNotFound
	}
	if !target.IsActive {
		return ErrDataSourceBindingUnavailable.Detailf("data source %s is inactive", target.Slug)
	}
	if ref.DataSourceType != model.DataSourceTypeUnknown && target.Type != ref.DataSourceType {
		return ErrDataSourceRefIncompatible.Detailf(
			"data source %s type %s does not match ref %s type %s",
			target.Slug,
			target.Type,
			ref.Slug,
			ref.DataSourceType,
		)
	}
	if err := validateRefCapabilities(target.Type, ref.RequiredCapabilities); err != nil {
		return err
	}
	if target.ReadOnly && hasCapability(ref.RequiredCapabilities, model.DataSourceRefCapabilityPipelineSink) {
		return datasourcessvc.ErrDataSourceReadOnly.Detailf("data source %s is read-only", target.Slug)
	}
	return nil
}

func validateResolvedBinding(
	binding *model.DataSourceBinding,
	requiredCapability model.DataSourceRefCapability,
	operationType string,
	candidateTypes []model.DataSourceType,
) error {
	if binding == nil {
		return ErrDataSourceBindingNotFound
	}
	if !binding.DataSourceActive {
		return ErrDataSourceBindingUnavailable.Detailf("data source %s is inactive", binding.DataSourceSlug)
	}
	if !hasCapability(binding.RequiredCapabilities, requiredCapability) {
		return ErrDataSourceRefIncompatible.Detailf(
			"data source ref %s does not require %s",
			binding.DataSourceRefSlug,
			requiredCapability,
		)
	}
	if binding.DataSourceReadOnly && requiredCapability == model.DataSourceRefCapabilityPipelineSink {
		return datasourcessvc.ErrDataSourceReadOnly.Detailf("data source %s is read-only", binding.DataSourceSlug)
	}
	if len(candidateTypes) == 0 {
		return ErrDataSourceRefIncompatible.Detailf(
			"operation_type %q cannot satisfy required capability %s",
			operationType,
			requiredCapability,
		)
	}
	if !containsDataSourceType(candidateTypes, binding.DataSourceType) {
		return ErrDataSourceRefIncompatible.Detailf(
			"data source binding %s resolves to data_source_type %s, but operation_type %q requires one of: %s",
			binding.DataSourceRefSlug,
			binding.DataSourceType,
			operationType,
			dataSourceTypeList(candidateTypes),
		)
	}
	return validateRefCapabilities(binding.DataSourceType, []model.DataSourceRefCapability{requiredCapability})
}

func hasCapability(capabilities []model.DataSourceRefCapability, want model.DataSourceRefCapability) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}

func clonePipeline(pipeline *pipelinespec.Pipeline) (*pipelinespec.Pipeline, error) {
	if pipeline == nil {
		return nil, ErrInvalidDataSourceRefInput.Detail("pipeline is required")
	}
	payload, err := json.Marshal(pipeline)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	cloned, err := pipelinespec.Load("pipeline.json", payload)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return cloned, nil
}

func stepResolutionError(
	err error,
	stepIndex int,
	stepSlug slug.Slug,
	stepField string,
	refSlug slug.Slug,
	environmentSlug slug.Slug,
	requiredCapability model.DataSourceRefCapability,
	message string,
) error {
	return &resolutionError{
		err:     err,
		message: fmt.Sprintf(`validate steps: step[%d] "%s": %s`, stepIndex, stepSlug, message),
		diagnostic: ResolutionDiagnostic{
			Field:              fmt.Sprintf("steps[%d].%s", stepIndex, stepField),
			StepSlug:           &stepSlug,
			DataSourceRefSlug:  refSlug,
			DataSourceRef:      refSlug,
			EnvironmentSlug:    environmentSlug,
			RequiredCapability: requiredCapability,
		},
	}
}

func stepResolutionErrorForBinding(
	err error,
	stepIndex int,
	stepSlug slug.Slug,
	stepField string,
	binding *model.DataSourceBinding,
	candidateTypes []model.DataSourceType,
	requiredCapability model.DataSourceRefCapability,
) error {
	message := fmt.Sprintf("data source binding %s is invalid", binding.DataSourceRefSlug)
	expectedType := model.DataSourceTypeUnknown
	if len(candidateTypes) == 1 {
		expectedType = candidateTypes[0]
	}
	return &resolutionError{
		err:     err,
		message: fmt.Sprintf(`validate steps: step[%d] "%s": %s`, stepIndex, stepSlug, message),
		diagnostic: ResolutionDiagnostic{
			Field:                  fmt.Sprintf("steps[%d].%s", stepIndex, stepField),
			StepSlug:               &stepSlug,
			DataSourceRefSlug:      binding.DataSourceRefSlug,
			DataSourceRef:          binding.DataSourceRefSlug,
			ResolvedDataSourceSlug: binding.DataSourceSlug,
			EnvironmentSlug:        binding.EnvironmentSlug,
			DataSourceType:         binding.DataSourceType,
			ExpectedDataSourceType: expectedType,
			RequiredCapability:     requiredCapability,
		},
	}
}
