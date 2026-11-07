package datasourcerefssvc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type pipelineDataSourceRefRequirement struct {
	refSlug            slug.Slug
	stepSlug           slug.Slug
	stepIndex          int
	field              string
	operationType      string
	requiredCapability model.DataSourceRefCapability
	candidateTypes     []model.DataSourceType
}

type pipelineDataSourceRefRequirementGroup struct {
	refSlug              slug.Slug
	requirements         []pipelineDataSourceRefRequirement
	requiredCapabilities []model.DataSourceRefCapability
	candidateTypes       []model.DataSourceType
}

// SyncPipelineDataSourceRefs creates or safely expands refs declared by a spec without pruning other pipeline refs.
func (s *service) SyncPipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*PipelineDataSourceRefSyncResult, error) {
	var result *PipelineDataSourceRefSyncResult
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		result, err = s.syncPipelineDataSourceRefs(ctx, tx, orgSlug, projectSlug, pipeline, actor, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReconcilePipelineDataSourceRefs syncs refs declared by the canonical spec and prunes refs it no longer declares.
func (s *service) ReconcilePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*PipelineDataSourceRefSyncResult, error) {
	var result *PipelineDataSourceRefSyncResult
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		result, err = s.syncPipelineDataSourceRefs(ctx, tx, orgSlug, projectSlug, pipeline, actor, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteDataSourceRefsForPipeline removes all refs owned by a pipeline.
func (s *service) DeleteDataSourceRefsForPipeline(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.deleteDataSourceRefsForPipeline(ctx, tx, orgSlug, projectSlug, pipelineSlug, actor)
	})
}

// syncPipelineDataSourceRefs creates, updates, and prunes refs in a transaction.
func (s *service) syncPipelineDataSourceRefs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
	pruneStale bool,
) (*PipelineDataSourceRefSyncResult, error) {
	pipelineSlug, err := pipelineSlugFromSpec(pipeline)
	if err != nil {
		return nil, err
	}
	requirements, err := pipelineDataSourceRefRequirements(pipeline)
	if err != nil {
		return nil, err
	}
	groups, err := groupPipelineDataSourceRefRequirements(requirements)
	if err != nil {
		return nil, err
	}

	result := &PipelineDataSourceRefSyncResult{Items: []PipelineDataSourceRefSyncItem{}}
	desired := make(map[slug.Slug]struct{}, len(groups))

	for _, group := range groups {
		desired[group.refSlug] = struct{}{}
		item, err := s.syncPipelineDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, group, actor)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, item)
	}
	if pruneStale {
		if err := s.deleteStalePipelineDataSourceRefs(ctx, tx, orgSlug, projectSlug, pipelineSlug, desired, actor); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// deleteDataSourceRefsForPipeline removes all refs owned by a pipeline in a transaction.
func (s *service) deleteDataSourceRefsForPipeline(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	return s.deleteStalePipelineDataSourceRefs(ctx, tx, orgSlug, projectSlug, pipelineSlug, map[slug.Slug]struct{}{}, actor)
}

// pipelineSlugFromSpec validates that sync and resolution have a concrete pipeline owner.
func pipelineSlugFromSpec(pipeline *pipelinespec.Pipeline) (slug.Slug, error) {
	if pipeline == nil {
		return "", ErrInvalidDataSourceRefInput.Detail("pipeline is required")
	}
	if pipeline.Metadata.Slug == "" {
		return "", ErrInvalidDataSourceRefInput.Detail("pipeline metadata.slug is required")
	}
	return pipeline.Metadata.Slug, nil
}

// pipelineDataSourceRefRequirements extracts source and sink ref requirements from the spec.
func pipelineDataSourceRefRequirements(
	pipeline *pipelinespec.Pipeline,
) ([]pipelineDataSourceRefRequirement, error) {
	if pipeline == nil {
		return nil, ErrInvalidDataSourceRefInput.Detail("pipeline is required")
	}

	requirements := make([]pipelineDataSourceRefRequirement, 0)
	for i := range pipeline.Steps {
		step := pipeline.Steps[i]
		if step.Query != nil && step.Query.DataSourceSlug != "" {
			req := newPipelineDataSourceRefRequirement(
				step.Query.DataSourceSlug,
				step.Slug,
				i,
				"query",
				step.Query.OperationType,
				model.DataSourceRefCapabilityPipelineSource,
			)
			if len(req.candidateTypes) == 0 {
				return nil, stepDataSourceRefSyncError(
					ErrDataSourceRefIncompatible,
					req,
					fmt.Sprintf(
						"operation_type %q cannot satisfy required capability %s",
						req.operationType,
						req.requiredCapability,
					),
				)
			}
			requirements = append(requirements, req)
		}
		if step.Sink != nil && step.Sink.DataSourceSlug != "" {
			req := newPipelineDataSourceRefRequirement(
				step.Sink.DataSourceSlug,
				step.Slug,
				i,
				"sink",
				step.Sink.OperationType,
				model.DataSourceRefCapabilityPipelineSink,
			)
			if len(req.candidateTypes) == 0 {
				return nil, stepDataSourceRefSyncError(
					ErrDataSourceRefIncompatible,
					req,
					fmt.Sprintf(
						"operation_type %q cannot satisfy required capability %s",
						req.operationType,
						req.requiredCapability,
					),
				)
			}
			requirements = append(requirements, req)
		}
	}
	return requirements, nil
}

// newPipelineDataSourceRefRequirement builds one normalized ref requirement.
func newPipelineDataSourceRefRequirement(
	refSlug slug.Slug,
	stepSlug slug.Slug,
	stepIndex int,
	field string,
	operationType string,
	requiredCapability model.DataSourceRefCapability,
) pipelineDataSourceRefRequirement {
	return pipelineDataSourceRefRequirement{
		refSlug:            refSlug,
		stepSlug:           stepSlug,
		stepIndex:          stepIndex,
		field:              field,
		operationType:      strings.TrimSpace(operationType),
		requiredCapability: requiredCapability,
		candidateTypes:     candidateDataSourceTypes(strings.TrimSpace(operationType), requiredCapability),
	}
}

// groupPipelineDataSourceRefRequirements merges repeated refs into one sync contract.
func groupPipelineDataSourceRefRequirements(
	requirements []pipelineDataSourceRefRequirement,
) ([]pipelineDataSourceRefRequirementGroup, error) {
	byRef := map[slug.Slug][]pipelineDataSourceRefRequirement{}
	for _, req := range requirements {
		byRef[req.refSlug] = append(byRef[req.refSlug], req)
	}

	refSlugs := make([]slug.Slug, 0, len(byRef))
	for refSlug := range byRef {
		refSlugs = append(refSlugs, refSlug)
	}
	sort.Slice(refSlugs, func(i, j int) bool {
		return refSlugs[i].String() < refSlugs[j].String()
	})

	groups := make([]pipelineDataSourceRefRequirementGroup, 0, len(refSlugs))
	for _, refSlug := range refSlugs {
		reqs := byRef[refSlug]
		candidates := append([]model.DataSourceType(nil), reqs[0].candidateTypes...)
		for _, req := range reqs[1:] {
			candidates = intersectDataSourceTypes(candidates, req.candidateTypes)
		}
		if len(candidates) == 0 {
			return nil, stepDataSourceRefSyncError(
				ErrDataSourceRefIncompatible,
				reqs[0],
				fmt.Sprintf(
					"data source ref %s has incompatible operation_type requirements",
					refSlug,
				),
			)
		}
		groups = append(groups, pipelineDataSourceRefRequirementGroup{
			refSlug:              refSlug,
			requirements:         reqs,
			requiredCapabilities: requiredCapabilitiesForRequirements(reqs),
			candidateTypes:       candidates,
		})
	}
	return groups, nil
}

// syncPipelineDataSourceRef applies one grouped requirement to persistence.
func (s *service) syncPipelineDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	group pipelineDataSourceRefRequirementGroup,
	actor model.EventActor,
) (PipelineDataSourceRefSyncItem, error) {
	repoRef, err := s.repo.FindDataSourceRefBySlug(ctx, tx, orgSlug, projectSlug, pipelineSlug, group.refSlug)
	if err != nil {
		if xerror.Is(err, ErrDataSourceRefNotFound) {
			return s.createSyncedPipelineDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, group, actor)
		}
		return PipelineDataSourceRefSyncItem{}, err
	}
	existing := toModelRef(repoRef)
	return s.syncExistingPipelineDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, existing, group, actor)
}

