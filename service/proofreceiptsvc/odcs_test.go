package proofreceiptsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/stretchr/testify/require"
)

const proofODCSDocument = `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"id":"order-id","name":"id","logicalType":"string","required":true,"description":"private-description","enum":[{"value":"private-enum"}]}]}]}`

type contractValidatorFunc func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error)

func (f contractValidatorFunc) ValidateRevision(ctx context.Context, org, project, contract, revision slug.Slug, object string) (*odcs.ConversionResult, error) {
	return f(ctx, org, project, contract, revision, object)
}

func referencedCandidate(t *testing.T, revision slug.Slug) *fakeReceiptRepository {
	t.Helper()
	target := testCandidateTarget(t)
	var pipeline pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(target.CanonicalSpec, &pipeline))
	pipeline.Steps[0].Query.OutputContract = nil
	pipeline.Steps[0].Query.OutputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: revision, SchemaObjectID: "order", Representation: model.ContractRepresentationRows}
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	target.CanonicalSpec = raw
	hash := sha256.Sum256(raw)
	target.SpecHash = "sha256:" + hex.EncodeToString(hash[:])
	return &fakeReceiptRepository{target: target}
}

func TestODCSCompileBindsExactRevisionAndSeparatesEvidence(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	repo := referencedCandidate(t, "v1")
	validator := contractValidatorFunc(func(_ context.Context, org, project, contract, revision slug.Slug, object string) (*odcs.ConversionResult, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("data"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		require.Equal(t, slug.Slug("v1"), revision)
		require.Equal(t, "order", object)
		return odcs.Convert([]byte(proofODCSDocument), object)
	})
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{SchemaData: json.RawMessage(`{}`), UpdatedAt: now.Add(-25 * time.Hour)}}, deploymentReader{}, emptySecretResolver{}, validator, fixedClock{now})
	require.NoError(t, err)
	first, err := svc.Compile(t.Context(), "acme", "candidate-1")
	require.NoError(t, err)
	replay, err := svc.Compile(t.Context(), "acme", "candidate-1")
	require.NoError(t, err)
	require.Equal(t, first.Value.ID, replay.Value.ID)
	conversion, err := odcs.Convert([]byte(proofODCSDocument), "order")
	require.NoError(t, err)
	for suffix, verdict := range map[string]model.ProofVerdict{"/revision_validity": model.ProofVerdictPassed, "/port_compatibility": model.ProofVerdictPassed, "/properties/ids/order-id/declaration": model.ProofVerdictPassed, "/properties/ids/order-id/source_observation": model.ProofVerdictStale, "/runtime_payload_conformance": model.ProofVerdictUnverified} {
		claim := odcsClaim(t, first.Value.Claims, suffix)
		require.Equal(t, verdict, claim.Verdict, suffix)
		require.Contains(t, claim.Identity.SemanticPath, "/contracts/orders/revisions/v1/objects/order/digests/"+conversion.Digest)
	}
	raw, err := json.Marshal(first.Value)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-description")
	require.NotContains(t, string(raw), "private-enum")
	require.NotContains(t, string(raw), "/properties/names/id")
}

func TestODCSClaimsDoNotVerifyUnsupportedOrUnavailableDeclarations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, document string
		err            error
		validity       model.ProofVerdict
	}{
		{"unsupported", strings.Replace(proofODCSDocument, `"version":"1"`, `"version":"1","slaProperties":[{"property":"latency","value":"private-sla"}]`, 1), nil, model.ProofVerdictPassed},
		{"invalid", strings.Replace(proofODCSDocument, `"logicalType":"string"`, `"logicalType":"private-type"`, 1), nil, model.ProofVerdictFailed},
		{"other tenant", "", odcscontracts.ErrNotFound, model.ProofVerdictUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := referencedCandidate(t, "v1")
			catalog := contractValidatorFunc(func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return odcs.Convert([]byte(tc.document), "order")
			})
			svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{SchemaData: json.RawMessage(`{}`), UpdatedAt: time.Now()}}, deploymentReader{}, emptySecretResolver{}, catalog, fixedClock{time.Now()})
			require.NoError(t, err)
			receipt, err := svc.Compile(t.Context(), "acme", "candidate-1")
			require.NoError(t, err)
			require.Equal(t, tc.validity, odcsClaim(t, receipt.Value.Claims, "/revision_validity").Verdict)
			compatibility := model.ProofVerdictUnverified
			if tc.name == "unsupported" {
				compatibility = model.ProofVerdictPassed
			}
			require.Equal(t, compatibility, odcsClaim(t, receipt.Value.Claims, "/port_compatibility").Verdict)
			declarations := 0
			for _, claim := range receipt.Value.Claims {
				if strings.Contains(claim.Identity.SemanticPath, "/declarations/") || strings.Contains(claim.Identity.SemanticPath, "/coverage/") {
					declarations++
					require.Equal(t, model.ProofVerdictUnverified, claim.Verdict)
				}
			}
			require.Positive(t, declarations)
			raw, err := json.Marshal(receipt.Value)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-sla")
			require.NotContains(t, string(raw), "private-type")
		})
	}
}

