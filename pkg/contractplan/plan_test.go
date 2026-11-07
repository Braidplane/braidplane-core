package contractplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestReadPreservesMalformedJSONCause(t *testing.T) {
	t.Parallel()
	plan, err := Read([]byte("{bad"), "unused-digest")
	require.Nil(t, plan)
	require.ErrorIs(t, err, ErrInvalid)
	var cause *json.SyntaxError
	require.ErrorAs(t, err, &cause)
}

func testInput(t *testing.T) Input {
	t.Helper()
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders-sync", Name: "Orders"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}}}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return Input{Source: Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Authored: raw, Operations: []Operation{{Step: "fetch", Role: "query", DataSource: "db", Slug: "orders", Revision: "revision-one", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT id FROM orders\"}"},"parameters":[]}`)}}}
}

func testProjection(t *testing.T) *odcs.ProjectionRequest {
	t.Helper()
	document := json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"row","name":"Orders","properties":[{"id":"id","name":"id","logicalType":"integer","required":true}]}]}`)
	record, err := odcs.Record(document, "orders")
	require.NoError(t, err)
	return &odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{{Contract: "orders", Revision: "v1", ObjectID: "row", Digest: record.Digest, Document: record.Document, Mapping: odcs.ProjectionMapping{ID: "synthetic/json/v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: map[payload.StructuralKind]bool{payload.StructuralInteger: true, payload.StructuralObject: true}}}}}
}

func TestCompiledIdentityIncludesEveryIndependentInput(t *testing.T) {
	in := testInput(t)
	in.Projection = testProjection(t)
	projection, err := odcs.CompileProjection(*in.Projection)
	require.NoError(t, err)
	require.Empty(t, projection.Diagnostics)
	pipeline, err := pipelinespec.Load("test.json", in.Authored)
	require.NoError(t, err)
	require.NoError(t, validatePins(pipeline, nil))
	raw, id, err := Compile(in)
	require.NoError(t, err)
	original, err := Read(raw, id)
	require.NoError(t, err)
	for name, change := range map[string]func(*Input){
		"operation revision": func(in *Input) { in.Operations[0].Revision = "revision-two" },
		"operation content": func(in *Input) {
			in.Operations[0].Content = json.RawMessage(`{"data":{"statement":"SELECT changed"},"parameters":[]}`)
		},
		"parameter default": func(in *Input) {
			in.Operations[0].Content = json.RawMessage(`{"data":{"statement":"SELECT id"},"parameters":[{"slug":"id","name":"id","data_type":"integer","required":false,"default":"1"}]}`)
		},
		"mapping": func(in *Input) { in.Projection.Inputs[0].Mapping.ID = "synthetic/json/v2" },
		"explicit unused input": func(in *Input) {
			in.Projection.Inputs[0].Variables = map[string]odcs.ResolutionValue{"UNUSED": {Value: "fixed", Revision: "v2"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := testInput(t)
			in.Projection = testProjection(t)
			change(&in)
			changed, newID, err := Compile(in)
			require.NoError(t, err)
			require.NotEqual(t, id, newID)
			plan, err := Read(changed, newID)
			require.NoError(t, err)
			require.Equal(t, original.AuthoredSpecHash, plan.AuthoredSpecHash)
		})
	}
	// Mutating upstream authoring inputs after compilation cannot affect reload.
	in.Operations[0].Content = nil
	in.Projection.Inputs[0].Document = nil
	reloaded, err := Read(raw, id)
	require.NoError(t, err)
	require.Equal(t, original, reloaded)
	// Whitespace or object-key order does not create another compiled identity.
	var pretty any
	require.NoError(t, json.Unmarshal(raw, &pretty))
	spaced, err := json.MarshalIndent(pretty, "", "  ")
	require.NoError(t, err)
	_, err = Read(spaced, id)
	require.NoError(t, err)
}

func TestReadRejectsIncompleteCorruptAndUnsupportedMaterial(t *testing.T) {
	in := testInput(t)
	in.Projection = testProjection(t)
	raw, id, err := Compile(in)
	require.NoError(t, err)
	_, err = Read([]byte(`{}`), id)
	require.ErrorIs(t, err, ErrInvalid)
	for name, change := range map[string]func(*Plan){
		"format":                   func(p *Plan) { p.FormatVersion++ },
		"missing format":           func(p *Plan) { p.FormatVersion = 0 },
		"compiler":                 func(p *Plan) { p.CompilerID += "/future" },
		"missing compiler":         func(p *Plan) { p.CompilerID = "" },
		"policy":                   func(p *Plan) { p.StructuralPolicyID += "/future" },
		"missing policy":           func(p *Plan) { p.StructuralPolicyID = "" },
		"authored hash":            func(p *Plan) { p.AuthoredSpecHash = "sha256:wrong" },
		"missing operation":        func(p *Plan) { p.Operations = nil },
		"extra operation":          func(p *Plan) { p.Operations = append(p.Operations, p.Operations[0]) },
		"operation digest":         func(p *Plan) { p.Operations[0].Digest = "sha256:wrong" },
		"operation identity":       func(p *Plan) { p.Operations[0].DataSource = "other" },
		"projection compiler":      func(p *Plan) { p.Projection.CompilerID = "future" },
		"projection digest":        func(p *Plan) { p.ProjectionDigest = "sha256:wrong" },
		"missing projection input": func(p *Plan) { p.ProjectionInputs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Read(raw, id)
			require.NoError(t, err)
			change(p)
			changed, err := json.Marshal(p)
			require.NoError(t, err)
			changed, err = canonical(changed)
			require.NoError(t, err)
			_, err = Read(changed, digest(changed))
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
	_, err = Read([]byte(strings.Repeat(" ", odcs.MaxProjectionBytes+1)), id)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestCompileRejectsUnmatchedFlatPinsAndMissingOperations(t *testing.T) {
	in := testInput(t)
	in.Operations = nil
	_, _, err := Compile(in)
	require.ErrorIs(t, err, ErrInvalid)
	in = testInput(t)
	in.Pins = []model.PinnedODCSPort{{StepSlug: "missing", Port: "output"}}
	_, _, err = Compile(in)
	require.ErrorIs(t, err, ErrInvalid)
}