// createSyncedPipelineDataSourceRef creates a missing ref without guessing concrete targets.
func (s *service) createSyncedPipelineDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	group pipelineDataSourceRefRequirementGroup,
	actor model.EventActor,
) (PipelineDataSourceRefSyncItem, error) {
	dsType := model.DataSourceTypeUnknown
	if len(group.candidateTypes) == 1 {
		dsType = group.candidateTypes[0]
	}

	params := datasourcerefs.CreateDataSourceRefParams{
		Slug:                 group.refSlug,
		Name:                 group.refSlug.String(),
		DataSourceType:       dsType,
		RequiredCapabilities: group.requiredCapabilities,
	}
	repoRef, err := s.repo.CreateDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, params)
	if err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}
	created := toModelRef(repoRef)
	if err := s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, created, model.EventActionCreate); err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}
	return pipelineDataSourceRefSyncItem(created, PipelineDataSourceRefSyncStatusCreated), nil
}

// syncExistingPipelineDataSourceRef validates or expands an existing logical ref.
func (s *service) syncExistingPipelineDataSourceRef(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	existing *model.DataSourceRef,
	group pipelineDataSourceRefRequirementGroup,
	actor model.EventActor,
) (PipelineDataSourceRefSyncItem, error) {
	if err := validateExistingRefRequirements(existing, group.requirements); err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}

	nextCapabilities := mergeDataSourceRefCapabilities(
		existing.RequiredCapabilities,
		group.requiredCapabilities,
	)
	if sameDataSourceRefCapabilities(existing.RequiredCapabilities, nextCapabilities) {
		return pipelineDataSourceRefSyncItem(existing, PipelineDataSourceRefSyncStatusUnchanged), nil
	}
	diagnosticReq := firstNewCapabilityRequirement(existing.RequiredCapabilities, group.requirements)
	if err := s.validateSafeCapabilityExpansion(ctx, tx, orgSlug, projectSlug, pipelineSlug, existing, nextCapabilities, diagnosticReq); err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}

	repoRef, err := s.repo.UpdateDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, existing.Slug, datasourcerefs.UpdateDataSourceRefParams{
		Name:                 existing.Name,
		DataSourceType:       existing.DataSourceType,
		RequiredCapabilities: nextCapabilities,
	})
	if err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}
	updated := toModelRef(repoRef)
	if err := s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, updated, model.EventActionUpdate); err != nil {
		return PipelineDataSourceRefSyncItem{}, err
	}
	return pipelineDataSourceRefSyncItem(updated, PipelineDataSourceRefSyncStatusUpdated), nil
}