func TestODCSChangedReferenceCreatesNewReceiptWithoutRewritingPrevious(t *testing.T) {
	t.Parallel()
	repo := referencedCandidate(t, "v1")
	catalog := contractValidatorFunc(func(_ context.Context, _, _, _, revision slug.Slug, object string) (*odcs.ConversionResult, error) {
		return odcs.Convert([]byte(strings.Replace(proofODCSDocument, `"version":"1"`, `"version":"`+revision.String()+`"`, 1)), object)
	})
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{SchemaData: json.RawMessage(`{}`), UpdatedAt: time.Now()}}, deploymentReader{}, emptySecretResolver{}, catalog, fixedClock{time.Now()})
	require.NoError(t, err)
	first, err := svc.Compile(t.Context(), "acme", "candidate-1")
	require.NoError(t, err)
	before, err := json.Marshal(first.Value)
	require.NoError(t, err)
	repo.target = referencedCandidate(t, "v2").target
	repo.target.CandidateSlug = "candidate-2"
	repo.target.HeadSHA = strings.Repeat("c", 40)
	second, err := svc.Compile(t.Context(), "acme", "candidate-2")
	require.NoError(t, err)
	require.NotEqual(t, first.Value.Candidate.SpecHash, second.Value.Candidate.SpecHash)
	require.NotEqual(t, first.Value.ID, second.Value.ID)
	require.NotEqual(t, odcsClaim(t, first.Value.Claims, "/revision_validity").Evidence, odcsClaim(t, second.Value.Claims, "/revision_validity").Evidence)
	after, err := json.Marshal(first.Value)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestODCSPortCompatibilityUsesCanonicalEdgesAndDoesNotInferPeers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		consumer *payload.Contract
		peer     bool
		want     model.ProofVerdict
	}{
		{"compatible", &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString}}}, false, model.ProofVerdictPassed},
		{"incompatible", &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}, false, model.ProofVerdictFailed},
		{"unresolved peer", nil, true, model.ProofVerdictUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := referencedCandidate(t, "v1")
			var pipeline pipelinespec.Pipeline
			require.NoError(t, json.Unmarshal(repo.target.CanonicalSpec, &pipeline))
			sink := &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "extract"}, InputContract: tc.consumer, DataSourceSlug: "destination", OperationType: "sql", OperationRef: "insert", IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey}
			if tc.peer {
				sink.InputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "absent", RevisionSlug: "v1", SchemaObjectID: "order"}
			}
			pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: "load", Sink: sink})
			compiler := &Compiler{contracts: contractValidatorFunc(func(_ context.Context, _, _, contract, _ slug.Slug, object string) (*odcs.ConversionResult, error) {
				if contract == "absent" {
					return nil, odcscontracts.ErrNotFound
				}
				return odcs.Convert([]byte(proofODCSDocument), object)
			})}
			inputs, err := compiler.contractInputs(t.Context(), "acme", "data", &pipeline)
			require.NoError(t, err)
			require.Equal(t, tc.want, inputs[0].Compatibility)
			require.NotNil(t, pipeline.Steps[0].Query.OutputContractRef, "authored candidate stays immutable")
		})
	}
}

func TestODCSDependencyFailuresRemainErrors(t *testing.T) {
	t.Parallel()
	want := xerror.Unexpected().Detail("catalog unavailable")
	repo := referencedCandidate(t, "v1")
	var pipeline pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(repo.target.CanonicalSpec, &pipeline))
	compiler := &Compiler{contracts: contractValidatorFunc(func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error) {
		return nil, want
	})}
	_, err := compiler.contractInputs(t.Context(), "acme", "data", &pipeline)
	require.ErrorIs(t, err, want)
}

