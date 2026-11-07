package hubspot

import (
	"context"
	"encoding/json"
)

// Credentials holds datasource-specific HubSpot credential payload fields.
// PrivateAppToken is retained only for the static-secret API contract; OAuth
// grants arrive through AccessToken and are adapted to the neutral CRM client.
type Credentials struct {
	PrivateAppToken string   `json:"private_app_token,omitempty" validate:"omitempty,min=1"`
	AccessToken     string   `json:"access_token,omitempty" validate:"omitempty,min=1"`
	GrantedScopes   []string `json:"granted_scopes,omitempty"`
}

const (
	operationList   = "list"
	operationSearch = "search"

	// OperationUpsert is the saved-query operation that consumes row-object sink input.
	OperationUpsert = "upsert"

	// RecordIDFieldName is the adapter-owned column name for HubSpot record IDs.
	RecordIDFieldName = "hs_object_id"
)

// SchemaIntrospector exposes HubSpot property discovery for supported CRM objects.
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context) (*Schema, error)
}

type Schema struct {
	Objects []ObjectSchema `json:"objects"`
}

type ObjectSchema struct {
	Name             string            `json:"name"`
	Label            string            `json:"label"`
	Properties       []PropertySchema  `json:"properties"`
	Operations       []OperationSchema `json:"operations"`
	ProviderMetadata map[string]any    `json:"provider_metadata,omitempty"`
}

type PropertySchema struct {
	Name             string         `json:"name"`
	Label            string         `json:"label"`
	Type             string         `json:"type"`
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
}

type OperationSchema struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

type statement struct {
	Type            string            `json:"type,omitempty"`
	ObjectType      string            `json:"object_type"`
	Operation       string            `json:"operation,omitempty"`
	Properties      []string          `json:"properties,omitempty"`
	Query           *string           `json:"query,omitempty"`
	Sorts           []string          `json:"sorts,omitempty"`
	After           *string           `json:"after,omitempty"`
	Limit           *int              `json:"limit,omitempty"`
	FilterGroups    []filterGroup     `json:"filter_groups,omitempty"`
	IDProperty      string            `json:"id_property,omitempty"`
	PropertyMapping map[string]string `json:"property_mapping,omitempty"`
	Rows            json.RawMessage   `json:"rows,omitempty"`
}

type filterGroup struct {
	Filters []filter `json:"filters"`
}

type filter struct {
	PropertyName string `json:"property_name"`
	Operator     string `json:"operator"`
	Value        any    `json:"value,omitempty"`
	HighValue    any    `json:"high_value,omitempty"`
	Values       []any  `json:"values,omitempty"`
}
