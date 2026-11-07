package connector

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// NewConnectorFunc is the signature for a connector factory function.
// It creates a connector for the given data source type with raw config and credentials payloads.
type NewConnectorFunc func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (Connector, error)

// NewConnector is the production connector factory function.
// It handles config/credentials unmarshaling and instantiation for all supported data source types.
func NewConnector(
	ctx context.Context,
	dsType model.DataSourceType,
	configJSON, credentialsJSON []byte,
) (Connector, error) {
	if dsType == model.DataSourceTypeUnknown {
		return nil, xerror.Unexpected().Detail(
			"cannot create connector for unknown data source type",
		)
	}

	definition, ok := connectorDefinitionFor(dsType)
	if !ok {
		return nil, xerror.Unexpected().Detailf("unsupported data source type: %s", dsType.String())
	}

	return definition.newConnector(ctx, configJSON, credentialsJSON)
}
