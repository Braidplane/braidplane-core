package pipelinesvc

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
)

// resolveDeployment never chooses a compiled plan by authored hash. Rollback
// reloads the old bytes and revalidates them without changing any run state.
func (s *service) resolveDeployment(ctx context.Context, db postgres.DBTX, org, project, environment slug.Slug, version *pipelinesrepo.PipelineVersion, req DeployPipelineRequest, revalidateOnly bool) (string, error) {
	if req.CompiledPlanDigest == nil && req.ResolutionDigest == nil {
		if len(req.ResolutionInputs) > 0 {
			return "", deploymentplan.ErrInvalid
		}
		pipeline, err := pipelineFromVersion(version)
		if err != nil {
			return "", err
		}
		if pipeline.UsesContractComposition() {
			return "", deploymentplan.ErrInvalid
		}
		return "", nil
	}
	if req.ResolutionDigest != nil && len(req.ResolutionInputs) > 0 {
		return "", deploymentplan.ErrInvalid
	}
	if req.ExpectedRevision == nil || req.CompiledPlanDigest != nil && req.ResolutionDigest != nil {
		return "", deploymentplan.ErrInvalid
	}
	var old *deploymentplan.Plan
	compiledID := req.CompiledPlanDigest
	if req.ResolutionDigest != nil {
		raw, err := s.resolutions.Find(ctx, db, org, project, environment, version.PipelineSlug, *req.ResolutionDigest)
		if err != nil {
			return "", deploymentplan.ErrInvalid.With(err)
		}
		old, err = deploymentplan.Read(raw, *req.ResolutionDigest)
		if err != nil || !old.Matches(org, project, environment, version.PipelineSlug) {
			return "", deploymentplan.ErrInvalid.With(err)
		}
		compiledID = &old.TemplateDigest
	}
	raw, err := s.reloadCompiledPlan(ctx, db, org, project, version, *compiledID)
	if err != nil {
		return "", err
	}
	var executableID string
	inputs := req.ResolutionInputs
	if old != nil {
		raw, executableID, inputs = old.CompiledPlan, old.CompiledDigest, old.ResolutionInputs
	} else {
		raw, executableID, err = contractplan.Specialize(raw, *compiledID, inputs)
		if err != nil {
			return "", err
		}
	}
	compiled, err := contractplan.Read(raw, executableID)
	if err != nil {
		return "", err
	}

	portable, err := compiled.RuntimePipeline()
	if err != nil {
		return "", err
	}
	if err := pipelinevalidate.Validate(portable); err != nil {
		return "", err
	}
	resolved, err := s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(ctx, org, project, environment, portable)
	if err != nil {
		return "", err
	}
	if resolved == nil || resolved.Pipeline == nil {
		return "", deploymentplan.ErrInvalid
	}
	plan := deploymentplan.Plan{TemplateDigest: *compiledID, ResolutionInputs: inputs, Inputs: resolved.Inputs, Organization: org, Project: project, Environment: environment, CompiledDigest: executableID, CompiledPlan: raw, Pipeline: resolved.Pipeline}
	for i, step := range resolved.Pipeline.Steps {
		for _, role := range []string{"query", "sink"} {
			var target deploymentplan.Target
			target.Step, target.Role = step.Slug, role
			if role == "query" && step.Query != nil {
				target.Reference, target.DataSource = portable.Steps[i].Query.DataSourceSlug, step.Query.DataSourceSlug
			}
			if role == "sink" && step.Sink != nil {
				target.Reference, target.DataSource = portable.Steps[i].Sink.DataSourceSlug, step.Sink.DataSourceSlug
			}
			if target.DataSource == "" {
				continue
			}
			for _, binding := range resolved.Resolutions {
				if binding.RefSlug == target.Reference && binding.DataSourceSlug == target.DataSource {
					target.BindingRevision = binding.BindingRevision
				}
			}
			ds, err := s.dataSourceService.FindBySlug(ctx, org, target.DataSource)
			if err != nil || ds == nil || !ds.IsActive || role == "sink" && ds.ReadOnly || (ds.Credential == nil || ds.Credential.Status != credentialssvc.CredentialStatusActive) {
				return "", deploymentplan.ErrInvalid.With(err)
			}
			for _, operation := range compiled.Operations {
				if operation.Step == step.Slug && operation.Role == role && operation.DataSourceType != model.DataSourceTypeUnknown && operation.DataSourceType != ds.Type {
					return "", deploymentplan.ErrInvalid
				}
			}
			target.ConfigDigest, err = connector.PublicConfigDigest(ds.Type, ds.Config)
			if err != nil {
				return "", deploymentplan.ErrInvalid.With(err)
			}
			plan.Targets = append(plan.Targets, target)
		}
	}
	if old != nil {
		if revalidateOnly {
			plan.RevalidatedFrom = old.RevalidatedFrom
		} else {
			if !sameExecutableResolution(old, &plan) {
				return "", deploymentplan.ErrInvalid
			}
			plan.RevalidatedFrom = *req.ResolutionDigest
		}
	}
	materialization, id, err := deploymentplan.Encode(plan)
	if err != nil {
		return "", err
	}
	if old != nil && revalidateOnly && id != *req.ResolutionDigest {
		return "", deploymentplan.ErrInvalid
	}
	if old == nil || !revalidateOnly {
		stored, err := s.resolutions.Create(ctx, db, org, project, environment, version.PipelineSlug, *compiledID, id, materialization)
		if xerror.Is(err, deploymentresolutions.ErrNotFound) {
			stored, err = s.resolutions.Find(ctx, db, org, project, environment, version.PipelineSlug, id)
		}
		if err != nil {
			return "", err
		}
		if !bytes.Equal(stored, materialization) {
			return "", deploymentplan.ErrInvalid
		}
	}
	return id, nil
}

