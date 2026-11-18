package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type UpsertRuntimeVariable struct {
	Key                              string                   `validate:"required,min=1,max=100"`
	ValueType                        RuntimeVariableValueType `validate:"required"`
	Value                            string                   `validate:"required,max=4000"`
	Description                      *string                  `validate:"omitempty,max=2000"`
	ProtectedEnvironmentConfirmation *slug.Slug
}

type RuntimeVariableDefinition struct {
	CreateOnly   bool
	Key          string
	ValueType    RuntimeVariableValueType
	Description  *string
	Required     bool
	DefaultValue *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type RuntimeVariableSource string

const (
	RuntimeVariableOverride RuntimeVariableSource = "override"
	RuntimeVariableDefault  RuntimeVariableSource = "default"
	RuntimeVariableMissing  RuntimeVariableSource = "missing"
)

type RuntimeVariableResolution struct {
	Source       RuntimeVariableSource
	Variable     *RuntimeVariable
	Required     bool
	TypeMismatch bool
}

func (r RuntimeVariableResolution) Blocked() bool {
	return r.TypeMismatch || (r.Required && r.Source == RuntimeVariableMissing)
}

// ResolveRuntimeVariable resolves a single key in one environment.
func ResolveRuntimeVariable(environment slug.Slug, definition *RuntimeVariableDefinition, override *RuntimeVariable, used bool) RuntimeVariableResolution {
	result := RuntimeVariableResolution{Source: RuntimeVariableMissing, Required: used}
	if definition != nil {
		result.Required = result.Required || definition.Required
	}
	if override != nil {
		result.Source = RuntimeVariableOverride
		result.Variable = override
		result.TypeMismatch = definition != nil && definition.ValueType != override.ValueType
	} else if definition != nil && definition.DefaultValue != nil {
		result.Source = RuntimeVariableDefault
		result.Variable = &RuntimeVariable{
			EnvironmentSlug: environment,
			Key:             definition.Key,
			ValueType:       definition.ValueType,
			Value:           *definition.DefaultValue,
			Description:     definition.Description,
			CreatedAt:       definition.CreatedAt,
			UpdatedAt:       definition.UpdatedAt,
		}
	}
	return result
}