// deleteStalePipelineDataSourceRefs removes pipeline-owned refs no longer declared by the spec.
func (s *service) deleteStalePipelineDataSourceRefs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	desired map[slug.Slug]struct{},
	actor model.EventActor,
) error {
	existingRefs, err := s.repo.ListDataSourceRefsByPipeline(ctx, tx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return err
	}
	for _, ref := range existingRefs {
		if _, ok := desired[ref.Slug]; ok {
			continue
		}
		protections, err := s.repo.ListProtectedBindingEnvironmentsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, ref.Slug)
		if err != nil {
			return err
		}
		if len(protections) > 0 {
			return ErrInvalidDataSourceRefInput.Detailf(
				"data source ref %s is bound in protected environment %s; delete that binding with protected confirmation before removing the ref from pipeline %s",
				ref.Slug,
				protections[0].EnvironmentSlug,
				pipelineSlug,
			)
		}
		if err := s.repo.DeleteDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, ref.Slug); err != nil {
			return err
		}
		if err := s.recordDataSourceRefEvent(ctx, tx, orgSlug, projectSlug, actor, toModelRef(ref), model.EventActionDelete); err != nil {
			return err
		}
	}
	return nil
}

// validateExistingRefRequirements checks that an existing ref matches spec operation types.
func validateExistingRefRequirements(
	existing *model.DataSourceRef,
	requirements []pipelineDataSourceRefRequirement,
) error {
	if existing.DataSourceType == model.DataSourceTypeUnknown {
		return nil
	}
	_, ok := catalog.DescriptorFor(existing.DataSourceType)
	if !ok {
		return stepDataSourceRefSyncError(
			ErrDataSourceRefIncompatible,
			requirements[0],
			fmt.Sprintf("data source ref %s uses unsupported data_source_type %s", existing.Slug, existing.DataSourceType),
		)
	}

	for _, req := range requirements {
		if !containsDataSourceType(req.candidateTypes, existing.DataSourceType) {
			return stepDataSourceRefSyncError(
				ErrDataSourceRefIncompatible,
				req,
				fmt.Sprintf(
					"data source ref %s uses data_source_type %s, but operation_type %q requires one of: %s",
					existing.Slug,
					existing.DataSourceType,
					req.operationType,
					dataSourceTypeList(req.candidateTypes),
				),
			)
		}
		if err := validateRefCapabilities(existing.DataSourceType, []model.DataSourceRefCapability{req.requiredCapability}); err != nil {
			return stepDataSourceRefSyncError(err, req, err.Error())
		}
	}
	return nil
}

