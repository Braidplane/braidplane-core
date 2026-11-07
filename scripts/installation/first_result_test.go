package installation_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transform "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
)

func TestFirstResultFixture(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile("first-result/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	pipeline, err := spec.Load("first-result.pipeline.json", read("first-result.pipeline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.Validate(pipeline); err != nil {
		t.Fatal(err)
	}
	step := pipeline.Steps[1].Transform
	inferred, err := transform.InferOutputContractWithInputSchema(step.SQL, step.OutputKind,
		model.DataSourceTypeGit, map[string]model.ContractFieldType{"value": model.ContractFieldTypeValueInteger})
	if err != nil {
		t.Fatal(err)
	}
	if inferred.Fields[0].Type != step.OutputContract.Fields[0].Type {
		t.Fatal("arithmetic output contract must match server inference")
	}
	result, err := transform.NewRuntime().Execute(transform.Transform{SQL: step.SQL, OutputKind: step.OutputKind},
		map[string]*payload.Payload{"fixture": {Representation: model.ContractRepresentationJSON, Document: read("input.json")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := transform.ValidateOutputContract(result, step.OutputContract); err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(result.Rows)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(read("expected.json"), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first result = %v; want %v", got, want)
	}
}
