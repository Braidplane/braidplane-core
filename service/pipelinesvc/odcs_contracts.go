package pipelinesvc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
)

var ErrODCSContractReference = xerror.New(xerror.KindInvalidInput, "PIPELINE_ODCS_REFERENCE_INVALID", "pipeline ODCS contract reference cannot be resolved")

type odcsContractReferenceError struct {
	diagnostics []PipelineValidationDiagnostic
}

func (e *odcsContractReferenceError) Error() string { return ErrODCSContractReference.Error() }
func (e *odcsContractReferenceError) Unwrap() error { return ErrODCSContractReference }

type odcsPort struct {
	field string
	step  slug.Slug
	name  string
	ref   **pipelinespec.ODCSContractRef
	shape **payload.Contract
}

func pipelineODCSPorts(pipeline *pipelinespec.Pipeline) []odcsPort {
	var ports []odcsPort
	for i := range pipeline.Steps {
		step := &pipeline.Steps[i]
		if step.Input != nil {
			ports = append(ports, odcsPort{fmt.Sprintf("steps[%d].input.output_contract_ref", i), step.Slug, "output", &step.Input.OutputContractRef, &step.Input.OutputContract})
		}
		if step.Query != nil {
			ports = append(ports, odcsPort{fmt.Sprintf("steps[%d].query.output_contract_ref", i), step.Slug, "output", &step.Query.OutputContractRef, &step.Query.OutputContract})
		}
		if step.Transform != nil {
			ports = append(ports,
				odcsPort{fmt.Sprintf("steps[%d].transform.input_contract_ref", i), step.Slug, "input", &step.Transform.InputContractRef, &step.Transform.InputContract},
				odcsPort{fmt.Sprintf("steps[%d].transform.output_contract_ref", i), step.Slug, "output", &step.Transform.OutputContractRef, &step.Transform.OutputContract})
		}
		if step.Transform != nil {
			names := make([]string, 0, len(step.Transform.InputContracts))
			for name := range step.Transform.InputContracts {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				attachment := step.Transform.InputContracts[name]
				if attachment != nil {
					ports = append(ports, odcsPort{fmt.Sprintf("steps[%d].transform.input_contracts.%s.contract_ref", i, name), step.Slug, "input:" + name, &attachment.ContractRef, &attachment.Contract})
				}
			}
		}
		if step.Sink != nil {
			ports = append(ports, odcsPort{fmt.Sprintf("steps[%d].sink.input_contract_ref", i), step.Slug, "input", &step.Sink.InputContractRef, &step.Sink.InputContract})
		}
	}
	return ports
}

func (s *pipelineStore) ResolvePipelineContracts(ctx context.Context, org, project slug.Slug, pipeline *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
	return s.resolvePipelineContracts(ctx, org, project, pipeline, false)
}

func (s *pipelineStore) resolvePipelineContracts(ctx context.Context, org, project slug.Slug, pipeline *pipelinespec.Pipeline, retainReferences bool) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error) {
	if pipeline.UsesContractComposition() {
		if err := pipelinevalidate.Validate(pipeline); err != nil {
			return nil, nil, err
		}
	}

	document, err := json.Marshal(pipeline)
	if err != nil {
		return nil, nil, xerror.Unexpected().With(err).Detail("encode pipeline for ODCS resolution")
	}
	resolved := new(pipelinespec.Pipeline)
	if err := json.Unmarshal(document, resolved); err != nil {
		return nil, nil, xerror.Unexpected().With(err).Detail("decode pipeline for ODCS resolution")
	}
	pins := []model.PinnedODCSPort{}
	pinBytes := 2
	for _, port := range pipelineODCSPorts(resolved) {
		ref := *port.ref
		if ref == nil {
			continue
		}
		if *port.shape != nil {
			return nil, nil, ErrODCSContractReference.WithPublicDetail("Use an inline contract or a reference, never both.")
		}
		var revision *model.ODCSRevision
		err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			var readErr error
			revision, readErr = s.odcs.GetRevision(ctx, tx, org, project, ref.ContractSlug, ref.RevisionSlug)
			return readErr
		})
		if err != nil {
			if xerror.Is(err, odcscontracts.ErrNotFound) {
				return nil, nil, port.referenceError([]odcs.Diagnostic{{Code: "reference_unavailable", Path: "$", Message: "Select a revision available in this project."}})
			}
			return nil, nil, err
		}
		conversion, err := odcs.Convert(revision.Document, ref.SchemaObjectID)
		if err != nil {
			return nil, nil, err
		}
		if conversion.Shape == nil || len(conversion.Diagnostics) != 0 {
			// Git retains rich authoring for explicit compilation; flat execution
			// still rejects these references without exact selected checks.
			if retainReferences && conversion.DocumentValid && conversion.Digest == revision.Digest && revision.ODCSVersion == odcs.PinnedVersion && slices.ContainsFunc(revision.SchemaObjects, func(object model.ODCSSchemaObject) bool { return object.ID == ref.SchemaObjectID }) {
				continue
			}
			return nil, nil, port.referenceError(conversion.Diagnostics)
		}
		if conversion.Digest != revision.Digest || revision.ODCSVersion != odcs.PinnedVersion {
			return nil, nil, xerror.Unexpected().Detail("ODCS revision digest or version differs from its document")
		}
		conversion.Shape.Representation = ref.Representation
		conversion.Shape.RootShape = ref.RootShape
		pins = append(pins, model.PinnedODCSPort{StepSlug: port.step, Port: port.name, ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectID: ref.SchemaObjectID, Digest: revision.Digest, Shape: conversion.Shape, CompilerID: conversion.CompilerID, Coverage: conversion.Coverage})
		encoded, err := json.Marshal(pins[len(pins)-1])
		if err != nil {
			return nil, nil, xerror.Unexpected().With(err).Detail("encode ODCS projection pin")
		}
		pinBytes += len(encoded) + 1
		if pinBytes > odcs.MaxProjectionBytes {
			return nil, nil, port.referenceError([]odcs.Diagnostic{{Code: "projection_limit_exceeded", Path: "$", Message: "Combined immutable ODCS pins exceed the 8 MiB resource budget; no partial snapshot is applied."}})
		}
		*port.shape = conversion.Shape
		*port.ref = nil
	}
	return resolved, pins, nil
}

