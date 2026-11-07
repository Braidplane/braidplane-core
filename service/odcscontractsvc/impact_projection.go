package odcscontractsvc

import (
	"context"
	"encoding/hex"
	"reflect"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
)

var ErrImpactSelection = xerror.New(xerror.KindInvalidInput, "ODCS_IMPACT_SELECTION_UNAVAILABLE", "select two supported retained compiled plans containing the exact requested contract revisions")

func (s *service) selectedImpactInputs(ctx context.Context, db postgres.DBTX, org, project, contract slug.Slug, revision *model.ODCSRevision, selection *ImpactPlanSelection) (map[string]*odcs.ProjectedInput, error) {
	if _, err := slug.New(selection.Pipeline.String()); err != nil || len(selection.Digest) != 71 || !strings.HasPrefix(selection.Digest, "sha256:") {
		return nil, ErrImpactSelection.With(err)
	}
	if _, err := hex.DecodeString(selection.Digest[7:]); err != nil {
		return nil, ErrImpactSelection.With(err)
	}
	stored, err := s.plans.Find(ctx, db, org, project, selection.Pipeline, selection.Digest)
	if err != nil {
		if xerror.Is(err, compiledplans.ErrNotFound) {
			return nil, ErrImpactSelection.With(err)
		}
		return nil, err
	}
	plan, err := contractplan.Read(stored.Materialization, selection.Digest)
	if err != nil || plan.Projection == nil || stored.PipelineSlug != selection.Pipeline || stored.Digest != selection.Digest || stored.AuthoredSpecHash != plan.AuthoredSpecHash || stored.FormatVersion != plan.FormatVersion {
		return nil, ErrImpactSelection.With(err)
	}
	pipeline, err := plan.RuntimePipeline()
	if err != nil || pipeline.Metadata.Slug != selection.Pipeline {
		return nil, ErrImpactSelection.With(err)
	}
	inputs := map[string]*odcs.ProjectedInput{}
	if selection.ProjectionInput != nil && (*selection.ProjectionInput < 0 || *selection.ProjectionInput >= len(plan.Projection.Inputs)) {
		return nil, ErrImpactSelection
	}
	for i := range plan.Projection.Inputs {
		if selection.ProjectionInput != nil && i != *selection.ProjectionInput {
			continue
		}
		input := &plan.Projection.Inputs[i]
		if input.Contract != contract.String() || input.Revision != revision.Slug.String() {
			continue
		}
		if input.Digest != revision.Digest || input.Structure == nil || inputs[input.ObjectID] != nil {
			return nil, ErrImpactSelection
		}
		inputs[input.ObjectID] = input
	}
	if len(inputs) == 0 {
		return nil, ErrImpactSelection
	}
	return inputs, nil
}

func (s *service) previewSelectedProjectionImpact(ctx context.Context, db postgres.DBTX, org, project, contract slug.Slug, before, after *model.ODCSRevision, request RevisionImpactRequest, result *RevisionImpact) error {
	old, err := s.selectedImpactInputs(ctx, db, org, project, contract, before, request.FromPlan)
	if err != nil {
		return err
	}
	current, err := s.selectedImpactInputs(ctx, db, org, project, contract, after, request.ToPlan)
	if err != nil {
		return err
	}
	result.FromPlan, result.ToPlan = request.FromPlan, request.ToPlan
	ids := make([]string, 0, len(old)+len(current))
	for id := range old {
		ids = append(ids, id)
	}
	for id := range current {
		if old[id] == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		object, err := compareProjectedObject(id, old[id], current[id])
		if err != nil {
			return err
		}
		result.Objects = append(result.Objects, object)
	}
	return nil
}

