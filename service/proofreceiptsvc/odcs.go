package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
)

// Only identity and validation outcomes enter the evidence digest, never document values.
type contractInput struct {
	Path          string
	Reference     pipelinespec.ODCSContractRef
	Digest        string
	DocumentValid bool
	Supported     bool
	Properties    []odcs.PropertyIdentity
	Diagnostics   []odcs.Diagnostic
	CompilerID    string
	Coverage      []model.ODCSDeclarationCoverage
	SourceRef     slug.Slug
	Step          slug.Slug
	Port          string
	Compatibility model.ProofVerdict
}

func (s *Compiler) contractInputs(ctx context.Context, org, project slug.Slug, pipeline *pipelinespec.Pipeline) ([]contractInput, error) {
	raw, err := json.Marshal(pipeline)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	resolved := new(pipelinespec.Pipeline)
	if err := json.Unmarshal(raw, resolved); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var inputs []contractInput
	inputBytes := 0
	for _, step := range resolved.Steps {
		type portReference struct {
			path      string
			ref       *pipelinespec.ODCSContractRef
			source    slug.Slug
			shape     **payload.Contract
			reference **pipelinespec.ODCSContractRef
			port      string
		}
		var ports []portReference
		base := "/steps/" + pointerToken(step.Slug.String())
		if step.Input != nil {
			ports = append(ports, portReference{base + "/input/output_contract", step.Input.OutputContractRef, "", &step.Input.OutputContract, &step.Input.OutputContractRef, "output"})
		}
		if step.Query != nil {
			ports = append(ports, portReference{base + "/query/output_contract", step.Query.OutputContractRef, step.Query.DataSourceSlug, &step.Query.OutputContract, &step.Query.OutputContractRef, "output"})
		}
		if step.Transform != nil {
			ports = append(ports, portReference{base + "/transform/input_contract", step.Transform.InputContractRef, "", &step.Transform.InputContract, &step.Transform.InputContractRef, "input"}, portReference{base + "/transform/output_contract", step.Transform.OutputContractRef, "", &step.Transform.OutputContract, &step.Transform.OutputContractRef, "output"})
			names := make([]string, 0, len(step.Transform.InputContracts))
			for name := range step.Transform.InputContracts {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				if attachment := step.Transform.InputContracts[name]; attachment != nil {
					ports = append(ports, portReference{base + "/transform/input_contracts/" + pointerToken(name) + "/contract", attachment.ContractRef, "", &attachment.Contract, &attachment.ContractRef, "input:" + name})
				}
			}
		}
		if step.Sink != nil {
			ports = append(ports, portReference{base + "/sink/input_contract", step.Sink.InputContractRef, "", &step.Sink.InputContract, &step.Sink.InputContractRef, "input"})
		}
		for _, port := range ports {
			if port.ref == nil {
				continue
			}
			input := contractInput{Path: port.path, Reference: *port.ref, SourceRef: port.source, Step: step.Slug, Port: port.port, Compatibility: model.ProofVerdictUnverified}
			result, err := s.contracts.ValidateRevision(ctx, org, project, port.ref.ContractSlug, port.ref.RevisionSlug, port.ref.SchemaObjectID)
			switch {
			case xerror.Is(err, odcscontracts.ErrNotFound):
				input.Diagnostics = []odcs.Diagnostic{{Code: "reference_unavailable", Path: "$", Message: "Select a revision available in this project."}}
			case err != nil:
				return nil, err
			default:
				input.Digest, input.DocumentValid, input.Supported = result.Digest, result.DocumentValid, result.Shape != nil && len(result.Diagnostics) == 0
				input.Properties, input.Diagnostics = result.Properties, result.Diagnostics
				input.CompilerID, input.Coverage = result.CompilerID, result.Coverage
				if input.Supported && *port.shape == nil {
					result.Shape.Representation = port.ref.Representation
					result.Shape.RootShape = port.ref.RootShape
					*port.shape = result.Shape
					*port.reference = nil
				}
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				return nil, xerror.Unexpected().With(err).Detail("encode ODCS Proof input")
			}
			inputBytes += len(encoded)
			if inputBytes > odcs.MaxProjectionBytes {
				return nil, xerror.ValidationFailed().WithPublicDetail("Combined ODCS Proof inputs exceed the 8 MiB resource budget.")
			}
			inputs = append(inputs, input)
		}
	}

	canonicalValid := pipelinevalidate.Validate(resolved) == nil
	edges, edgeErr := pipelinevalidate.StepEdgeContexts(resolved)
	if edgeErr == nil {
		diagnostics := pipelinevalidate.AggregateStepInputCompatibilityDiagnostics(resolved)
		for _, edge := range edges {
			if !edgeHasContractReference(edge) {
				diagnostics = append(diagnostics, *pipelinevalidate.EvaluateEdgeDiagnosticsForSteps(edge.FromStep, edge.ToStep))
			}
		}
		for i := range inputs {
			input := &inputs[i]
			if !input.Supported {
				continue
			}
			// Named relation compatibility requires the exact selected composition plan.
			if strings.HasPrefix(input.Port, "input:") {
				continue
			}
			input.Compatibility = model.ProofVerdictPassed
			for _, edge := range edges {
				if ((input.Port == "output" && edge.FromStep.Slug == input.Step) || (input.Port == "input" && edge.ToStep.Slug == input.Step)) && edgeHasContractReference(edge) {
					input.Compatibility = model.ProofVerdictUnverified
				}
			}
			for _, diagnostic := range diagnostics {
				if !diagnostic.Compatible && ((input.Port == "output" && diagnostic.FromStepSlug == input.Step) || (input.Port == "input" && diagnostic.ToStepSlug == input.Step)) {
					input.Compatibility = model.ProofVerdictFailed
				}
			}
			if input.Compatibility == model.ProofVerdictPassed && !canonicalValid {
				input.Compatibility = model.ProofVerdictUnverified
			}
		}
	}
	return inputs, nil
}