func odcsClaim(t *testing.T, claims []model.ProofClaim, suffix string) model.ProofClaim {
	t.Helper()
	for _, claim := range claims {
		if strings.HasSuffix(claim.Identity.SemanticPath, suffix) {
			return claim
		}
	}
	t.Fatalf("missing claim %s", suffix)
	return model.ProofClaim{}
}

type odcsSecrets struct{}

func (odcsSecrets) ResolveRuntimeSecrets(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
	return []model.RuntimeSecretValue{{Value: "private~value"}}, nil
}

func TestODCSPropertyIdentityPassesThroughReceiptRedaction(t *testing.T) {
	t.Parallel()
	repo := referencedCandidate(t, "v1")
	catalog := contractValidatorFunc(func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error) {
		return odcs.Convert([]byte(strings.Replace(proofODCSDocument, "order-id", "private~value", 1)), "order")
	})
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{SchemaData: json.RawMessage(`{}`), UpdatedAt: time.Now()}}, deploymentReader{}, odcsSecrets{}, catalog, fixedClock{time.Now()})
	require.NoError(t, err)
	receipt, err := svc.Compile(t.Context(), "acme", "candidate-1")
	require.NoError(t, err)
	raw, err := json.Marshal(receipt.Value)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private~value")
	require.Contains(t, string(raw), "[REDACTED]")
	require.NotContains(t, string(raw), "private~0value")
}

func TestODCSUnsupportedPropertyPathUsesStableID(t *testing.T) {
	t.Parallel()
	result, err := odcs.Convert([]byte(strings.Replace(proofODCSDocument, `"logicalType":"string"`, `"logicalType":"date"`, 1)), "order")
	require.NoError(t, err)
	require.True(t, result.DocumentValid)
	input := contractInput{Path: "/steps/extract/query/output_contract", Reference: pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}, Digest: result.Digest, DocumentValid: result.DocumentValid, Properties: result.Properties, Diagnostics: result.Diagnostics}
	claims, err := appendODCSClaims(nil, []contractInput{input}, nil, nil, "candidate", "evidence", time.Now(), func(s string) string { return s })
	require.NoError(t, err)
	declarations := 0
	for _, claim := range claims {
		if strings.Contains(claim.Identity.SemanticPath, "/declarations/") {
			declarations++
			require.Contains(t, claim.Identity.SemanticPath, "properties/ids/order-id/")
			require.Equal(t, model.ProofVerdictUnverified, claim.Verdict)
		}
	}
	require.Positive(t, declarations)
}

func TestODCSClaimBudgetRejectsExpansionWithoutPartialReceipt(t *testing.T) {
	input := contractInput{Path: "/steps/extract/query/output_contract", Reference: pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: strings.Repeat("x", odcs.MaxDocumentBytes/2)}}
	for range 20 {
		input.Coverage = append(input.Coverage, model.ODCSDeclarationCoverage{Path: "$.slaProperties", State: "unsupported", Reason: "no_runtime_evaluator"})
	}
	claims, err := appendODCSClaims(nil, []contractInput{input}, nil, nil, "candidate", "evidence", time.Now(), func(s string) string { return s })
	require.ErrorIs(t, err, xerror.ValidationFailed())
	require.Nil(t, claims)
	require.NotContains(t, xerror.PublicDetail(err), strings.Repeat("x", 100))
}

func TestODCSProofInputBudgetBoundsRepeatedPortCoverage(t *testing.T) {
	raw := strings.Replace(proofODCSDocument, `"version":"1"`, `"version":"1","tags":[`+strings.Repeat(`"tag",`, 1500)+`"tag"]`, 1)
	conversion, err := odcs.Convert([]byte(raw), "order")
	require.NoError(t, err)
	require.True(t, conversion.CoverageComplete)
	require.Greater(t, len(conversion.Coverage), 1500)
	compiler := &Compiler{contracts: contractValidatorFunc(func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error) {
		return conversion, nil
	})}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}}
	for i := range 100 {
		pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "query", Slug: slug.Slug(fmt.Sprintf("extract-%d", i)), Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OutputContractRef: &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}}})
	}
	inputs, err := compiler.contractInputs(t.Context(), "acme", "etl", pipeline)
	require.ErrorIs(t, err, xerror.ValidationFailed())
	require.Nil(t, inputs)
}

