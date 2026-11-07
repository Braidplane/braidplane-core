package model

import "github.com/Braidplane/braidplane-core/pkg/slug"

type UpsertRuntimeVariable struct {
	Key                              string                   `validate:"required,min=1,max=100"`
	ValueType                        RuntimeVariableValueType `validate:"required"`
	Value                            string                   `validate:"required,max=4000"`
	Description                      *string                  `validate:"omitempty,max=2000"`
	ProtectedEnvironmentConfirmation *slug.Slug
}