// firstNewCapabilityRequirement selects the step that requires capability widening.
func firstNewCapabilityRequirement(
	existingCapabilities []model.DataSourceRefCapability,
	requirements []pipelineDataSourceRefRequirement,
) pipelineDataSourceRefRequirement {
	for _, req := range requirements {
		if !hasCapability(existingCapabilities, req.requiredCapability) {
			return req
		}
	}
	if len(requirements) == 0 {
		return pipelineDataSourceRefRequirement{}
	}
	return requirements[0]
}

// validateSafeCapabilityExpansion preserves existing binding safety before widening a ref.
func (s *service) validateSafeCapabilityExpansion(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	existing *model.DataSourceRef,
	nextCapabilities []model.DataSourceRefCapability,
	diagnosticReq pipelineDataSourceRefRequirement,
) error {
	protections, err := s.repo.ListProtectedBindingEnvironmentsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, existing.Slug)
	if err != nil {
		return err
	}
	if len(protections) > 0 {
		return stepDataSourceRefSyncError(
			ErrDataSourceRefIncompatible,
			diagnosticReq,
			fmt.Sprintf(
				"data source ref %s is bound in protected environment %s; update the ref contract with protected confirmation before deploy sync can expand capabilities",
				existing.Slug,
				protections[0].EnvironmentSlug,
			),
		)
	}

	bindings, err := s.repo.ListEnvironmentBindingsForDataSourceRef(ctx, tx, orgSlug, projectSlug, pipelineSlug, existing.Slug)
	if err != nil {
		return err
	}
	nextRef := &model.DataSourceRef{
		PipelineSlug:         pipelineSlug,
		Slug:                 existing.Slug,
		Name:                 existing.Name,
		DataSourceType:       existing.DataSourceType,
		RequiredCapabilities: nextCapabilities,
	}
	for _, binding := range bindings {
		target := &datasourcessvc.DataSource{
			Slug:     binding.DataSourceSlug,
			Name:     binding.DataSourceName,
			Type:     binding.DataSourceType,
			IsActive: binding.DataSourceActive,
			ReadOnly: binding.DataSourceReadOnly,
		}
		if err := validateConcreteTarget(nextRef, target); err != nil {
			return stepDataSourceRefSyncError(err, diagnosticReq, err.Error())
		}
	}
	return nil
}

// candidateDataSourceTypes returns catalog data-source types matching a step requirement.
func candidateDataSourceTypes(
	operationType string,
	requiredCapability model.DataSourceRefCapability,
) []model.DataSourceType {
	candidates := make([]model.DataSourceType, 0)
	for _, descriptor := range catalog.AllDescriptors() {
		if descriptor.QueryOperationType != operationType {
			continue
		}
		if requiredCapability == model.DataSourceRefCapabilityPipelineSource && !descriptor.SupportsPipelineSource {
			continue
		}
		if requiredCapability == model.DataSourceRefCapabilityPipelineSink && !descriptor.SupportsPipelineSink {
			continue
		}
		candidates = append(candidates, descriptor.Type)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].String() < candidates[j].String()
	})
	return candidates
}

