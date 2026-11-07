package crm

import "encoding/json"

type Config struct {
	BaseURL           string `json:"base_url,omitempty" validate:"omitempty,url"`
	TimeoutSeconds    int    `json:"timeout_seconds,omitempty" validate:"omitempty,min=1,max=300"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

type AccountDetails struct {
	AccountID   int64  `json:"portalId"`
	CompanyName string `json:"companyName"`
}

type Object struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

type CustomObjectSchema struct {
	ObjectTypeID string
	Name         string
	Label        string
	Properties   []Property
}

type Property struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Type           string `json:"type"`
	HasUniqueValue bool   `json:"has_unique_value,omitempty"`
	ReadOnlyValue  bool   `json:"read_only_value,omitempty"`
}

type ListRequest struct {
	ObjectType       string
	Properties       []string
	After            string
	Limit            int
	MaxResponseBytes int64
}

type SearchRequest struct {
	ObjectType       string
	Properties       []string
	Query            string
	Sorts            []string
	After            string
	Limit            int
	FilterGroups     []FilterGroup
	MaxResponseBytes int64
}

type UpsertRequest struct {
	ObjectType       string
	Inputs           []UpsertInput
	MaxResponseBytes int64
}

type UpsertInput struct {
	ID         string
	IDProperty string
	Properties map[string]any
}

type UpsertResult struct {
	Records []UpsertRecord
}

type UpsertRecord struct {
	ID      string
	Created bool
}

type FilterGroup struct {
	Filters []Filter
}

type Filter struct {
	PropertyName string
	Operator     string
	Value        any
	HighValue    any
	Values       []any
}

type Page struct {
	ObjectType string
	Records    []Record
	NextAfter  *string
	HasMore    bool
}

type Record struct {
	ID         string
	Properties map[string]any
}

type accountDetailsResponse struct {
	AccountID   int64  `json:"portalId"`
	CompanyName string `json:"companyName"`
}

type propertiesResponse struct {
	Results []propertyResponse `json:"results"`
}

type propertyResponse struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Type           string `json:"type"`
	FormField      bool   `json:"formField"`
	HasUniqueValue bool   `json:"hasUniqueValue"`
	ReadOnlyValue  bool   `json:"readOnlyValue"`
}

type customObjectSchemasResponse struct {
	Results []customObjectSchemaResponse `json:"results"`
}

type customObjectSchemaResponse struct {
	Archived     bool               `json:"archived"`
	Name         string             `json:"name"`
	ObjectTypeID string             `json:"objectTypeId"`
	Labels       objectSchemaLabels `json:"labels"`
	Properties   []propertyResponse `json:"properties"`
}

type objectSchemaLabels struct {
	Plural   string `json:"plural"`
	Singular string `json:"singular"`
}

type objectsResponse struct {
	Results []objectRecord `json:"results"`
	Paging  struct {
		Next struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

type objectRecord struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
}

type batchUpsertResponse struct {
	Status  string              `json:"status"`
	Results []batchUpsertRecord `json:"results"`
	Errors  []json.RawMessage   `json:"errors"`
}

type batchUpsertRecord struct {
	ID      string `json:"id"`
	Created bool   `json:"new"`
}
