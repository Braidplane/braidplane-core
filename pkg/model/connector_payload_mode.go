package model

// ConnectorPayloadMode distinguishes native inputs from conversion and binding support.
//
//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=ConnectorPayloadMode -json -text -linecomment -output=connector_payload_mode_enumer.go
type ConnectorPayloadMode int

const (
	ConnectorPayloadModeUnknown         ConnectorPayloadMode = iota // unknown
	ConnectorPayloadModeNative                                      // native
	ConnectorPayloadModeSerialized                                  // serialized
	ConnectorPayloadModeRowNormalized                               // row_normalized
	ConnectorPayloadModeBindingFallback                             // binding_fallback
)
