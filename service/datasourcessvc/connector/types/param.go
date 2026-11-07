package types

import "time"

// TypedParam represents a query parameter with its value and declared data type.
type TypedParam struct {
	Value    any
	DataType string // Parameter data type from query definition (text, integer, boolean, date, uuid)
}

// ExecutionLimits carries per-execution safety guardrails into connectors.
type ExecutionLimits struct {
	ReadOnly             bool
	ConnectorTimeout     time.Duration
	MaxResultRows        int
	MaxResponseBytes     int64
	HTTPTimeout          time.Duration
	HTTPMaxRedirects     int
	HTTPMaxRequestBytes  int64
	HTTPMaxResponseBytes int64
	HTTPRetryAttempts    int
	HTTPRetryBackoff     time.Duration
}

// ResolveExecutionLimits returns the first supplied limits value, or zero limits.
func ResolveExecutionLimits(limits ...ExecutionLimits) ExecutionLimits {
	if len(limits) == 0 {
		return ExecutionLimits{}
	}

	return limits[0]
}
