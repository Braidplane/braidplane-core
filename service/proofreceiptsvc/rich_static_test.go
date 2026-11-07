package proofreceiptsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

type staticValidationResult struct {
	result pipelinesvc.PipelineValidationResult
}

func (v staticValidationResult) Validate(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinesvc.PipelineValidationResult, error) {
	return &v.result, nil
}

func TestRichStaticReceiptPreservesReferencesWithoutDisprovingSelectedProjection(t *testing.T) {
	t.Parallel()
	document := json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"id":"at","name":"at","logicalType":"timestamp"},{"id":"vector","name":"vector","logicalType":"vector","logicalTypeOptions":{"dimensions":3}},{"id":"nested","name":"nested","logicalType":"object","properties":[{"name":"id","logicalType":"string"}]}]}]}`)
	flat, err := odcs.Convert(document, "order")
	require.NoError(t, err)
	require.True(t, flat.DocumentValid)
	require.Nil(t, flat.Shape)
	require.NotEmpty(t, flat.Diagnostics)
	projection, err := odcs.CompileProjection(odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{{
		Contract: "orders", Revision: "v1", ObjectID: "order", Digest: flat.Digest, Document: document,
		Mapping: odcs.ProjectionMapping{ID: "synthetic/json/v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: map[payload.StructuralKind]bool{
			payload.StructuralObject: true, payload.StructuralString: true, payload.StructuralInstant: true, payload.StructuralVector: true, payload.StructuralNumber: true,
		}},
	}}})
	require.NoError(t, err)
	require.Empty(t, projection.Diagnostics)
	require.NotNil(t, projection.Plan)

	for _, port := range []string{"query", "input", "named relation"} {
		t.Run(port, func(t *testing.T) {
			repo := referencedCandidate(t, "v1")
			var pipeline pipelinespec.Pipeline
			require.NoError(t, json.Unmarshal(repo.target.CanonicalSpec, &pipeline))
			ref := pipeline.Steps[0].Query.OutputContractRef
			ref.Representation, ref.RootShape = model.ContractRepresentationJSON, model.ContractRootShapeObject
			field := "steps[0].query.output_contract_ref"
			path := "/steps/extract/query/output_contract"
			switch port {
			case "input":
				pipeline.Steps[0] = pipelinespec.Step{Kind: "input", Slug: "extract", Input: &pipelinespec.InputSpec{OutputContractRef: ref}}
				pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "project", Transform: &pipelinespec.TransformSpec{
					SQL: "select 1 as id from input_rows", OutputKind: model.TransformOutputKindRows,
					Inputs: map[string]pipelinespec.StepInput{"input_rows": {FromStep: "extract"}},
				}})
				field, path = "steps[0].input.output_contract_ref", "/steps/extract/input/output_contract"
			case "named relation":
				pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "join", Transform: &pipelinespec.TransformSpec{
					SQL: "select 1 as id from input_rows", OutputKind: model.TransformOutputKindRows,
					Inputs:         map[string]pipelinespec.StepInput{"input_rows": {FromStep: "extract"}},
					InputContracts: map[string]*pipelinespec.InputContractAttachment{"input_rows": {ContractRef: ref}},
				}})
			}
			repo.target.CanonicalSpec, err = json.Marshal(pipeline)
			require.NoError(t, err)
			hash := sha256.Sum256(repo.target.CanonicalSpec)
			repo.target.SpecHash = "sha256:" + hex.EncodeToString(hash[:])
			step := "extract"
			validation := staticValidationResult{}
			for _, finding := range flat.Diagnostics {
				validation.result.Diagnostics = append(validation.result.Diagnostics, pipelinesvc.PipelineValidationDiagnostic{Code: "ODCS_" + strings.ToUpper(finding.Code), Field: field, StepSlug: &step})
			}
			catalog := contractValidatorFunc(func(_ context.Context, org, project, contract, revision slug.Slug, object string) (*odcs.ConversionResult, error) {
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("data"), project)
				require.Equal(t, slug.Slug("orders"), contract)
				require.Equal(t, slug.Slug("v1"), revision)
				require.Equal(t, "order", object)
				return odcs.Convert(document, object)
			})
			svc, err := New(testutil.FakeTransactionManager{}, repo, validation, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{}}, deploymentReader{}, emptySecretResolver{}, catalog, fixedClock{time.Now()})
			require.NoError(t, err)
			receipt, err := svc.Compile(t.Context(), "acme", "candidate-1")
			require.NoError(t, err)
			require.Equal(t, repo.target.SpecHash, receipt.Value.Candidate.SpecHash)
			claim := odcsClaim(t, receipt.Value.Claims, "/validation")
			require.Equal(t, model.ProofVerdictUnverified, claim.Verdict)
			require.Equal(t, "proof.selected_projection_unavailable", claim.Diagnostics[0].Code)
			validity := odcsClaim(t, receipt.Value.Claims, path+"/odcs/contracts/orders/revisions/v1/objects/order/digests/"+flat.Digest+"/revision_validity")
			require.Equal(t, model.ProofVerdictPassed, validity.Verdict)
			if port == "named relation" {
				claim := odcsClaim(t, receipt.Value.Claims, "/steps/join/transform/input_contracts/input_rows/contract/odcs/contracts/orders/revisions/v1/objects/order/digests/"+flat.Digest+"/port_compatibility")
				require.Equal(t, model.ProofVerdictUnverified, claim.Verdict)
			}
			for _, claim := range receipt.Value.Claims {
				require.NotEqual(t, model.ProofVerdictFailed, claim.Verdict, claim.Identity.SemanticPath)
				if strings.HasSuffix(claim.Identity.SemanticPath, "/port_compatibility") || strings.HasSuffix(claim.Identity.SemanticPath, "/runtime_payload_conformance") {
					require.Equal(t, model.ProofVerdictUnverified, claim.Verdict)
				}
				for _, diagnostic := range claim.Diagnostics {
					require.NotEqual(t, "proof.validation_failed", diagnostic.Code)
				}
			}
			require.NotEqual(t, model.ProofVerdictPassed, receipt.Value.AggregateVerdict)
		})
	}
}

func TestUnsupportedProjectionDoesNotMaskInvalidCandidateDiagnostics(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"PIPELINE_INVALID_INPUT", "ODCS_SCHEMA_INVALID", "ODCS_OBJECT_NOT_FOUND", "ODCS_REFERENCE_UNAVAILABLE", "ODCS_VARIABLE_UNRESOLVED", "ODCS_MAPPING_INVALID", "ODCS_PROJECTION_LIMIT_EXCEEDED", "DATA_SOURCE_BINDING_NOT_FOUND", "DATA_SOURCE_REF_INCOMPATIBLE"} {
		t.Run(code, func(t *testing.T) {
			validation := &pipelinesvc.PipelineValidationResult{Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{Code: "ODCS_TYPE_UNSUPPORTED"}, {Code: code}}}
			claims := compileClaims(&pipelinespec.Pipeline{}, validation, nil, nil, nil, "candidate", "evidence", time.Now())
			claim := odcsClaim(t, claims, "/validation")
			require.Equal(t, model.ProofVerdictFailed, claim.Verdict)
			require.Equal(t, "proof.validation_failed", claim.Diagnostics[1].Code)
		})
	}
	require.False(t, selectedProjectionUnavailable(nil))
}