func TestODCSBindsEveryEligiblePortAndUsesIDsForSharedRevision(t *testing.T) {
	t.Parallel()
	ref := func() *pipelinespec.ODCSContractRef {
		return &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Representation: model.ContractRepresentationRows}
	}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "extract", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OutputContractRef: ref()}},
		{Kind: "transform", Slug: "query", Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"rows": {FromStep: "extract"}}, InputContractRef: ref(), OutputContractRef: ref(), OutputKind: model.TransformOutputKindRows}},
		{Kind: "sink", Slug: "load", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "query"}, InputContractRef: ref()}},
	}}
	reads := 0
	compiler := &Compiler{contracts: contractValidatorFunc(func(_ context.Context, org, project, contract, revision slug.Slug, object string) (*odcs.ConversionResult, error) {
		reads++
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("data"), project)
		require.Equal(t, slug.Slug("orders"), contract)
		require.Equal(t, slug.Slug("v1"), revision)
		return odcs.Convert([]byte(proofODCSDocument), object)
	})}
	inputs, err := compiler.contractInputs(t.Context(), "acme", "data", pipeline)
	require.NoError(t, err)
	require.Len(t, inputs, 4)
	require.Equal(t, 4, reads)
	claims, err := appendODCSClaims(nil, inputs, nil, nil, "candidate", "evidence", time.Now(), func(s string) string { return s })
	require.NoError(t, err)
	observations := 0
	for _, claim := range claims {
		if strings.HasSuffix(claim.Identity.SemanticPath, "/source_observation") {
			observations++
			require.Contains(t, claim.Identity.SemanticPath, "/steps/extract/query/")
		}
	}
	require.Equal(t, 1, observations)
	for _, input := range inputs {
		require.Equal(t, inputs[0].Digest, input.Digest)
		require.Equal(t, "order-id", input.Properties[0].ID)
	}
}

func TestODCSDoesNotPassUnsupportedMultiInputMapping(t *testing.T) {
	t.Parallel()
	repo := referencedCandidate(t, "v1")
	var pipeline pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(repo.target.CanonicalSpec, &pipeline))
	other := pipeline.Steps[0]
	other.Slug = "other"
	pipeline.Steps = append(pipeline.Steps, other)
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "combine", Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"first": {FromStep: "extract"}, "second": {FromStep: "other"}}, SQL: "select first.id from first join second on first.id=second.id", OutputKind: model.TransformOutputKindRows, InputContractRef: &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Representation: model.ContractRepresentationRows}}})
	compiler := &Compiler{contracts: contractValidatorFunc(func(_ context.Context, _, _, _, _ slug.Slug, object string) (*odcs.ConversionResult, error) {
		return odcs.Convert([]byte(proofODCSDocument), object)
	})}
	inputs, err := compiler.contractInputs(t.Context(), "acme", "data", &pipeline)
	require.NoError(t, err)
	require.True(t, inputs[2].DocumentValid)
	require.True(t, inputs[2].Supported)
	require.Equal(t, model.ProofVerdictUnverified, inputs[2].Compatibility)
}

func TestODCSNamedInlineRelationPreservesSupportedProducerCheck(t *testing.T) {
	t.Parallel()
	repo := referencedCandidate(t, "v1")
	var pipeline pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(repo.target.CanonicalSpec, &pipeline))
	conversion, err := odcs.Convert([]byte(proofODCSDocument), "order")
	require.NoError(t, err)
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "project", Transform: &pipelinespec.TransformSpec{
		SQL: "select id from order_rows", OutputKind: model.TransformOutputKindRows,
		Inputs:         map[string]pipelinespec.StepInput{"order_rows": {FromStep: "extract"}},
		InputContracts: map[string]*pipelinespec.InputContractAttachment{"order_rows": {Contract: conversion.Shape}},
	}})
	compiler := &Compiler{contracts: contractValidatorFunc(func(_ context.Context, _, _, _, _ slug.Slug, object string) (*odcs.ConversionResult, error) {
		return odcs.Convert([]byte(proofODCSDocument), object)
	})}
	inputs, err := compiler.contractInputs(t.Context(), "acme", "data", &pipeline)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, model.ProofVerdictPassed, inputs[0].Compatibility)
}
