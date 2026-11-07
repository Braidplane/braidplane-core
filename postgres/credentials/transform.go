package credentials

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func toCredentialRecord(row dbgen.Credential, params ResolveRuntimeCredentialParams) CredentialRecord {
	return CredentialRecord{
		Slug:                       row.Slug,
		OrgSlug:                    params.OrgSlug,
		OwnerKind:                  ownerKindOrDataSource(params.OwnerKind),
		DataSourceSlug:             params.DataSourceSlug,
		NotificationConnectionSlug: params.NotificationConnectionSlug,
		EnvironmentSlug:            params.EnvironmentSlug,
		EnvironmentSecretKey:       params.EnvironmentSecretKey,
		Kind:                       row.Kind,
		CredentialType:             textValue(row.CredentialType),
		Status:                     row.Status,
		EncryptedPayload:           append([]byte(nil), row.EncryptedPayload...),
		CreatedAt:                  row.CreatedAt.Time,
		UpdatedAt:                  row.UpdatedAt.Time,
		RotatedAt:                  timePtr(row.RotatedAt),
		RevokedAt:                  timePtr(row.RevokedAt),
		FailureCode:                textValue(row.FailureCode),
		FailureMessage:             textValue(row.FailureMessage),
	}
}

func toCredentialRecordFromStatic(row dbgen.Credential, owner CredentialRecord) CredentialRecord {
	return toCredentialRecord(row, ResolveRuntimeCredentialParams{
		OwnerKind:                  ownerKindOrDataSource(owner.OwnerKind),
		OrgSlug:                    owner.OrgSlug,
		DataSourceSlug:             owner.DataSourceSlug,
		NotificationConnectionSlug: owner.NotificationConnectionSlug,
		EnvironmentSlug:            owner.EnvironmentSlug,
		EnvironmentSecretKey:       owner.EnvironmentSecretKey,
	})
}

// toCredentialInstallFromCreate converts a persisted install and its tenant-scoped owner slugs.
func toCredentialInstallFromCreate(
	row dbgen.CreateOAuthInstallRow,
	orgSlug, dataSourceSlug slug.Slug,
) CredentialInstall {
	return CredentialInstall{
		Slug:                     row.Slug,
		OrgSlug:                  orgSlug,
		OwnerKind:                OwnerKindDataSource,
		DataSourceSlug:           dataSourceSlug,
		Provider:                 row.Provider,
		StateHash:                row.StateHash,
		EncryptedPKCEVerifier:    append([]byte(nil), row.EncryptedPkceVerifier...),
		Intent:                   row.Intent,
		ExpectedCredentialSlug:   row.ExpectedCredentialSlug,
		ExpectedCredentialStatus: row.ExpectedCredentialStatus,
		ExpectedSourceRevision:   int64Ptr(row.ExpectedSourceRevision),
		ActorType:                row.ActorType,
		ActorSlug:                slugPtr(row.ActorSlug),
		RequestedScopes:          append([]string(nil), row.RequestedScopes...),
		ExpiresAt:                row.ExpiresAt.Time,
		ConsumedAt:               timePtr(row.ConsumedAt),
		CreatedAt:                row.CreatedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
}

func toCredentialInstallFromConsume(row dbgen.ConsumeOAuthInstallRow) CredentialInstall {
	install := CredentialInstall{
		RepositorySetupSlug:        slug.Slug(row.RepositorySetupSlug.String),
		RepositorySetupProjectSlug: slug.Slug(row.RepositorySetupProjectSlug.String),
		Slug:                       row.Slug,
		OrgSlug:                    slug.Slug(row.OrgSlug),
		OwnerKind:                  OwnerKindDataSource,
		DataSourceSlug:             slug.Slug(row.DataSourceSlug),
		Provider:                   row.Provider,
		StateHash:                  row.StateHash,
		EncryptedPKCEVerifier:      append([]byte(nil), row.EncryptedPkceVerifier...),
		Intent:                     row.Intent,
		ExpectedCredentialSlug:     slug.Slug(row.ExpectedCredentialSlug),
		ExpectedCredentialStatus:   row.ExpectedCredentialStatus,
		ExpectedSourceRevision:     int64Ptr(row.ExpectedSourceRevision),
		ActorType:                  row.ActorType,
		ActorSlug:                  slugPtr(row.ActorSlug),
		RequestedScopes:            append([]string(nil), row.RequestedScopes...),
		ExpiresAt:                  row.ExpiresAt.Time,
		ConsumedAt:                 timePtr(row.ConsumedAt),
		CreatedAt:                  row.CreatedAt.Time,
		UpdatedAt:                  row.UpdatedAt.Time,
	}
	if row.OrganizationOwned {
		install.OwnerKind = OwnerKindOrganizationGitLab
	}
	if row.NotificationConnectionSlug != "" {
		install.OwnerKind = OwnerKindNotificationConnection
		install.NotificationConnectionSlug = slug.Slug(row.NotificationConnectionSlug)
	}
	return install
}

func toCredentialInstallFromFind(row dbgen.FindOAuthInstallByStateRow) CredentialInstall {
	install := CredentialInstall{
		RepositorySetupSlug:        slug.Slug(row.RepositorySetupSlug.String),
		RepositorySetupProjectSlug: slug.Slug(row.RepositorySetupProjectSlug.String),
		Slug:                       row.Slug,
		OrgSlug:                    row.OrgSlug,
		OwnerKind:                  OwnerKindDataSource,
		DataSourceSlug:             slug.Slug(row.DataSourceSlug),
		Provider:                   row.Provider,
		StateHash:                  row.StateHash,
		EncryptedPKCEVerifier:      append([]byte(nil), row.EncryptedPkceVerifier...),
		Intent:                     row.Intent,
		ExpectedCredentialSlug:     row.ExpectedCredentialSlug,
		ExpectedCredentialStatus:   row.ExpectedCredentialStatus,
		ExpectedSourceRevision:     int64Ptr(row.ExpectedSourceRevision),
		ActorType:                  row.ActorType,
		ActorSlug:                  slugPtr(row.ActorSlug),
		RequestedScopes:            append([]string(nil), row.RequestedScopes...),
		ExpiresAt:                  row.ExpiresAt.Time,
		ConsumedAt:                 timePtr(row.ConsumedAt),
		CreatedAt:                  row.CreatedAt.Time,
		UpdatedAt:                  row.UpdatedAt.Time,
	}
	if row.OrganizationOwned {
		install.OwnerKind = OwnerKindOrganizationGitLab
	}
	if row.NotificationConnectionSlug != "" {
		install.OwnerKind = OwnerKindNotificationConnection
		install.NotificationConnectionSlug = slug.Slug(row.NotificationConnectionSlug)
	}
	return install
}

func toCredentialGrant(
	row dbgen.FindOAuthGrantByDataSourceRow,
	orgSlug, dataSourceSlug slug.Slug,
) CredentialGrant {
	return CredentialGrant{
		Slug:               row.Slug,
		OrgSlug:            orgSlug,
		OwnerKind:          OwnerKindDataSource,
		DataSourceSlug:     dataSourceSlug,
		Kind:               row.Kind,
		Provider:           row.Provider,
		Status:             row.Status,
		EncryptedTokenSet:  append([]byte(nil), row.EncryptedTokenSet...),
		AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt),
		Scopes:             append([]string(nil), row.Scopes...),
		Account:            append(json.RawMessage(nil), row.Account...),
		LastRefreshedAt:    timePtr(row.LastRefreshedAt),
		RevokedAt:          timePtr(row.RevokedAt),
		RotatedAt:          timePtr(row.RotatedAt),
		FailureCode:        textValue(row.FailureCode),
		FailureMessage:     textValue(row.FailureMessage),
		CreatedAt:          row.CreatedAt.Time,
		UpdatedAt:          row.UpdatedAt.Time,
		GrantUpdatedAt:     row.GrantUpdatedAt.Time,
	}
}