func compareProjectedObject(id string, before, after *odcs.ProjectedInput) (ObjectImpact, error) {
	result := ObjectImpact{SchemaObjectID: id, Scope: "selected_projection", Comparison: "supported", Changes: []ImpactChange{}, Diagnostics: []ImpactDiagnostic{}}
	if before == nil || after == nil {
		result.Comparison = "unsupported"
		result.Diagnostics = append(result.Diagnostics, ImpactDiagnostic{Diagnostic: odcs.Diagnostic{Code: "projection_not_selected", Path: "$", Message: "Select this exact object in both retained plans before comparing its executable meaning."}})
		return result, nil
	}
	result.richBefore, result.richAfter = before.Structure, after.Structure
	matches, err := payload.MatchStructuralDeclarations(before.Structure, after.Structure, payload.DefaultStructuralLimits())
	if err != nil {
		return result, ErrImpactSelection.With(err)
	}
	add := func(kind, path, old, current string, severity drift.ReportSeverity, reason string, match payload.StructuralMatch) {
		change := ImpactChange{Change: drift.Change{Kind: kind, Path: path, Before: old, After: current, Severity: severity, ReasonCode: reason}}
		if match.Before != nil {
			change.BeforePropertyID = match.Before.ID
		}
		if match.After != nil {
			change.AfterPropertyID = match.After.ID
		}
		result.Changes = append(result.Changes, change)
	}
	if before.Structure.Representation != after.Structure.Representation || before.Structure.RootShape != after.Structure.RootShape {
		add("representation_change", "$", before.Structure.Representation.String()+"/"+before.Structure.RootShape.String(), after.Structure.Representation.String()+"/"+after.Structure.RootShape.String(), drift.ReportSeverityBreaking, "structural_encoding_changed", payload.StructuralMatch{})
	}
	if !reflect.DeepEqual(before.Mapping, after.Mapping) {
		add("mapping_change", "$", "recorded", "changed", drift.ReportSeverityWarning, "projection_mapping_changed", payload.StructuralMatch{})
	}
	if !reflect.DeepEqual(before.Resolutions, after.Resolutions) || !reflect.DeepEqual(before.Defaults, after.Defaults) {
		add("resolution_change", "$", "recorded", "changed", drift.ReportSeverityWarning, "projection_resolution_changed", payload.StructuralMatch{})
	}
	for _, match := range matches {
		switch {
		case match.Before == nil:
			add("declaration_added", match.After.Path.String(), "absent", "declared", drift.ReportSeverityWarning, "structural_declaration_added", match)
		case match.After == nil:
			add("declaration_removed", match.Before.Path.String(), "declared", "absent", drift.ReportSeverityBreaking, "structural_declaration_removed", match)
		case match.PathChanged:
			add("data_path_changed", match.After.Path.String(), match.Before.Path.String(), match.After.Path.String(), drift.ReportSeverityBreaking, "structural_data_path_changed", match)
		}
		if match.Before == nil || match.After == nil {
			continue
		}
		if match.Before.Required != match.After.Required || !reflect.DeepEqual(match.Before.Type, match.After.Type) {
			severity := drift.ReportSeveritySafe
			old := *before.Structure
			current := *after.Structure
			old.Root = &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen, Properties: []payload.StructuralProperty{{Name: "value", Required: match.Before.Required, Type: match.Before.Type}}}
			current.Root = &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen, Properties: []payload.StructuralProperty{{Name: "value", Required: match.After.Required, Type: match.After.Type}}}
			old.Representation, old.RootShape = current.Representation, current.RootShape
			if payload.CheckStructuralCompatibility(&current, &old, payload.DefaultStructuralLimits()) != nil {
				severity = drift.ReportSeverityBreaking
			}
			add("structural_constraint_changed", match.After.Path.String(), match.Before.Type.Kind.String(), match.After.Type.Kind.String(), severity, "structural_guarantee_changed", match)
		}
	}
	oldRoot, currentRoot := *before.Structure.Root, *after.Structure.Root
	oldRoot.Properties, oldRoot.Items, oldRoot.Keys, oldRoot.Values = nil, nil, nil, nil
	currentRoot.Properties, currentRoot.Items, currentRoot.Keys, currentRoot.Values = nil, nil, nil, nil
	rootGuaranteeChanged := payload.CheckStructuralCompatibility(after.Structure, before.Structure, payload.DefaultStructuralLimits()) != nil || payload.CheckStructuralCompatibility(before.Structure, after.Structure, payload.DefaultStructuralLimits()) != nil
	if !reflect.DeepEqual(oldRoot, currentRoot) || rootGuaranteeChanged && !slices.ContainsFunc(result.Changes, func(change ImpactChange) bool {
		return strings.HasPrefix(change.Kind, "structural_") || strings.HasPrefix(change.Kind, "declaration_") || change.Kind == "data_path_changed" || change.Kind == "representation_change"
	}) {
		severity := drift.ReportSeveritySafe
		if payload.CheckStructuralCompatibility(after.Structure, before.Structure, payload.DefaultStructuralLimits()) != nil {
			severity = drift.ReportSeverityBreaking
		}
		add("root_constraint_changed", "$", "recorded", "changed", severity, "structural_root_guarantee_changed", payload.StructuralMatch{})
	}
	if len(result.Changes) == 0 && before.Digest != after.Digest {
		add("descriptive_change", "$", "recorded", "changed", drift.ReportSeveritySafe, "executable_projection_unchanged", payload.StructuralMatch{})
	}
	return result, nil
}

func selectedProjectionPortImpact(pipeline *spec.Pipeline, port consumerPort, object ObjectImpact) *drift.Report {
	report := &drift.Report{Type: drift.ReportTypeContractImpact, Status: drift.ReportStatusOpen, Severity: drift.ReportSeveritySafe, Summary: "Selected executable projection comparison only; validate this consumer before upgrading. Runtime conformance and delivery are separate.", Changes: []drift.Change{}}
	if object.Comparison == "unsupported" {
		report.Severity = drift.ReportSeverityWarning
	}
	for _, change := range object.Changes {
		report.Changes = append(report.Changes, change.Change)
		report.Severity = max(report.Severity, change.Severity)
	}
	if object.richBefore != nil && object.richAfter != nil {
		producer, consumer := object.richAfter, object.richBefore
		if strings.HasPrefix(port.name, "input") {
			producer, consumer = object.richBefore, object.richAfter
		}
		if payload.CheckStructuralCompatibility(producer, consumer, payload.DefaultStructuralLimits()) != nil {
			report.Severity = drift.ReportSeverityBreaking
		}
	}
	report.Impacts = drift.PortImpacts(pipeline, port.step, report.Severity)
	return report
}
