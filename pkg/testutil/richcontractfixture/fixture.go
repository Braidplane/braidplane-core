// Package richcontractfixture supplies synthetic rich-port admission material.
package richcontractfixture

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

const Document = `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","logicalType":"object","properties":[{"id":"id","name":"id","logicalType":"integer","required":true},{"id":"region","name":"region","logicalType":"string","required":true},{"name":"created","logicalType":"timestamp","required":true},{"name":"embedding","logicalType":"vector","required":true,"logicalTypeOptions":{"dimensions":3}},{"name":"context","logicalType":"object","properties":[{"name":"label","logicalType":"${LABEL_TYPE}","required":true}]}],"relationships":[{"from":["schema/order/properties/region","schema/order/properties/id"],"to":["schema/customer/properties/region","schema/customer/properties/id"]}]},{"id":"customer","name":"Customers","properties":[{"id":"id","name":"id","logicalType":"integer","required":true},{"id":"region","name":"region","logicalType":"string","required":true}]}]}`

const ValidJSON = `{"id":1,"region":"eu","created":"2026-10-20T12:00:00.000Z","embedding":[0.5,1,2],"context":{"label":"synthetic"}}`

func Selection(representation model.ContractRepresentation, root model.ContractRootShape) (*model.ODCSRevision, *odcs.ProjectionRequest, error) {
	revision, err := odcs.Record(json.RawMessage(Document), "orders")
	if err != nil {
		return nil, nil, err
	}
	mapping := odcs.ProjectionMapping{ID: "synthetic/rich/v1", Representation: representation, RootShape: root, Types: map[payload.StructuralKind]bool{payload.StructuralObject: true, payload.StructuralInteger: true, payload.StructuralString: true, payload.StructuralInstant: true, payload.StructuralVector: true, payload.StructuralNumber: true, payload.StructuralBoolean: true}}
	inputs := []odcs.ProjectionInput{}
	for _, object := range []string{"order", "customer"} {
		inputs = append(inputs, odcs.ProjectionInput{Contract: "orders", Revision: "v1", ObjectID: object, Digest: revision.Digest, Document: revision.Document, Mapping: mapping, Variables: map[string]odcs.ResolutionValue{"LABEL_TYPE": {Value: "string", Revision: "variables-v1"}}})
	}
	return revision, &odcs.ProjectionRequest{Inputs: inputs, Relationships: []odcs.RelationshipSelection{{Input: 0, Target: 1, Path: "$.schema[0].relationships[0]"}}}, nil
}

func Reference(representation model.ContractRepresentation, root model.ContractRootShape) *pipelinespec.ODCSContractRef {
	return &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order", Representation: representation, RootShape: root}
}
