package credentials

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	dbint "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (repository) CreateNotificationConnection(ctx context.Context, db postgres.DBTX, record CredentialRecord, name, provider string) (CredentialRecord, error) {
	row, err := dbint.New(db).CreateNotificationConnection(ctx, dbint.CreateNotificationConnectionParams{
		OrgSlug: stringToText(record.OrgSlug.String()), CredentialSlug: stringToText(record.Slug.String()),
		NotificationConnectionSlug: stringToText(record.NotificationConnectionSlug.String()), Name: stringToText(name), Provider: stringToText(provider),
	})
	if err != nil {
		if postgres.IsUniqueViolation(err) {
			return CredentialRecord{}, ErrNotificationConnectionSlugTaken.With(err)
		}
		return CredentialRecord{}, mapRepositoryError(err)
	}
	record.Slug, record.Kind, record.Status = row.Slug, row.Kind, row.Status
	record.CreatedAt, record.UpdatedAt = row.CreatedAt.Time, row.UpdatedAt.Time
	return record, nil
}

func (repository) FindNotificationConnectionCredential(ctx context.Context, db postgres.DBTX, owner ResolveRuntimeCredentialParams) (NotificationConnectionCredential, error) {
	row, err := dbint.New(db).FindNotificationConnectionCredential(ctx, dbint.FindNotificationConnectionCredentialParams{OrgSlug: owner.OrgSlug, NotificationConnectionSlug: owner.NotificationConnectionSlug})
	if err != nil {
		return NotificationConnectionCredential{}, mapRepositoryError(err)
	}
	return NotificationConnectionCredential{CredentialRecord: CredentialRecord{
		Slug: row.Slug, OrgSlug: owner.OrgSlug, OwnerKind: OwnerKindNotificationConnection,
		NotificationConnectionSlug: owner.NotificationConnectionSlug,
		Kind:                       row.Kind, Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, Provider: row.Provider}, nil
}

func (repository) ListNotificationConnections(ctx context.Context, db postgres.DBTX, org slug.Slug) ([]NotificationConnection, error) {
	rows, err := dbint.New(db).ListNotificationConnections(ctx, org)
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	result := make([]NotificationConnection, 0, len(rows))
	for _, row := range rows {
		result = append(result, NotificationConnection{Slug: row.Slug, Name: row.Name, Provider: row.Provider, Status: row.Status, Account: row.Account, UpdatedAt: row.UpdatedAt.Time})
	}
	return result, nil
}
