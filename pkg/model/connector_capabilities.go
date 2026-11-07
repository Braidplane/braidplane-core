package model

import "slices"

// ConnectorCapabilityDetails describes implemented operations; it grants no
// permissions and is not evidence that a provider accepted an operation.
type ConnectorCapabilityDetails struct {
	ReadOperations  []string                       `json:"read_operations"`
	WriteOperations []string                       `json:"write_operations"`
	LoadModes       []string                       `json:"load_modes"`
	SourcePayloads  []ContractRepresentation       `json:"source_payloads"`
	SinkPayloads    []ConnectorSinkPayloadSupport  `json:"sink_payloads"`
	Pagination      ConnectorPaginationSupport     `json:"pagination"`
	Authentication  ConnectorAuthenticationSupport `json:"authentication"`
	Limitations     []string                       `json:"limitations"`
}

type ConnectorSinkPayloadSupport struct {
	Representation ContractRepresentation `json:"representation"`
	Mode           ConnectorPayloadMode   `json:"mode"`
	Limitation     string                 `json:"limitation"`
}

type ConnectorPaginationSupport struct {
	Methods    []string `json:"methods"`
	Scope      string   `json:"scope"`
	Limitation string   `json:"limitation"`
	Throttling string   `json:"throttling"`
}

type ConnectorAuthenticationSupport struct {
	Modes    []string `json:"modes"`
	Renewal  string   `json:"renewal"`
	Recovery string   `json:"recovery"`
}

// Clone keeps UI and CLI consumers from mutating the catalog's nested slices.
func (d ConnectorCapabilityDetails) Clone() ConnectorCapabilityDetails {
	d.ReadOperations = slices.Clone(d.ReadOperations)
	d.WriteOperations = slices.Clone(d.WriteOperations)
	d.LoadModes = slices.Clone(d.LoadModes)
	d.SourcePayloads = slices.Clone(d.SourcePayloads)
	d.SinkPayloads = slices.Clone(d.SinkPayloads)
	d.Pagination.Methods = slices.Clone(d.Pagination.Methods)
	d.Authentication.Modes = slices.Clone(d.Authentication.Modes)
	d.Limitations = slices.Clone(d.Limitations)
	return d
}
