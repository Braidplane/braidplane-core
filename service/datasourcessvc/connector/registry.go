package connector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	workspaceconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	bigqueryconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	ftpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	mysqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	postgresconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	snowflakeconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake"
)

type connectorDefinition struct {
	dataSourceType     model.DataSourceType
	operationType      string
	configDecoder      connectorPayloadDecoder
	credentialsDecoder connectorPayloadDecoder
	prepare            connectorPayloadPreparation
	privateHostCheck   connectorPrivateHostCheck
	constructor        connectorPayloadConstructor
}

// DecodedConnectorPayloads carries typed connector config and credentials for
// callers that need validation without constructing a live connector.
type DecodedConnectorPayloads struct {
	Config                any
	Credentials           any
	PrivateHostsRequested bool
}

type connectorDefinitionOption[Config any, Credentials any] func(*connectorDefinitionBuilder[Config, Credentials])

type connectorDefinitionBuilder[Config any, Credentials any] struct {
	preparations     []connectorConfigPreparation[Config, Credentials]
	privateHostCheck func(Config) bool
}

type (
	connectorPayloadDecoder                                 func([]byte) (any, error)
	connectorPayloadPreparation                             func(any, any) (any, any, error)
	connectorPrivateHostCheck                               func(any) (bool, error)
	connectorPayloadConstructor                             func(context.Context, any, any) (Connector, error)
	connectorConfigPreparation[Config any, Credentials any] func(*Config, Credentials) error
)

type connectorDefinitionRegistry struct {
	definitions []connectorDefinition
	byType      map[model.DataSourceType]connectorDefinition
}

