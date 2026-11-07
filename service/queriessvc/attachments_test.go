package queriessvc

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	repoq "github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/stretchr/testify/require"
)

func TestSavedOperationAttachmentRoundTripAndReplacement(t *testing.T) {
	shape := &payload.Contract{Name: "rows", Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row"}
	create, err := toRepoCreateParams("db", &CreateParams{Name: "Orders", Slug: "orders", Data: json.RawMessage(`{"statement":"{}"}`), ExpectedInputContractRef: ref, OutputContract: shape})
	require.NoError(t, err)
	decoded, err := toQuery(&repoq.Query{Slug: "orders", Data: create.Data, ExpectedInputContractRef: create.ExpectedInputContractRef, OutputContract: create.OutputContract}, nil)
	require.NoError(t, err)
	require.Equal(t, ref, decoded.ExpectedInputContractRef)
	require.Equal(t, shape, decoded.OutputContract)
	update, err := toRepoUpdateParams(&UpdateParams{Name: "Orders", Data: create.Data, OutputContractRef: ref}, "orders")
	require.NoError(t, err)
	require.Nil(t, update.OutputContract)
	require.Nil(t, update.ExpectedInputContractRef)
	require.NotEmpty(t, update.OutputContractRef)
}

func TestSavedOperationAttachmentsFailBeforeRepositoryEffects(t *testing.T) {
	shape := &payload.Contract{Name: "rows", Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger}}}
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row"}
	for _, tc := range []struct {
		name                string
		input, output       *payload.Contract
		inputRef, outputRef *pipelinespec.ODCSContractRef
	}{
		{name: "exclusive output", output: shape, outputRef: ref},
		{name: "exclusive input", input: shape, inputRef: ref},
		{name: "latest rejected", outputRef: &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "latest", SchemaObjectID: "row"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &service{}
			_, err := svc.Create(t.Context(), "acme", "db", model.SystemEventActor(), &CreateParams{Name: "Orders", Data: json.RawMessage(`{}`), ExpectedInputContract: tc.input, ExpectedInputContractRef: tc.inputRef, OutputContract: tc.output, OutputContractRef: tc.outputRef})
			require.Error(t, err)
			_, err = svc.Update(t.Context(), "acme", "db", "orders", model.SystemEventActor(), &UpdateParams{Name: "Orders", Data: json.RawMessage(`{}`), ExpectedInputContract: tc.input, ExpectedInputContractRef: tc.inputRef, OutputContract: tc.output, OutputContractRef: tc.outputRef})
			require.Error(t, err)
		})
	}
}