func (s *service) reloadCompiledPlan(ctx context.Context, db postgres.DBTX, org, project slug.Slug, version *pipelinesrepo.PipelineVersion, digest string) ([]byte, error) {
	stored, err := s.compiledPlans.Find(ctx, db, org, project, version.PipelineSlug, digest)
	if err != nil {
		return nil, deploymentplan.ErrInvalid.With(err)
	}
	plan, err := contractplan.Read(stored.Materialization, digest)
	if err != nil || stored.Digest != digest || plan.AuthoredSpecHash != version.SpecHash || stored.AuthoredSpecHash != version.SpecHash || stored.FormatVersion != plan.FormatVersion {
		return nil, deploymentplan.ErrInvalid.With(err)
	}
	var authored struct {
		Metadata struct {
			Slug slug.Slug `json:"slug"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(plan.Authored, &authored); err != nil || authored.Metadata.Slug != version.PipelineSlug {
		return nil, deploymentplan.ErrInvalid.With(err)
	}
	return stored.Materialization, nil
}

func (s *service) executionResolution(ctx context.Context, org, project, environment slug.Slug, version *pipelinesrepo.PipelineVersion, id string) ([]byte, error) {
	var raw []byte
	err := s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		var err error
		raw, err = s.resolutions.Find(ctx, db, org, project, environment, version.PipelineSlug, id)
		if err != nil {
			return deploymentplan.ErrInvalid.With(err)
		}
		// Revalidation must produce the same target identity. It cannot repair
		// missing history from current bindings, operations or catalog content.
		revision := int64(0)
		_, err = s.resolveDeployment(ctx, db, org, project, environment, version, DeployPipelineRequest{ResolutionDigest: &id, ExpectedRevision: &revision}, true)
		return err
	})
	return raw, err
}

func sameExecutableResolution(old, current *deploymentplan.Plan) bool {
	before, err := json.Marshal(old.Pipeline)
	if err != nil {
		return false
	}
	after, err := json.Marshal(current.Pipeline)
	if err != nil || old.CompiledDigest != current.CompiledDigest || !bytes.Equal(before, after) || len(old.Targets) != len(current.Targets) || len(old.Inputs) != len(current.Inputs) {
		return false
	}
	for _, a := range old.Targets {
		found := false
		for _, b := range current.Targets {
			if a.Step == b.Step && a.Role == b.Role && a.Reference == b.Reference && a.DataSource == b.DataSource && a.ConfigDigest == b.ConfigDigest {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for i, a := range old.Inputs {
		b := current.Inputs[i]
		if a.Key != b.Key || a.Type != b.Type || a.Value != b.Value {
			return false
		}
	}
	return true
}

// Selected resolutions own executable pins; flat snapshots are only for unselected legacy versions.
func (s *service) executionPipeline(ctx context.Context, org, project, environment slug.Slug, version *pipelinesrepo.PipelineVersion, resolutionDigest *string) (*pipelinespec.Pipeline, []byte, error) {
	if resolutionDigest != nil {
		raw, err := s.executionResolution(ctx, org, project, environment, version, *resolutionDigest)
		if err != nil {
			return nil, nil, err
		}
		resolution, err := deploymentplan.Read(raw, *resolutionDigest)
		if err != nil {
			return nil, nil, err
		}
		return resolution.Pipeline, raw, nil
	}
	pipeline, err := pipelineFromVersion(version)
	if err != nil {
		return nil, nil, err
	}
	if pipeline.UsesContractComposition() {
		return nil, nil, deploymentplan.ErrInvalid
	}
	return pipeline, nil, nil
}

// ValidateCompletionExecutionContext reuses exact deployment revalidation without
// reading the current deployment pointer or selecting a replacement version.
func (s *service) ValidateCompletionExecutionContext(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot) error {
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, org, project, snapshot.PipelineSlug, snapshot.VersionHash)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
			return deploymentplan.ErrInvalid.With(err)
		}
		return err
	}
	raw, err := s.executionResolution(ctx, org, project, environment, version, snapshot.ResolutionDigest)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, snapshot.Materialization) {
		return deploymentplan.ErrInvalid
	}
	plan, err := deploymentplan.Read(raw, snapshot.ResolutionDigest)
	if err != nil || !plan.Matches(org, project, environment, snapshot.PipelineSlug) {
		return deploymentplan.ErrInvalid.With(err)
	}
	for _, step := range plan.Pipeline.Steps {
		if step.Input != nil {
			return xerror.InvalidInput(xerror.PipelineTrigger).Detail("completion triggers do not supply event input")
		}
	}
	// Completion ordering supplies no payload or per-run input mapping.
	if err := pipelineruntimevars.ResolvePipelineInput(plan.Pipeline, nil); err != nil {
		return err
	}
	return nil
}