func appendODCSClaims(claims []model.ProofClaim, inputs []contractInput, resolutions []model.ResolvedPipelineDataSourceRef, observations []schemaObservation, specHash, evidenceDigest string, now time.Time, redact func(string) string) ([]model.ProofClaim, error) {
	claimBytes := 0
	add := func(items ...model.ProofClaim) error {
		for _, claim := range items {
			encoded, err := json.Marshal(claim)
			if err != nil {
				return xerror.Unexpected().With(err).Detail("encode ODCS Proof claim")
			}
			claimBytes += len(encoded) + 1
			if claimBytes > odcs.MaxProjectionBytes {
				return xerror.ValidationFailed().WithPublicDetail("ODCS Proof claims exceed the 8 MiB resource budget; no partial receipt is returned.")
			}
			claims = append(claims, claim)
		}
		return nil
	}
	for _, input := range inputs {
		ref := model.ProofEvidenceReference{Kind: model.ProofEvidenceReferenceKindValidation, Digest: evidenceDigest}
		base := input.Path + "/odcs/contracts/" + pointerToken(redact(input.Reference.ContractSlug.String())) + "/revisions/" + pointerToken(redact(input.Reference.RevisionSlug.String())) + "/objects/" + pointerToken(redact(input.Reference.SchemaObjectID))
		if input.Digest != "" {
			base += "/digests/" + pointerToken(input.Digest)
		}
		validity := staticClaim(model.ProofClaimFamilyFieldsTypes, base+"/revision_validity", "exact referenced revision passes pinned ODCS schema validation", input.DocumentValid, input.Digest != "", nil, specHash, ref, now, model.ProofEvidenceLevelStatic)
		compatibility := staticClaim(model.ProofClaimFamilyFieldsTypes, base+"/port_compatibility", "supported revision shape is compatible with the exact candidate port", input.Compatibility == model.ProofVerdictPassed, input.Compatibility == model.ProofVerdictFailed, nil, specHash, ref, now, model.ProofEvidenceLevelStatic)
		if err := add(validity, compatibility); err != nil {
			return nil, err
		}
		for _, declaration := range input.Coverage {
			if declaration.State != "unsupported" && declaration.State != "unresolved" {
				continue
			}
			claim := staticClaim(model.ProofClaimFamilyFieldsTypes, base+"/coverage/"+pointerToken(declaration.Path), "ODCS declaration is preserved without a verified runtime check", false, false, nil, specHash, ref, now, model.ProofEvidenceLevelStatic)
			claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.odcs_" + declaration.State, Summary: declaration.Reason, Remediation: "inspect declaration coverage and selected-port applicability before relying on this agreement"}}
			if err := add(claim); err != nil {
				return nil, err
			}
		}
		seenDiagnostics := map[string]bool{}
		for _, diagnostic := range input.Diagnostics {
			key := odcsDeclarationPath(input.Properties, diagnostic.Path, redact) + "/" + pointerToken(diagnostic.Code)
			if seenDiagnostics[key] {
				continue
			}
			seenDiagnostics[key] = true
			claim := staticClaim(model.ProofClaimFamilyFieldsTypes, base+"/declarations/"+key, "ODCS declaration has no verified static mapping", false, false, nil, specHash, ref, now, model.ProofEvidenceLevelStatic)
			claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.odcs_" + diagnostic.Code, Summary: diagnostic.Message, Remediation: "select a supported revision or keep this declaration outside the executable port contract"}}
			if input.DocumentValid && (diagnostic.Code == "type_unsupported" || diagnostic.Code == "mapping_unsupported") {
				claim.Diagnostics = []model.ProofDiagnostic{selectedProjectionDiagnostic()}
			}
			if err := add(claim); err != nil {
				return nil, err
			}
		}
		for _, property := range input.Properties {
			if !input.Supported {
				continue
			}
			path := base + "/properties/"
			if property.ID != "" {
				path += "ids/" + pointerToken(redact(property.ID))
			} else {
				path += "names/" + pointerToken(redact(property.Name))
			}
			if err := add(staticClaim(model.ProofClaimFamilyFieldsTypes, path+"/declaration", "referenced property declaration belongs to the supported static shape", input.Supported, false, nil, specHash, ref, now, model.ProofEvidenceLevelStatic)); err != nil {
				return nil, err
			}
			if input.SourceRef != "" {
				source := slug.Slug("")
				for _, resolution := range resolutions {
					if input.SourceRef == resolution.RefSlug {
						source = resolution.DataSourceSlug
					}
				}
				observed := make(map[slug.Slug]schemaObservation, len(observations))
				for _, observation := range observations {
					observed[observation.DataSourceSlug] = observation
				}
				fieldClaims := appendContractClaims(nil, input.Path, &payload.Contract{Fields: []payload.ContractField{{Name: property.Name}}}, true, source, false, observed, specHash, ref, now)
				fieldClaims[0].Identity.SemanticPath = path + "/source_observation"
				if err := add(fieldClaims...); err != nil {
					return nil, err
				}
			}
		}
		claim := staticClaim(model.ProofClaimFamilyFieldsTypes, base+"/runtime_payload_conformance", "runtime payload conformance requires separate execution evidence", false, false, nil, specHash, ref, now, model.ProofEvidenceLevelStatic)
		claim.RequiredEvidenceLevel = model.ProofEvidenceLevelBoundedExecution
		claim.Diagnostics = []model.ProofDiagnostic{{Code: "proof.odcs_payload_unverified", Summary: "static contract validation does not prove runtime payload conformance", Remediation: "inspect separately linked run-time contract outcomes"}}
		if err := add(claim); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

func edgeHasContractReference(edge pipelinevalidate.EdgeContext) bool {
	from, to := edge.FromStep, edge.ToStep
	if to.Transform != nil {
		for _, attachment := range to.Transform.InputContracts {
			if attachment != nil && attachment.ContractRef != nil {
				return true
			}
		}
	}
	return (from.Input != nil && from.Input.OutputContractRef != nil) || (from.Query != nil && from.Query.OutputContractRef != nil) || (from.Transform != nil && from.Transform.OutputContractRef != nil) || (to.Transform != nil && to.Transform.InputContractRef != nil) || (to.Sink != nil && to.Sink.InputContractRef != nil)
}

func odcsDeclarationPath(properties []odcs.PropertyIdentity, path string, redact func(string) string) string {
	_, suffix, ok := strings.Cut(path, ".properties[")
	if ok {
		indexText, field, closed := strings.Cut(suffix, "]")
		index, err := strconv.Atoi(indexText)
		if closed && err == nil && index >= 0 && index < len(properties) && properties[index].ID != "" {
			return "properties/ids/" + pointerToken(redact(properties[index].ID)) + "/" + pointerToken(field)
		}
	}
	return pointerToken(path)
}
