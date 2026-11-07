package contractplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/stretchr/testify/require"
)

func TestRichPortAdmissionRetainsExactStructureAndProvenance(t *testing.T) {
	t.Parallel()
	revision, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	flat, err := odcs.Convert(revision.Document, "order")
	require.NoError(t, err)
	require.Nil(t, flat.Shape)
	projection, err := odcs.CompileProjection(*selection)
	require.NoError(t, err)
	require.Empty(t, projection.Diagnostics)
	in := testInput(t)
	pipeline, err := pipelinespec.Load("test.json", in.Authored)
	require.NoError(t, err)
	pipeline.Steps[0].Query.OutputContractRef = richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	pipeline.Steps[0].Query.OutputContractRef.Representation = model.ContractRepresentationUnknown
	in.Authored, err = json.Marshal(pipeline)
	require.NoError(t, err)
	in.Projection = selection
	in.Pins, err = CapturePipelinePins(pipeline, projection.Plan, func(ref *pipelinespec.ODCSContractRef) (*model.ODCSRevision, error) { return revision, nil })
	require.NoError(t, err)
	raw, id, err := Compile(in)
	require.NoError(t, err)
	plan, err := Read(raw, id)
	require.NoError(t, err)
	require.Equal(t, odcs.ProjectionCompilerID, plan.Pins[0].CompilerID)
	executable, err := plan.RuntimePipeline()
	require.NoError(t, err)
	require.Nil(t, executable.Steps[0].Query.OutputContractRef)
	require.Equal(t, model.ContractFieldTypeValueArray, executable.Steps[0].Query.OutputContract.Fields[3].Type)
	require.NotEmpty(t, plan.Projection.Inputs[0].Defaults)
	require.NotEmpty(t, plan.Projection.Inputs[0].Resolutions)
	require.Equal(t, "region", plan.Projection.Relationships[0].Pairs[0].From.IDs[0])
	require.Equal(t, "id", plan.Projection.Relationships[0].Pairs[1].From.IDs[0])
	for _, version := range []int{1, 2, 3} {
		plan.FormatVersion = version
		plan.CompilerID = map[int]string{1: "braidplane/contract-plan/v1", 2: "braidplane/contract-plan/v2", 3: "braidplane/contract-plan/v3"}[version]
		encoded, err := json.Marshal(plan)
		require.NoError(t, err)
		encoded, err = canonical(encoded)
		require.NoError(t, err)
		_, err = Read(encoded, digest(encoded))
		require.ErrorIs(t, err, ErrInvalid, "old formats cannot acquire rich admission")
	}
}

func TestRichPortSelectionsFailClosed(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"Missing", "WrongFacets", "Ambiguous", "AmbiguousMapping", "WrongDigest", "UnsupportedMapping"} {
		t.Run(scenario, func(t *testing.T) {
			revision, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
			require.NoError(t, err)
			ref := richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
			switch scenario {
			case "Missing":
				selection.Inputs[0].ObjectID = "customer"
				selection.Relationships = nil
			case "WrongFacets":
				ref.Representation = model.ContractRepresentationJSON
				ref.RootShape = model.ContractRootShapeObject
			case "Ambiguous":
				other := selection.Inputs[0]
				other.Variables = map[string]odcs.ResolutionValue{"LABEL_TYPE": {Value: "boolean", Revision: "variables-v2"}}
				selection.Inputs = append(selection.Inputs, other)
			case "AmbiguousMapping":
				other := selection.Inputs[0]
				other.Mapping = selection.Inputs[0].Mapping
				other.Mapping.ID = "synthetic/rich/equivalent-v2"
				selection.Inputs = append(selection.Inputs, other)
			case "WrongDigest":
				revision.Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "UnsupportedMapping":
				selection.Inputs[0].Mapping.Types[payload.StructuralVector] = false
			}
			result, err := odcs.CompileProjection(*selection)
			require.NoError(t, err)
			if scenario == "UnsupportedMapping" {
				require.Nil(t, result.Plan)
				require.NotEmpty(t, result.Diagnostics)
				return
			}
			require.Empty(t, result.Diagnostics)
			_, err = CapturePort(ref, revision, result.Plan)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestRichPinsCannotBeReboundOrLoseTheirStructure(t *testing.T) {
	t.Parallel()
	revision, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	projection, err := odcs.CompileProjection(*selection)
	require.NoError(t, err)
	require.Empty(t, projection.Diagnostics)
	in := testInput(t)
	pipeline, err := pipelinespec.Load("test.json", in.Authored)
	require.NoError(t, err)
	pipeline.Steps[0].Query.OutputContractRef = richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	in.Authored, err = json.Marshal(pipeline)
	require.NoError(t, err)
	in.Pins, err = CapturePipelinePins(pipeline, projection.Plan, func(*pipelinespec.ODCSContractRef) (*model.ODCSRevision, error) { return revision, nil })
	require.NoError(t, err)
	in.Projection = selection
	raw, id, err := Compile(in)
	require.NoError(t, err)
	for _, scenario := range []string{"MissingProjection", "OtherRevisionDigest", "DifferentShape"} {
		t.Run(scenario, func(t *testing.T) {
			p, err := Read(raw, id)
			require.NoError(t, err)
			switch scenario {
			case "MissingProjection":
				p.Projection, p.ProjectionInputs, p.ProjectionDigest = nil, nil, ""
			case "OtherRevisionDigest":
				p.Pins[0].Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			case "DifferentShape":
				p.Pins[0].Shape.Fields[0].Required = false
			}
			encoded, err := json.Marshal(p)
			require.NoError(t, err)
			encoded, err = canonical(encoded)
			require.NoError(t, err)
			_, err = Read(encoded, digest(encoded))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	specialized, specializedID, err := Specialize(raw, id, map[string]odcs.ResolutionValue{"LABEL_TYPE": {Value: "boolean", Revision: "variables-v2"}})
	require.NoError(t, err)
	p, err := Read(specialized, specializedID)
	require.NoError(t, err)
	require.NotEqual(t, id, specializedID)
	require.Equal(t, payload.StructuralBoolean, p.Projection.Inputs[0].Structure.Root.Properties[4].Type.Properties[0].Type.Kind)
	original, err := Read(raw, id)
	require.NoError(t, err)
	require.Equal(t, payload.StructuralString, original.Projection.Inputs[0].Structure.Root.Properties[4].Type.Properties[0].Type.Kind)
}

func TestRichPinBudgetReturnsNoPartialCapture(t *testing.T) {
	t.Parallel()
	revision, selection, err := richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
	require.NoError(t, err)
	projection, err := odcs.CompileProjection(*selection)
	require.NoError(t, err)
	require.Empty(t, projection.Diagnostics)
	projection.Plan.Inputs[0].Coverage = []model.ODCSDeclarationCoverage{{Path: "$", Reason: strings.Repeat("x", odcs.MaxProjectionBytes/2)}}
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Slug: "first", Query: &pipelinespec.QueryRef{OutputContractRef: richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)}},
		{Slug: "second", Query: &pipelinespec.QueryRef{OutputContractRef: richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)}},
	}}
	pins, err := CapturePipelinePins(pipeline, projection.Plan, func(*pipelinespec.ODCSContractRef) (*model.ODCSRevision, error) { return revision, nil })
	require.ErrorIs(t, err, ErrInvalid)
	require.Nil(t, pins)
}
