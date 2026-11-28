package queriessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
)

// ResolveFTPArtifactWrite loads the same scoped configuration, credentials and
// execution limits as a captured query without dispatching a connector.
func (s *service) ResolveFTPArtifactWrite(ctx context.Context, org, source slug.Slug, pin contractplan.OperationContent) (ConnectorExecutionRequest, error) {
	if pin.ResourceScope != nil || len(pin.Parameters) != 0 {
		return ConnectorExecutionRequest{}, deploymentplan.ErrInvalid
	}
	statement, err := ExtractQueryStatement(pin.Data)
	if err != nil {
		return ConnectorExecutionRequest{}, deploymentplan.ErrInvalid.With(err)
	}
	ds, err := s.datasourcesSvc.FindBySlug(ctx, org, source)
	if err != nil || ds == nil || ds.Type != model.DataSourceTypeFTP || !ds.IsActive || ds.ReadOnly ||
		ds.Credential == nil || ds.Credential.Kind != credentialssvc.CredentialKindStaticSecret ||
		ds.Credential.Status != credentialssvc.CredentialStatusActive {
		return ConnectorExecutionRequest{}, deploymentplan.ErrInvalid.With(err)
	}
	digest, err := deploymentplan.DispatchConfig(ctx, org, source)
	if err != nil || digest == "" {
		return ConnectorExecutionRequest{}, deploymentplan.ErrInvalid.With(err)
	}
	config, credentials, configVersion, credentialVersion, reference, err := s.resolveExecutionPayload(ctx, org, source, ds)
	if err != nil {
		return ConnectorExecutionRequest{}, err
	}
	actual, err := connector.PublicConfigDigest(ds.Type, config)
	if err != nil || actual != digest {
		return ConnectorExecutionRequest{}, deploymentplan.ErrInvalid.With(err)
	}
	if err := deploymentplan.Admit(ctx, source, reference); err != nil {
		return ConnectorExecutionRequest{}, err
	}
	prepared := &preparedExecution{statementJSON: statement, rowLimit: parseRowLimit(pin.Data), timeoutSeconds: parseTimeoutSeconds(pin.Data)}
	limits := s.effectiveConnectorLimits(prepared)
	timeout, _ := s.effectiveTimeout(prepared)
	if limits.ConnectorTimeout <= 0 || timeout < limits.ConnectorTimeout {
		limits.ConnectorTimeout = timeout
	}
	return ConnectorExecutionRequest{
		SinkExecution: true, OrganizationSlug: org, DataSourceSlug: source, DataSourceType: ds.Type,
		PinnedConfigDigest: digest, ConfigJSON: config, CredentialsJSON: credentials, StatementJSON: statement,
		ConfigVersion: configVersion, CredentialVersion: credentialVersion, CredentialReference: reference, Limits: limits,
		ConcurrencyLimit: s.limits.ConnectorConcurrencyPerDataSource, ConcurrencyQueueLimit: s.limits.ConnectorConcurrencyMaxQueued,
		ConcurrencyQueueTimeout: s.limits.ConnectorConcurrencyQueueTimeout,
	}, nil
}