// containsDataSourceType reports whether a candidate list includes the type.
func containsDataSourceType(candidates []model.DataSourceType, want model.DataSourceType) bool {
	for _, candidate := range candidates {
		if candidate == want {
			return true
		}
	}
	return false
}

// requiredCapabilitiesForRequirements returns source/sink capabilities in stable order.
func requiredCapabilitiesForRequirements(
	requirements []pipelineDataSourceRefRequirement,
) []model.DataSourceRefCapability {
	required := make([]model.DataSourceRefCapability, 0, 2)
	for _, capability := range []model.DataSourceRefCapability{
		model.DataSourceRefCapabilityPipelineSource,
		model.DataSourceRefCapabilityPipelineSink,
	} {
		for _, req := range requirements {
			if req.requiredCapability == capability {
				required = append(required, capability)
				break
			}
		}
	}
	return required
}

// mergeDataSourceRefCapabilities returns a stable union of existing and inferred capabilities.
func mergeDataSourceRefCapabilities(
	existing []model.DataSourceRefCapability,
	required []model.DataSourceRefCapability,
) []model.DataSourceRefCapability {
	merged := append([]model.DataSourceRefCapability(nil), existing...)
	for _, capability := range required {
		if !hasCapability(merged, capability) {
			merged = append(merged, capability)
		}
	}
	return orderDataSourceRefCapabilities(merged)
}

// orderDataSourceRefCapabilities keeps capability output deterministic.
func orderDataSourceRefCapabilities(
	capabilities []model.DataSourceRefCapability,
) []model.DataSourceRefCapability {
	ordered := make([]model.DataSourceRefCapability, 0, len(capabilities))
	for _, capability := range []model.DataSourceRefCapability{
		model.DataSourceRefCapabilityPipelineSource,
		model.DataSourceRefCapabilityPipelineSink,
	} {
		if hasCapability(capabilities, capability) {
			ordered = append(ordered, capability)
		}
	}
	return ordered
}

// sameDataSourceRefCapabilities compares normalized source/sink capability sets.
func sameDataSourceRefCapabilities(
	left []model.DataSourceRefCapability,
	right []model.DataSourceRefCapability,
) bool {
	left = orderDataSourceRefCapabilities(left)
	right = orderDataSourceRefCapabilities(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// intersectDataSourceTypes returns the stable type intersection for repeated ref use.
func intersectDataSourceTypes(
	left []model.DataSourceType,
	right []model.DataSourceType,
) []model.DataSourceType {
	out := make([]model.DataSourceType, 0, len(left))
	for _, leftType := range left {
		for _, rightType := range right {
			if leftType == rightType {
				out = append(out, leftType)
				break
			}
		}
	}
	return out
}

// dataSourceTypeList formats candidate types for operator diagnostics.
func dataSourceTypeList(types []model.DataSourceType) string {
	names := make([]string, len(types))
	for i, dsType := range types {
		names[i] = dsType.String()
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// pipelineDataSourceRefSyncItem maps a ref into the public sync result shape.
func pipelineDataSourceRefSyncItem(
	ref *model.DataSourceRef,
	status PipelineDataSourceRefSyncStatus,
) PipelineDataSourceRefSyncItem {
	return PipelineDataSourceRefSyncItem{
		PipelineSlug:         ref.PipelineSlug,
		RefSlug:              ref.Slug,
		DataSourceType:       ref.DataSourceType,
		RequiredCapabilities: orderDataSourceRefCapabilities(ref.RequiredCapabilities),
		Status:               status,
	}
}

// stepDataSourceRefSyncError wraps sync failures so validation can point at the spec step.
func stepDataSourceRefSyncError(
	err error,
	req pipelineDataSourceRefRequirement,
	message string,
) error {
	return &resolutionError{
		err:     err,
		message: fmt.Sprintf(`validate steps: step[%d] "%s": %s`, req.stepIndex, req.stepSlug, message),
		diagnostic: ResolutionDiagnostic{
			Field:              fmt.Sprintf("steps[%d].%s", req.stepIndex, req.field),
			StepSlug:           &req.stepSlug,
			DataSourceRefSlug:  req.refSlug,
			DataSourceRef:      req.refSlug,
			RequiredCapability: req.requiredCapability,
		},
	}
}