func toCredentialGrantFromCreate(
	row dbgen.CreateOAuthGrantRow,
	orgSlug, dataSourceSlug slug.Slug,
) CredentialGrant {
	return CredentialGrant{
		Slug:               row.Slug,
		OrgSlug:            orgSlug,
		OwnerKind:          OwnerKindDataSource,
		DataSourceSlug:     dataSourceSlug,
		Kind:               row.Kind,
		Provider:           row.Provider,
		Status:             row.Status,
		EncryptedTokenSet:  append([]byte(nil), row.EncryptedTokenSet...),
		AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt),
		Scopes:             append([]string(nil), row.Scopes...),
		Account:            append(json.RawMessage(nil), row.Account...),
		LastRefreshedAt:    timePtr(row.LastRefreshedAt),
		RevokedAt:          timePtr(row.RevokedAt),
		RotatedAt:          timePtr(row.RotatedAt),
		FailureCode:        textValue(row.FailureCode),
		FailureMessage:     textValue(row.FailureMessage),
		CreatedAt:          row.CreatedAt.Time,
		UpdatedAt:          row.UpdatedAt.Time,
		GrantUpdatedAt:     row.GrantUpdatedAt.Time,
	}
}

func toCredentialGrantFromUpsert(
	row dbgen.UpsertOAuthGrantRow,
	orgSlug, dataSourceSlug slug.Slug,
) CredentialGrant {
	return CredentialGrant{
		Slug:               row.Slug,
		OrgSlug:            orgSlug,
		OwnerKind:          OwnerKindDataSource,
		DataSourceSlug:     dataSourceSlug,
		Kind:               row.Kind,
		Provider:           row.Provider,
		Status:             row.Status,
		EncryptedTokenSet:  append([]byte(nil), row.EncryptedTokenSet...),
		AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt),
		Scopes:             append([]string(nil), row.Scopes...),
		Account:            append(json.RawMessage(nil), row.Account...),
		LastRefreshedAt:    timePtr(row.LastRefreshedAt),
		RevokedAt:          timePtr(row.RevokedAt),
		RotatedAt:          timePtr(row.RotatedAt),
		FailureCode:        textValue(row.FailureCode),
		FailureMessage:     textValue(row.FailureMessage),
		CreatedAt:          row.CreatedAt.Time,
		UpdatedAt:          row.UpdatedAt.Time,
		GrantUpdatedAt:     row.GrantUpdatedAt.Time,
	}
}

func mapRepositoryError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, ErrOAuthStateConsumed) ||
		xerror.Is(err, ErrOAuthStateExpired) ||
		xerror.Is(err, ErrOAuthStateInvalid) {
		return err
	}
	if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
		return ErrCredentialNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}

func ownerKindOrDataSource(value string) string {
	if value == "" {
		return OwnerKindDataSource
	}
	return value
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func timestamptzValue(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func stringToText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func slugPtrToText(value *slug.Slug) pgtype.Text {
	if value == nil || *value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value.String(), Valid: true}
}

func slugPtr(value pgtype.Text) *slug.Slug {
	if !value.Valid {
		return nil
	}
	parsed := slug.Slug(value.String)
	return &parsed
}

func timeToTimestamptz(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

func timePtrToTimestamptz(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func int64Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}