// Runtime consumes only the immutable version snapshot, never the catalog.
func pipelineFromVersion(version *pipelinesrepo.PipelineVersion) (*pipelinespec.Pipeline, error) {
	pipeline, err := pipelinespec.Load("version.pipeline.json", version.SpecJSONB)
	if err != nil {
		return nil, err
	}
	var pins []model.PinnedODCSPort
	if len(version.ResolvedContracts) > 0 {
		if err := json.Unmarshal(version.ResolvedContracts, &pins); err != nil {
			return nil, xerror.Unexpected().With(err).Detail("decode pinned ODCS contracts")
		}
	}
	for _, port := range pipelineODCSPorts(pipeline) {
		ref := *port.ref
		if ref == nil {
			continue
		}
		index := slices.IndexFunc(pins, func(pin model.PinnedODCSPort) bool { return pin.StepSlug == port.step && pin.Port == port.name })
		if index < 0 {
			return nil, xerror.InvalidInput(xerror.PipelineVersion).Detail("immutable version is missing a pinned ODCS contract").WithPublicDetail("Compile this retained version with an exact projection selection, then select its compiled plan for deployment.")
		}
		pin := pins[index]
		if !odcs.IsFlatCompiler(pin.CompilerID) {
			return nil, xerror.Unexpected().Detail("immutable ODCS contract pin uses an unsupported flat compiler")
		}
		digest, err := hex.DecodeString(strings.TrimPrefix(pin.Digest, "sha256:"))
		if err != nil || !strings.HasPrefix(pin.Digest, "sha256:") || len(digest) != 32 || pin.Shape == nil || *port.shape != nil || pin.ContractSlug != ref.ContractSlug || pin.RevisionSlug != ref.RevisionSlug || pin.SchemaObjectID != ref.SchemaObjectID || pin.Shape.Representation != ref.Representation || pin.Shape.RootShape != ref.RootShape {
			return nil, xerror.Unexpected().With(err).Detail("immutable ODCS contract pin does not match its authored reference")
		}
		*port.shape = pin.Shape
		*port.ref = nil
		pins = slices.Delete(pins, index, index+1)
	}
	if len(pins) != 0 {
		return nil, xerror.Unexpected().Detail("immutable version contains unmatched ODCS contract pins")
	}
	return pipeline, nil
}

func (port odcsPort) referenceError(findings []odcs.Diagnostic) error {
	diagnostics := make([]PipelineValidationDiagnostic, 0, len(findings))
	for _, finding := range findings {
		diagnostics = append(diagnostics, PipelineValidationDiagnostic{
			Severity: "error", Code: "ODCS_" + strings.ToUpper(finding.Code),
			Field: port.field, StepSlug: ptrString(port.step.String()),
			Message: finding.Path + ": " + finding.Message,
		})
	}
	return &odcsContractReferenceError{diagnostics: diagnostics}
}
