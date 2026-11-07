package connector

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// Connector verifies connectivity and manages connector lifecycle.
type Connector interface {
	// Execute runs the query described by queryData with the provided TypedParam values.
	// Each connector is responsible for normalizing parameter values based on DataType.
	// Returns a structured ExecutionResult containing type-specific data and metadata.
	Execute(
		ctx context.Context,
		queryData json.RawMessage,
		params map[string]types.TypedParam,
		limits ...types.ExecutionLimits,
	) (*result.ExecutionResult, error)

	// TestConnection verifies that a connection to the data source can be established
	// with the configured credentials.
	TestConnection(ctx context.Context) error

	// Close closes the connector and releases any held resources (connection pools, etc.).
	Close() error
}
