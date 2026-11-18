package model

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestResolveRuntimeVariable(t *testing.T) {
	defaultValue := ""
	definition := RuntimeVariableDefinition{
		Key: "region", ValueType: RuntimeVariableValueTypeText, DefaultValue: &defaultValue,
	}
	override := RuntimeVariable{Key: "region", ValueType: RuntimeVariableValueTypeText, Value: "west"}
	got := ResolveRuntimeVariable("dev", &definition, &override, true)
	if got.Source != RuntimeVariableOverride || got.Variable != &override || got.Blocked() {
		t.Fatalf("override resolution = %+v", got)
	}
	got = ResolveRuntimeVariable("stg", &definition, nil, true)
	if got.Source != RuntimeVariableDefault || got.Variable.Value != "" || got.Variable.EnvironmentSlug != "stg" || got.Blocked() {
		t.Fatalf("default resolution = %+v", got)
	}
	override.ValueType = RuntimeVariableValueTypeInteger
	got = ResolveRuntimeVariable("dev", &definition, &override, false)
	if got.Source != RuntimeVariableOverride || !got.Blocked() || !got.TypeMismatch {
		t.Fatalf("type mismatch resolution = %+v", got)
	}
	definition.DefaultValue = nil
	definition.Required = true
	got = ResolveRuntimeVariable("prod", &definition, nil, false)
	if got.Source != RuntimeVariableMissing || got.Variable != nil || !got.Blocked() {
		t.Fatalf("required resolution = %+v", got)
	}
	if ResolveRuntimeVariable("prod", nil, nil, false).Blocked() || !ResolveRuntimeVariable("prod", nil, nil, true).Blocked() {
		t.Fatal("a missing legacy key should block only when used")
	}
}

func TestCompareRuntimeVariables(t *testing.T) {
	comparison := CompareRuntimeVariables(
		[]slug.Slug{"dev", "stg", "prod"},
		[]RuntimeVariableDefinition{
			{Key: "limit", ValueType: RuntimeVariableValueTypeInteger, DefaultValue: new("200")},
			{Key: "region", ValueType: RuntimeVariableValueTypeText, Required: true},
		},
		[]RuntimeVariable{
			{EnvironmentSlug: "dev", Key: "limit", ValueType: RuntimeVariableValueTypeInteger, Value: "100"},
			{EnvironmentSlug: "prod", Key: "limit", ValueType: RuntimeVariableValueTypeInteger, Value: "500"},
		},
		[]string{"unconfigured"},
	)
	if len(comparison.Rows) != 3 {
		t.Fatalf("comparison has %d rows", len(comparison.Rows))
	}
	for i, source := range []RuntimeVariableSource{RuntimeVariableOverride, RuntimeVariableDefault, RuntimeVariableOverride} {
		cell := comparison.Rows[0].Cells[i]
		if cell.Source != source || cell.Blocked() {
			t.Fatalf("limit in %s = %+v", comparison.Environments[i], cell)
		}
	}
	for _, row := range comparison.Rows[1:] {
		for _, cell := range row.Cells {
			if cell.Source != RuntimeVariableMissing || !cell.Blocked() {
				t.Fatalf("%s cell = %+v", row.Key, cell)
			}
		}
	}
}
