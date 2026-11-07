package http

import (
	"encoding/json"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

func TestSavedQueryAttachmentHTTPMapping(t *testing.T) {
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject}
	encoded := operationContractRefToAPI(ref)
	decoded, _, err := apiOperationContractRef(encoded)
	require.NoError(t, err)
	require.Equal(t, ref, decoded)
	_, _, err = apiOperationContractRef(&api.PipelineODCSContractRef{ContractSlug: "bad slug", RevisionSlug: "v1", SchemaObjectId: "row"})
	require.Error(t, err)
	contract := &payload.Contract{Name: "rows", Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
	q, err := queryToHTTP(&queriessvc.Query{Slug: "orders", Name: "Orders", DataSourceSlug: "db", Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT 1\"}"}`), ExpectedInputContractRef: ref, OutputContract: contract})
	require.NoError(t, err)
	require.Equal(t, encoded, q.ExpectedInputContractRef)
	require.NotNil(t, q.OutputContract)
	require.Equal(t, "rows", q.OutputContract.Name)
	require.Error(t, validateQueryContractValidationKeys([]byte(`{"output_contract":{"fields":[{"validations":{"arbitrary":true}}]}}`)))
}
