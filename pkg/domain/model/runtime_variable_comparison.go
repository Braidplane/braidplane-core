package model

import (
	"maps"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type RuntimeVariableComparison struct {
	Environments []slug.Slug
	Rows         []RuntimeVariableComparisonRow
}

type RuntimeVariableComparisonRow struct {
	Key        string
	Definition *RuntimeVariableDefinition
	Used       bool
	Cells      []RuntimeVariableResolution
}

// CompareRuntimeVariables gives each defined, used, or overridden key a cell in every environment.
func CompareRuntimeVariables(
	environments []slug.Slug,
	definitions []RuntimeVariableDefinition,
	overrides []RuntimeVariable,
	usedKeys []string,
) RuntimeVariableComparison {
	rows := make(map[string]RuntimeVariableComparisonRow)
	for i := range definitions {
		definition := &definitions[i]
		rows[definition.Key] = RuntimeVariableComparisonRow{Key: definition.Key, Definition: definition}
	}
	byKey := make(map[string]map[slug.Slug]*RuntimeVariable)
	for i := range overrides {
		override := &overrides[i]
		if _, exists := rows[override.Key]; !exists {
			rows[override.Key] = RuntimeVariableComparisonRow{Key: override.Key}
		}
		if byKey[override.Key] == nil {
			byKey[override.Key] = make(map[slug.Slug]*RuntimeVariable)
		}
		byKey[override.Key][override.EnvironmentSlug] = override
	}
	for _, key := range usedKeys {
		row := rows[key]
		row.Key = key
		row.Used = true
		rows[key] = row
	}

	comparison := RuntimeVariableComparison{
		Environments: environments,
		Rows:         make([]RuntimeVariableComparisonRow, 0, len(rows)),
	}
	for _, key := range slices.Sorted(maps.Keys(rows)) {
		row := rows[key]
		row.Cells = make([]RuntimeVariableResolution, 0, len(environments))
		for _, environment := range environments {
			row.Cells = append(row.Cells, ResolveRuntimeVariable(environment, row.Definition, byKey[key][environment], row.Used))
		}
		comparison.Rows = append(comparison.Rows, row)
	}
	return comparison
}
