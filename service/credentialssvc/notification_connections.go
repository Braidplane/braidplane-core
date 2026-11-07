package credentialssvc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
)

func (s *service) CreateNotificationConnection(ctx context.Context, params CreateNotificationConnectionParams) (NotificationConnection, error) {
	params.Name = strings.TrimSpace(params.Name)
	if err := validate.Default().Struct(params); err != nil {
		return NotificationConnection{}, ErrInvalidInput.With(err)
	}
	if _, err := s.provider(params.Provider); err != nil {
		return NotificationConnection{}, err
	}
	provided := params.Slug != ""
	base, err := params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return NotificationConnection{}, ErrInvalidInput.With(err)
	}
	params.Slug = base
	credentialSlug, err := s.nextSlug("notification", params.Slug)
	if err != nil {
		return NotificationConnection{}, err
	}
	create := func() (repocredentials.CredentialRecord, error) {
		return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (repocredentials.CredentialRecord, error) {
			return s.repo.CreateNotificationConnection(ctx, tx, repocredentials.CredentialRecord{
				Slug: credentialSlug, OrgSlug: params.OrgSlug, OwnerKind: repocredentials.OwnerKindNotificationConnection,
				NotificationConnectionSlug: params.Slug,
			}, params.Name, params.Provider)
		})
	}
	record, err := create()
	if !provided && xerror.Is(err, repocredentials.ErrNotificationConnectionSlugTaken) {
		params.Slug, err = slug.WithRandomSuffix(base, 6)
		if err != nil {
			return NotificationConnection{}, err
		}
		record, err = create()
	}
	if err != nil {
		return NotificationConnection{}, err
	}
	return NotificationConnection{Slug: params.Slug, Name: params.Name, Provider: params.Provider, Status: mustCredentialStatus(record.Status), UpdatedAt: record.UpdatedAt}, nil
}

func (s *service) ListNotificationConnections(ctx context.Context, org slug.Slug) ([]NotificationConnection, error) {
	if org == "" {
		return nil, ErrInvalidInput.Detail("organization slug is required")
	}
	rows, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]repocredentials.NotificationConnection, error) {
		return s.repo.ListNotificationConnections(ctx, db, org)
	})
	if err != nil {
		return nil, err
	}
	result := make([]NotificationConnection, 0, len(rows))
	for _, row := range rows {
		var account ProviderAccount
		if err := json.Unmarshal(row.Account, &account); err != nil {
			return nil, ErrInvalidInput.Detail("invalid notification connection account metadata")
		}
		result = append(result, NotificationConnection{Slug: row.Slug, Name: row.Name, Provider: row.Provider, Status: mustCredentialStatus(row.Status), Account: account, UpdatedAt: row.UpdatedAt})
	}
	return result, nil
}

func (s *service) ConnectNotificationConnection(ctx context.Context, owner CredentialOwner, actor model.EventActor) (ProviderConnectorInstall, error) {
	if owner.Kind != CredentialOwnerKindNotificationConnection {
		return ProviderConnectorInstall{}, ErrInvalidInput
	}
	if err := validateOAuthOwner(owner); err != nil {
		return ProviderConnectorInstall{}, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (ProviderConnectorInstall, error) {
		record, err := s.repo.FindNotificationConnectionCredential(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner}))
		if err != nil {
			return ProviderConnectorInstall{}, err
		}
		intent := OAuthInstallIntentRotate
		if record.Status == CredentialStatusPending.String() {
			intent = OAuthInstallIntentCreate
		}
		return s.startOAuthInstall(ctx, tx, StartOAuthInstallParams{Owner: owner, Provider: record.Provider, Intent: intent, Actor: actor})
	})
}