var productionConnectorRegistry = mustConnectorRegistry(
	defineConnector(
		model.DataSourceTypeGoogleWorkspace,
		catalog.OperationTypeGoogleWorkspace,
		workspaceconnector.New,
		withConfigPreparation(func(cfg *workspace.Config, _ provider.Credentials) error { return cfg.Validate() }),
	),
	defineConnector(
		model.DataSourceTypePostgres,
		catalog.OperationTypeSQL,
		func(ctx context.Context, cfg postgresconnector.Config, _ postgresconnector.Credentials) (*postgresconnector.Connector, error) {
			if endpoint, ok := ctx.Value(privatePostgresContextKey{}).(PrivatePostgresEndpoint); ok && endpoint.Address.IsValid() {
				cfg.DialFunc = endpoint.dialContext
				cfg.MaxConns = endpoint.MaxConns
			}
			if endpoint, ok := ctx.Value(policyPostgresContextKey{}).(PrivatePostgresEndpoint); ok && endpoint.Address.IsValid() {
				cfg.DialFunc = endpoint.dialContext
				cfg.MaxConns = 1
				cfg.Authorize = endpoint.Authorize
			}
			return postgresconnector.New(ctx, cfg)
		},
		withConfigPreparation(func(cfg *postgresconnector.Config, creds postgresconnector.Credentials) error {
			sqlconnector.ApplyCredentialDefaults(
				&cfg.User,
				&cfg.Password,
				&cfg.ConnectTimeout,
				sqlconnector.Credentials{Username: creds.Username, Password: creds.Password},
			)
			return nil
		}),
		withPrivateHostCheck[postgresconnector.Config, postgresconnector.Credentials](
			func(cfg postgresconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeMySQL,
		catalog.OperationTypeSQL,
		func(ctx context.Context, cfg mysqlconnector.Config, _ mysqlconnector.Credentials) (*mysqlconnector.Connector, error) {
			return mysqlconnector.New(ctx, cfg)
		},
		withConfigPreparation(func(cfg *mysqlconnector.Config, creds mysqlconnector.Credentials) error {
			if creds.Password == nil {
				return xerror.ValidationFailed().Detail("MySQL password field is required; use an empty string for a passwordless account")
			}
			sqlconnector.ApplyCredentialDefaults(
				&cfg.User,
				&cfg.Password,
				&cfg.ConnectTimeout,
				sqlconnector.Credentials{Username: creds.Username, Password: *creds.Password},
			)
			return nil
		}),
		withPrivateHostCheck[mysqlconnector.Config, mysqlconnector.Credentials](
			func(cfg mysqlconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeBigQuery,
		catalog.OperationTypeBigQuery,
		bigqueryconnector.New,
	),
	defineConnector(
		model.DataSourceTypeSnowflake,
		catalog.OperationTypeSnowflake,
		func(ctx context.Context, cfg snowflakeconnector.Config, creds snowflakeconnector.Credentials) (*snowflakeconnector.Connector, error) {
			cfg.WIFTokenSource, _ = ctx.Value(snowflakeTokenSourceContextKey{}).(func(context.Context) (string, error))
			return snowflakeconnector.New(ctx, cfg, creds)
		},
		withConfigPreparation(func(cfg *snowflakeconnector.Config, creds snowflakeconnector.Credentials) error {
			if len(cfg.WIFObjects) > 0 {
				if err := cfg.ValidateOnboarding(); err != nil {
					return err
				}
			}
			return snowflakeconnector.ValidateCredentials(creds)
		}),
	),
	defineConnector(
		model.DataSourceTypeHTTP,
		catalog.OperationTypeHTTP,
		httpconnector.New,
		withConfigPreparation(func(_ *httpconnector.Config, creds httpconnector.Credentials) error {
			return httpconnector.ValidateCredentials(creds)
		}),
		withPrivateHostCheck[httpconnector.Config, httpconnector.Credentials](
			func(cfg httpconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeMongoDB,
		catalog.OperationTypeMongoDB,
		mongoconnector.New,
		withPrivateHostCheck[mongoconnector.Config, mongoconnector.Credentials](
			func(cfg mongoconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeHubSpot,
		catalog.OperationTypeHubSpot,
		hubspotconnector.New,
	),
	defineConnector(
		model.DataSourceTypeFTP,
		catalog.OperationTypeFTP,
		ftpconnector.New,
		withPrivateHostCheck[ftpconnector.Config, ftpconnector.Credentials](
			func(cfg ftpconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeGit,
		catalog.OperationTypeGit,
		gitconnector.New,
		withPrivateHostCheck[gitconnector.Config, gitconnector.Credentials](
			func(cfg gitconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeGithub,
		catalog.OperationTypeGit,
		gitconnector.New,
		withPrivateHostCheck[gitconnector.Config, gitconnector.Credentials](
			func(cfg gitconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeGitlab,
		catalog.OperationTypeGit,
		gitconnector.New,
		withPrivateHostCheck[gitconnector.Config, gitconnector.Credentials](
			func(cfg gitconnector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
	defineConnector(
		model.DataSourceTypeS3,
		catalog.OperationTypeS3,
		s3connector.New,
		withPrivateHostCheck[s3connector.Config, s3connector.Credentials](
			func(cfg s3connector.Config) bool {
				return cfg.AllowPrivateHosts
			},
		),
	),
)

// DecodeConnectorPayloads decodes config and credentials through the production
// registry without constructing a connector.
func DecodeConnectorPayloads(
	dsType model.DataSourceType,
	configJSON, credentialsJSON []byte,
) (DecodedConnectorPayloads, bool, error) {
	definition, ok := connectorDefinitionFor(dsType)
	if !ok {
		return DecodedConnectorPayloads{}, false, nil
	}

	payloads, err := definition.decodePayloads(configJSON, credentialsJSON)
	if err != nil {
		return DecodedConnectorPayloads{}, true, err
	}
	return payloads, true, nil
}

// newConnector decodes raw payloads, applies definition-specific preparation,
// and invokes the connector constructor captured by the registry entry.
func (definition connectorDefinition) newConnector(
	ctx context.Context,
	configJSON, credentialsJSON []byte,
) (Connector, error) {
	payloads, err := definition.decodePayloads(configJSON, credentialsJSON)
	if err != nil {
		return nil, err
	}

	return definition.constructor(ctx, payloads.Config, payloads.Credentials)
}

// decodePayloads returns prepared typed payloads and connector-owned validation
// facts that service callers need before construction.
func (definition connectorDefinition) decodePayloads(
	configJSON, credentialsJSON []byte,
) (DecodedConnectorPayloads, error) {
	config, err := definition.configDecoder(configJSON)
	if err != nil {
		return DecodedConnectorPayloads{}, err
	}

	credentials, err := definition.credentialsDecoder(credentialsJSON)
	if err != nil {
		return DecodedConnectorPayloads{}, err
	}

	config, credentials, err = definition.prepare(config, credentials)
	if err != nil {
		return DecodedConnectorPayloads{}, err
	}

	privateHostsRequested, err := definition.privateHostCheck(config)
	if err != nil {
		return DecodedConnectorPayloads{}, err
	}

	return DecodedConnectorPayloads{
		Config:                config,
		Credentials:           credentials,
		PrivateHostsRequested: privateHostsRequested,
	}, nil
}

// connectorDefinitionFor returns the explicit construction definition for a
// supported data source type.
func connectorDefinitionFor(dsType model.DataSourceType) (connectorDefinition, bool) {
	return productionConnectorRegistry.definitionFor(dsType)
}

// allConnectorDefinitions returns the reviewed registry entries for consistency tests.
func allConnectorDefinitions() []connectorDefinition {
	return productionConnectorRegistry.allDefinitions()
}

// defineConnector creates one typed registry entry and keeps raw JSON decoding
// separate from connector-specific preparation and construction.
func defineConnector[Config, Credentials any, T Connector](
	dsType model.DataSourceType,
	operationType string,
	constructor func(context.Context, Config, Credentials) (T, error),
	options ...connectorDefinitionOption[Config, Credentials],
) connectorDefinition {
	builder := connectorDefinitionBuilder[Config, Credentials]{}
	for _, option := range options {
		option(&builder)
	}

	return connectorDefinition{
		dataSourceType: dsType,
		operationType:  operationType,
		configDecoder: func(raw []byte) (any, error) {
			return decodeConnectorPayload[Config](raw)
		},
		credentialsDecoder: func(raw []byte) (any, error) {
			return decodeConnectorPayload[Credentials](raw)
		},
		prepare: func(config, credentials any) (any, any, error) {
			typedConfig, ok := config.(Config)
			if !ok {
				return nil, nil, xerror.Unexpected().Detailf(
					"connector registry decoded unexpected config type for %s",
					dsType.String(),
				)
			}
			typedCredentials, ok := credentials.(Credentials)
			if !ok {
				return nil, nil, xerror.Unexpected().Detailf(
					"connector registry decoded unexpected credentials type for %s",
					dsType.String(),
				)
			}

			for _, prepare := range builder.preparations {
				if err := prepare(&typedConfig, typedCredentials); err != nil {
					return nil, nil, err
				}
			}
			return typedConfig, typedCredentials, nil
		},
		privateHostCheck: func(config any) (bool, error) {
			if builder.privateHostCheck == nil {
				return false, nil
			}
			typedConfig, ok := config.(Config)
			if !ok {
				return false, xerror.Unexpected().Detailf(
					"connector registry prepared unexpected private host config type for %s",
					dsType.String(),
				)
			}
			return builder.privateHostCheck(typedConfig), nil
		},
		constructor: func(ctx context.Context, config, credentials any) (Connector, error) {
			typedConfig, ok := config.(Config)
			if !ok {
				return nil, xerror.Unexpected().Detailf(
					"connector registry prepared unexpected config type for %s",
					dsType.String(),
				)
			}
			typedCredentials, ok := credentials.(Credentials)
			if !ok {
				return nil, xerror.Unexpected().Detailf(
					"connector registry prepared unexpected credentials type for %s",
					dsType.String(),
				)
			}

			conn, err := constructor(ctx, typedConfig, typedCredentials)
			if err != nil {
				return nil, err
			}
			return conn, nil
		},
	}
}

// withPrivateHostCheck marks connector configs where customer payloads can
// request private-host access that deployment policy must approve.
func withPrivateHostCheck[Config, Credentials any](
	check func(Config) bool,
) connectorDefinitionOption[Config, Credentials] {
	return func(builder *connectorDefinitionBuilder[Config, Credentials]) {
		builder.privateHostCheck = check
	}
}

// withConfigPreparation registers connector-local config preparation such as
// credential merging, defaults, or construction-time validation.
func withConfigPreparation[Config, Credentials any](
	prepare connectorConfigPreparation[Config, Credentials],
) connectorDefinitionOption[Config, Credentials] {
	return func(builder *connectorDefinitionBuilder[Config, Credentials]) {
		builder.preparations = append(builder.preparations, prepare)
	}
}

// decodeConnectorPayload maps malformed connector payload JSON to validation
// failure while making config and credential decoding reusable across connector types.
func decodeConnectorPayload[T any](raw []byte) (T, error) {
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, xerror.ValidationFailed().With(err)
	}
	return payload, nil
}

// mustConnectorRegistry builds the keyed registry and fails fast when reviewed
// definitions contain unknown types, duplicate entries, or incomplete hooks.
func mustConnectorRegistry(definitions ...connectorDefinition) connectorDefinitionRegistry {
	registry := connectorDefinitionRegistry{
		definitions: append([]connectorDefinition(nil), definitions...),
		byType:      make(map[model.DataSourceType]connectorDefinition, len(definitions)),
	}

	for _, definition := range definitions {
		if definition.dataSourceType == model.DataSourceTypeUnknown {
			panic("connector registry cannot define unknown data source type")
		}
		if definition.operationType == "" {
			panic(fmt.Sprintf("connector registry operation type missing for %s", definition.dataSourceType))
		}
		if definition.configDecoder == nil ||
			definition.credentialsDecoder == nil ||
			definition.prepare == nil ||
			definition.privateHostCheck == nil ||
			definition.constructor == nil {
			panic(fmt.Sprintf("connector registry definition incomplete for %s", definition.dataSourceType))
		}
		if _, ok := registry.byType[definition.dataSourceType]; ok {
			panic(fmt.Sprintf("duplicate connector registry definition for %s", definition.dataSourceType))
		}
		registry.byType[definition.dataSourceType] = definition
	}

	return registry
}

// definitionFor returns a registry entry by data source type without exposing
// the mutable registry map to callers.
func (registry connectorDefinitionRegistry) definitionFor(
	dsType model.DataSourceType,
) (connectorDefinition, bool) {
	definition, ok := registry.byType[dsType]
	return definition, ok
}

// allDefinitions returns a copy of reviewed definitions for consistency checks.
func (registry connectorDefinitionRegistry) allDefinitions() []connectorDefinition {
	return append([]connectorDefinition(nil), registry.definitions...)
}
