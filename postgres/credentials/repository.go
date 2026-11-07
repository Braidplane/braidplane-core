package credentials

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	dbint "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct{}

// New creates the production SQLC-backed credential repository.
func New() Repository {
	return repository{}
}

// CreateStaticSecret stores encrypted static credentials for later data-source attachment.
func (repository) CreateStaticSecret(
	ctx context.Context,
	tx postgres.DBTX,
	record CredentialRecord,
) (CredentialRecord, error) {
	row, err := dbint.New(tx).CreateStaticCredential(ctx, dbint.CreateStaticCredentialParams{
		OrgSlug:          record.OrgSlug,
		CredentialSlug:   record.Slug,
		CredentialType:   pgtype.Text{String: record.CredentialType, Valid: record.CredentialType != ""},
		Status:           record.Status,
		EncryptedPayload: record.EncryptedPayload,
	})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecordFromStatic(row, record), nil
}

func (repository) CreatePipelineWebhookCredential(
	ctx context.Context,
	tx postgres.DBTX,
	owner PipelineWebhookOwner,
	record CredentialRecord,
	rotatedAt time.Time,
) (CredentialRecord, int, error) {
	row, err := dbint.New(tx).CreatePipelineWebhookCredential(ctx, dbint.CreatePipelineWebhookCredentialParams{
		OrgSlug: owner.OrgSlug, ProjectSlug: owner.ProjectSlug, EnvironmentSlug: owner.EnvironmentSlug,
		TriggerSlug: owner.TriggerSlug, CredentialSlug: record.Slug, EncryptedPayload: record.EncryptedPayload,
		RotatedAt: pgtype.Timestamptz{Time: rotatedAt, Valid: true},
	})
	if err != nil {
		return CredentialRecord{}, 0, mapRepositoryError(err)
	}
	record.Kind = row.Kind
	record.CredentialType = row.CredentialType.String
	record.Status = row.Status
	record.CreatedAt = row.CreatedAt.Time
	record.UpdatedAt = row.UpdatedAt.Time
	return record, int(row.KeyVersion), nil
}

func (repository) RevokePipelineWebhookCredentials(
	ctx context.Context,
	tx postgres.DBTX,
	owner PipelineWebhookOwner,
	revokedAt time.Time,
) error {
	_, err := dbint.New(tx).RevokePipelineWebhookCredentials(ctx, dbint.RevokePipelineWebhookCredentialsParams{
		OrgSlug: owner.OrgSlug, ProjectSlug: owner.ProjectSlug, EnvironmentSlug: owner.EnvironmentSlug,
		TriggerSlug: owner.TriggerSlug, RevokedAt: pgtype.Timestamptz{Time: revokedAt, Valid: true},
	})
	return mapRepositoryError(err)
}

func (repository) LockPipelineWebhookCredentialOwner(
	ctx context.Context,
	tx postgres.DBTX,
	owner PipelineWebhookOwner,
) error {
	_, err := dbint.New(tx).LockPipelineWebhookCredentialOwner(ctx, dbint.LockPipelineWebhookCredentialOwnerParams{
		OrgSlug: owner.OrgSlug, ProjectSlug: owner.ProjectSlug, EnvironmentSlug: owner.EnvironmentSlug, TriggerSlug: owner.TriggerSlug,
	})
	return mapRepositoryError(err)
}

func (repository) ResolvePipelineWebhookCredential(
	ctx context.Context,
	tx postgres.DBTX,
	owner PipelineWebhookOwner,
	keyVersion int,
) (CredentialRecord, error) {
	if keyVersion <= 0 || keyVersion > math.MaxInt32 {
		return CredentialRecord{}, ErrCredentialNotFound
	}
	row, err := dbint.New(tx).ResolvePipelineWebhookCredential(ctx, dbint.ResolvePipelineWebhookCredentialParams{
		OrgSlug: owner.OrgSlug, ProjectSlug: owner.ProjectSlug, EnvironmentSlug: owner.EnvironmentSlug,
		TriggerSlug: owner.TriggerSlug, KeyVersion: int32(keyVersion),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return CredentialRecord{}, ErrCredentialNotFound.With(err)
		}
		return CredentialRecord{}, xerror.Unexpected().With(err)
	}
	return toCredentialRecord(row, ResolveRuntimeCredentialParams{OrgSlug: owner.OrgSlug}), nil
}

// CreatePendingOAuthCredential stores a payload-free pending OAuth credential for data-source attachment.
func (repository) CreatePendingOAuthCredential(
	ctx context.Context,
	tx postgres.DBTX,
	record CredentialRecord,
) (CredentialRecord, error) {
	row, err := dbint.New(tx).CreatePendingOAuthCredential(ctx, dbint.CreatePendingOAuthCredentialParams{
		OrgSlug:        record.OrgSlug,
		CredentialSlug: record.Slug,
		Status:         record.Status,
	})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecordFromStatic(row, record), nil
}

func (repository) CreateOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, record CredentialRecord) (CredentialRecord, error) {
	row, err := dbint.New(tx).CreateOrganizationGitLabConnection(ctx, dbint.CreateOrganizationGitLabConnectionParams{OrgSlug: stringToText(record.OrgSlug.String()), CredentialSlug: stringToText(record.Slug.String())})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return CredentialRecord{Slug: row.Slug, OrgSlug: record.OrgSlug, OwnerKind: OwnerKindOrganizationGitLab, Kind: row.Kind, Status: row.Status, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (repository) AttachOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, orgSlug, dataSourceSlug, credentialSlug slug.Slug, repositoryID string) error {
	_, err := dbint.New(tx).AttachOrganizationGitLabConnection(ctx, dbint.AttachOrganizationGitLabConnectionParams{OrgSlug: orgSlug, DataSourceSlug: dataSourceSlug, ExpectedCredentialSlug: credentialSlug, ExpectedRepositoryID: repositoryID})
	return mapRepositoryError(err)
}

func (repository) FindOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (CredentialRecord, error) {
	row, err := dbint.New(tx).FindOrganizationGitLabConnection(ctx, orgSlug)
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecord(row, ResolveRuntimeCredentialParams{OwnerKind: OwnerKindOrganizationGitLab, OrgSlug: orgSlug}), nil
}

// FindStaticSecretByDataSource returns the active static credential attached to a data source.
func (repository) FindStaticSecretByDataSource(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRuntimeCredentialParams,
) (CredentialRecord, error) {
	row, err := dbint.New(tx).FindStaticCredentialByDataSource(ctx, dbint.FindStaticCredentialByDataSourceParams{
		OrgSlug:        params.OrgSlug,
		DataSourceSlug: params.DataSourceSlug,
	})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecord(row, params), nil
}

// FindStaticSecretByEnvironmentSecret returns the active static credential backing an environment secret.
func (repository) FindStaticSecretByEnvironmentSecret(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRuntimeCredentialParams,
) (CredentialRecord, error) {
	row, err := dbint.New(tx).FindStaticCredentialByEnvironmentSecret(ctx, dbint.FindStaticCredentialByEnvironmentSecretParams{
		OrgSlug:              params.OrgSlug,
		EnvironmentSlug:      params.EnvironmentSlug,
		EnvironmentSecretKey: params.EnvironmentSecretKey,
	})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecord(row, params), nil
}

// MarkStaticEnvironmentSecretRotated tombstones the previous active credential after a secret replacement.
func (repository) MarkStaticEnvironmentSecretRotated(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRuntimeCredentialParams,
	replacementSlug slug.Slug,
	rotatedAt time.Time,
) ([]CredentialRecord, error) {
	rows, err := dbint.New(tx).MarkStaticEnvironmentSecretRotated(ctx, dbint.MarkStaticEnvironmentSecretRotatedParams{
		RotatedAt:                 pgtype.Timestamptz{Time: rotatedAt, Valid: true},
		OrgSlug:                   params.OrgSlug,
		EnvironmentSlug:           params.EnvironmentSlug,
		EnvironmentSecretKey:      params.EnvironmentSecretKey,
		ReplacementCredentialSlug: replacementSlug,
	})
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	result := make([]CredentialRecord, len(rows))
	for i, row := range rows {
		result[i] = toCredentialRecord(row, params)
	}
	return result, nil
}

// MarkStaticEnvironmentSecretRevoked tombstones the active credential before a secret metadata row is removed.
func (repository) MarkStaticEnvironmentSecretRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRuntimeCredentialParams,
	revokedAt time.Time,
) (CredentialRecord, error) {
	row, err := dbint.New(tx).MarkStaticEnvironmentSecretRevoked(ctx, dbint.MarkStaticEnvironmentSecretRevokedParams{
		RevokedAt:            pgtype.Timestamptz{Time: revokedAt, Valid: true},
		OrgSlug:              params.OrgSlug,
		EnvironmentSlug:      params.EnvironmentSlug,
		EnvironmentSecretKey: params.EnvironmentSecretKey,
	})
	if err != nil {
		return CredentialRecord{}, mapRepositoryError(err)
	}
	return toCredentialRecord(row, params), nil
}

// CreateOAuthInstall persists a hashed single-use provider callback state.
func (repository) CreateOAuthInstall(
	ctx context.Context,
	tx postgres.DBTX,
	install CredentialInstall,
) (CredentialInstall, error) {
	install.RequestedScopes = append([]string{}, install.RequestedScopes...)
	if install.OwnerKind == OwnerKindNotificationConnection {
		row, err := dbint.New(tx).CreateNotificationConnectionOAuthInstall(ctx, dbint.CreateNotificationConnectionOAuthInstallParams{InstallSlug: install.Slug, Provider: install.Provider, StateHash: install.StateHash, Intent: install.Intent, ActorType: install.ActorType, ActorSlug: slugPtrToText(install.ActorSlug), RequestedScopes: install.RequestedScopes, ExpiresAt: pgtype.Timestamptz{Time: install.ExpiresAt, Valid: true}, OrgSlug: install.OrgSlug, NotificationConnectionSlug: install.NotificationConnectionSlug})
		if err != nil {
			return CredentialInstall{}, mapRepositoryError(err)
		}
		return CredentialInstall{NotificationConnectionSlug: install.NotificationConnectionSlug, ExpectedCredentialSlug: install.ExpectedCredentialSlug, ExpectedCredentialStatus: install.ExpectedCredentialStatus, Slug: row.Slug, OrgSlug: install.OrgSlug, OwnerKind: OwnerKindNotificationConnection, Provider: row.Provider, StateHash: row.StateHash, Intent: row.Intent, ActorType: row.ActorType, ActorSlug: slugPtr(row.ActorSlug), RequestedScopes: row.RequestedScopes, ExpiresAt: row.ExpiresAt.Time, ConsumedAt: timePtr(row.ConsumedAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
	}

	if install.OwnerKind == OwnerKindOrganizationGitLab {
		row, err := dbint.New(tx).CreateOrganizationGitLabOAuthInstall(ctx, dbint.CreateOrganizationGitLabOAuthInstallParams{InstallSlug: install.Slug, Provider: install.Provider, StateHash: install.StateHash, Intent: install.Intent, ActorType: install.ActorType, ActorSlug: slugPtrToText(install.ActorSlug), RequestedScopes: install.RequestedScopes, ExpiresAt: pgtype.Timestamptz{Time: install.ExpiresAt, Valid: true}, OrgSlug: install.OrgSlug, RepositorySetupSlug: stringToText(install.RepositorySetupSlug.String()), RepositorySetupProjectSlug: stringToText(install.RepositorySetupProjectSlug.String())})
		if err != nil {
			return CredentialInstall{}, mapRepositoryError(err)
		}
		return CredentialInstall{RepositorySetupSlug: slug.Slug(row.RepositorySetupSlug.String), RepositorySetupProjectSlug: slug.Slug(row.RepositorySetupProjectSlug.String), Slug: row.Slug, OrgSlug: install.OrgSlug, OwnerKind: OwnerKindOrganizationGitLab, Provider: row.Provider, StateHash: row.StateHash, Intent: row.Intent, ActorType: row.ActorType, ActorSlug: slugPtr(row.ActorSlug), RequestedScopes: row.RequestedScopes, ExpiresAt: row.ExpiresAt.Time, ConsumedAt: timePtr(row.ConsumedAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
	}
	row, err := dbint.New(tx).CreateOAuthInstall(ctx, dbint.CreateOAuthInstallParams{
		OrgSlug:               install.OrgSlug,
		DataSourceSlug:        install.DataSourceSlug,
		InstallSlug:           install.Slug,
		Provider:              install.Provider,
		StateHash:             install.StateHash,
		EncryptedPkceVerifier: install.EncryptedPKCEVerifier,
		Intent:                install.Intent,
		ActorType:             install.ActorType,
		ActorSlug:             slugPtrToText(install.ActorSlug),
		RequestedScopes:       install.RequestedScopes,
		ExpiresAt:             pgtype.Timestamptz{Time: install.ExpiresAt, Valid: true},
	})
	if err != nil {
		return CredentialInstall{}, mapRepositoryError(err)
	}
	return toCredentialInstallFromCreate(row, install.OrgSlug, install.DataSourceSlug), nil
}

// FindOAuthInstallByState returns install metadata for authorization without consuming state.
func (repository) FindOAuthInstallByState(
	ctx context.Context,
	tx postgres.DBTX,
	provider, stateHash string,
	now time.Time,
) (CredentialInstall, error) {
	row, err := dbint.New(tx).FindOAuthInstallByState(ctx, dbint.FindOAuthInstallByStateParams{
		Provider:  provider,
		StateHash: stateHash,
		Now:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
			return CredentialInstall{}, ErrOAuthStateInvalid.With(err)
		}
		return CredentialInstall{}, mapRepositoryError(err)
	}
	install := toCredentialInstallFromFind(row)
	switch row.StateStatus {
	case "consumed":
		return install, ErrOAuthStateConsumed
	case "expired":
		return install, ErrOAuthStateExpired
	}
	return install, nil
}

// ConsumeOAuthInstall marks a matching unexpired install state as consumed.
func (repository) ConsumeOAuthInstall(
	ctx context.Context,
	tx postgres.DBTX,
	provider, stateHash string,
	now time.Time,
) (CredentialInstall, error) {
	row, err := dbint.New(tx).ConsumeOAuthInstall(ctx, dbint.ConsumeOAuthInstallParams{
		Provider:   provider,
		StateHash:  stateHash,
		ConsumedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
			return CredentialInstall{}, ErrOAuthStateInvalid.With(err)
		}
		return CredentialInstall{}, mapRepositoryError(err)
	}
	switch row.StateStatus {
	case "consumed":
		return CredentialInstall{}, ErrOAuthStateConsumed
	case "expired":
		return CredentialInstall{}, ErrOAuthStateExpired
	}
	return toCredentialInstallFromConsume(row), nil
}

// LockOAuthGrantIntent locks and returns the attached credential state for callback intent validation.
func (repository) LockOAuthGrantIntent(
	ctx context.Context,
	tx postgres.DBTX,
	owner ResolveRuntimeCredentialParams,
) (OAuthGrantIntentState, error) {
	if owner.OwnerKind == OwnerKindNotificationConnection && owner.DataSourceSlug == "" {
		row, err := dbint.New(tx).LockNotificationConnectionOAuthGrantIntent(ctx, dbint.LockNotificationConnectionOAuthGrantIntentParams{OrgSlug: owner.OrgSlug, NotificationConnectionSlug: owner.NotificationConnectionSlug})
		if err != nil {
			return OAuthGrantIntentState{}, mapRepositoryError(err)
		}
		return OAuthGrantIntentState{CredentialSlug: row.CredentialSlug, Kind: row.Kind, Status: row.Status, HasGrant: row.HasGrant}, nil
	}

	if owner.OwnerKind == OwnerKindOrganizationGitLab && owner.DataSourceSlug == "" {
		row, err := dbint.New(tx).LockOrganizationGitLabOAuthGrantIntent(ctx, owner.OrgSlug)
		if err != nil {
			return OAuthGrantIntentState{}, mapRepositoryError(err)
		}
		return OAuthGrantIntentState{CredentialSlug: row.CredentialSlug, Kind: row.Kind, Status: row.Status, HasGrant: row.HasGrant}, nil
	}
	if owner.OwnerKind != OwnerKindDataSource || owner.DataSourceSlug == "" {
		return OAuthGrantIntentState{}, ErrCredentialNotFound
	}
	row, err := dbint.New(tx).LockOAuthGrantIntent(ctx, dbint.LockOAuthGrantIntentParams{
		OrgSlug:        owner.OrgSlug,
		DataSourceSlug: owner.DataSourceSlug,
	})
	if err != nil {
		return OAuthGrantIntentState{}, mapRepositoryError(err)
	}
	return OAuthGrantIntentState{
		SourceRevision: row.ConnectionTestRevision,
		CredentialSlug: row.CredentialSlug,
		Kind:           row.Kind,
		Status:         row.Status,
		HasGrant:       row.HasGrant,
	}, nil
}

// CreateOAuthGrant stores OAuth credential metadata, attaches it to the data source, and stores token material.
func (repository) CreateOAuthGrant(
	ctx context.Context,
	tx postgres.DBTX,
	grant CredentialGrant,
	intent string,
) (CredentialGrant, error) {
	grant.Scopes = append([]string{}, grant.Scopes...)
	if grant.OwnerKind == OwnerKindNotificationConnection {
		row, err := dbint.New(tx).CreateNotificationConnectionOAuthGrant(ctx, dbint.CreateNotificationConnectionOAuthGrantParams{OrgSlug: grant.OrgSlug, NotificationConnectionSlug: grant.NotificationConnectionSlug, CredentialSlug: grant.Slug, Status: grant.Status, Intent: stringToText(intent), Provider: grant.Provider, EncryptedTokenSet: grant.EncryptedTokenSet, AccessTokenExpiresAt: timeToTimestamptz(grant.AccessTokenExpires), Scopes: grant.Scopes, Account: grant.Account, LastRefreshedAt: timePtrToTimestamptz(grant.LastRefreshedAt)})
		if err != nil {
			return CredentialGrant{}, mapRepositoryError(err)
		}
		return CredentialGrant{Slug: row.Slug, OrgSlug: grant.OrgSlug, NotificationConnectionSlug: grant.NotificationConnectionSlug, OwnerKind: OwnerKindNotificationConnection, Kind: row.Kind, Provider: row.Provider, Status: row.Status, EncryptedTokenSet: row.EncryptedTokenSet, AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt), Scopes: row.Scopes, Account: row.Account, LastRefreshedAt: timePtr(row.LastRefreshedAt), RevokedAt: timePtr(row.RevokedAt), RotatedAt: timePtr(row.RotatedAt), FailureCode: textValue(row.FailureCode), FailureMessage: textValue(row.FailureMessage), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, GrantUpdatedAt: row.GrantUpdatedAt.Time}, nil
	}

	if grant.OwnerKind == OwnerKindOrganizationGitLab {
		row, err := dbint.New(tx).CreateOrganizationGitLabOAuthGrant(ctx, dbint.CreateOrganizationGitLabOAuthGrantParams{OrgSlug: grant.OrgSlug, CredentialSlug: grant.Slug, Status: grant.Status, Intent: stringToText(intent), Provider: grant.Provider, EncryptedTokenSet: grant.EncryptedTokenSet, AccessTokenExpiresAt: timeToTimestamptz(grant.AccessTokenExpires), Scopes: grant.Scopes, Account: grant.Account, LastRefreshedAt: timePtrToTimestamptz(grant.LastRefreshedAt)})
		if err != nil {
			return CredentialGrant{}, mapRepositoryError(err)
		}
		return CredentialGrant{Slug: row.Slug, OrgSlug: grant.OrgSlug, OwnerKind: OwnerKindOrganizationGitLab, Kind: row.Kind, Provider: row.Provider, Status: row.Status, EncryptedTokenSet: row.EncryptedTokenSet, AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt), Scopes: row.Scopes, Account: row.Account, LastRefreshedAt: timePtr(row.LastRefreshedAt), RevokedAt: timePtr(row.RevokedAt), RotatedAt: timePtr(row.RotatedAt), FailureCode: textValue(row.FailureCode), FailureMessage: textValue(row.FailureMessage), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, GrantUpdatedAt: row.GrantUpdatedAt.Time}, nil
	}
	row, err := dbint.New(tx).CreateOAuthGrant(ctx, dbint.CreateOAuthGrantParams{
		CredentialSlug:       grant.Slug,
		Status:               grant.Status,
		OrgSlug:              grant.OrgSlug,
		DataSourceSlug:       grant.DataSourceSlug,
		Intent:               stringToText(intent),
		Provider:             grant.Provider,
		EncryptedTokenSet:    grant.EncryptedTokenSet,
		AccessTokenExpiresAt: timeToTimestamptz(grant.AccessTokenExpires),
		Scopes:               grant.Scopes,
		Account:              grant.Account,
		LastRefreshedAt:      timePtrToTimestamptz(grant.LastRefreshedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
			return CredentialGrant{}, ErrOAuthIntentConflict.With(err)
		}
		return CredentialGrant{}, mapRepositoryError(err)
	}
	return toCredentialGrantFromCreate(row, grant.OrgSlug, grant.DataSourceSlug), nil
}

// UpsertOAuthGrant stores encrypted OAuth token material for the data source's attached OAuth credential.
func (repository) UpsertOAuthGrant(
	ctx context.Context,
	tx postgres.DBTX,
	grant CredentialGrant,
) (CredentialGrant, error) {
	grant.Scopes = append([]string{}, grant.Scopes...)
	row, err := dbint.New(tx).UpsertOAuthGrant(ctx, dbint.UpsertOAuthGrantParams{
		OwnerKind:                  ownerKindOrDataSource(grant.OwnerKind),
		OrgSlug:                    grant.OrgSlug,
		DataSourceSlug:             grant.DataSourceSlug,
		NotificationConnectionSlug: grant.NotificationConnectionSlug,
		ExpectedCredentialSlug:     grant.Slug,
		ExpectedGrantUpdatedAt:     timeToTimestamptz(grant.GrantUpdatedAt),
		Provider:                   grant.Provider,
		EncryptedTokenSet:          grant.EncryptedTokenSet,
		AccessTokenExpiresAt:       timeToTimestamptz(grant.AccessTokenExpires),
		Scopes:                     grant.Scopes,
		Account:                    grant.Account,
		LastRefreshedAt:            timePtrToTimestamptz(grant.LastRefreshedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
			return CredentialGrant{}, ErrOAuthGrantConflict.With(err)
		}
		return CredentialGrant{}, mapRepositoryError(err)
	}
	result := toCredentialGrantFromUpsert(row, grant.OrgSlug, grant.DataSourceSlug)
	result.OwnerKind = grant.OwnerKind
	result.NotificationConnectionSlug = grant.NotificationConnectionSlug
	return result, nil
}

// FindOAuthGrant returns OAuth credential metadata and encrypted token material attached to a data source.
func (repository) FindOAuthGrant(
	ctx context.Context,
	tx postgres.DBTX,
	params ResolveRuntimeCredentialParams,
) (CredentialGrant, error) {
	if params.OwnerKind == OwnerKindNotificationConnection {
		row, err := dbint.New(tx).FindNotificationConnectionOAuthGrant(ctx, dbint.FindNotificationConnectionOAuthGrantParams{OrgSlug: params.OrgSlug, NotificationConnectionSlug: params.NotificationConnectionSlug})
		if err != nil {
			return CredentialGrant{}, mapRepositoryError(err)
		}
		return CredentialGrant{Slug: row.Slug, OrgSlug: params.OrgSlug, NotificationConnectionSlug: params.NotificationConnectionSlug, OwnerKind: OwnerKindNotificationConnection, Kind: row.Kind, Provider: row.Provider, Status: row.Status, EncryptedTokenSet: row.EncryptedTokenSet, AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt), Scopes: row.Scopes, Account: row.Account, LastRefreshedAt: timePtr(row.LastRefreshedAt), RevokedAt: timePtr(row.RevokedAt), RotatedAt: timePtr(row.RotatedAt), FailureCode: textValue(row.FailureCode), FailureMessage: textValue(row.FailureMessage), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, GrantUpdatedAt: row.GrantUpdatedAt.Time}, nil
	}

	if params.OwnerKind == OwnerKindOrganizationGitLab {
		row, err := dbint.New(tx).FindOrganizationGitLabOAuthGrant(ctx, params.OrgSlug)
		if err != nil {
			return CredentialGrant{}, mapRepositoryError(err)
		}
		return CredentialGrant{Slug: row.Slug, OrgSlug: params.OrgSlug, OwnerKind: OwnerKindOrganizationGitLab, Kind: row.Kind, Provider: row.Provider, Status: row.Status, EncryptedTokenSet: row.EncryptedTokenSet, AccessTokenExpires: timestamptzValue(row.AccessTokenExpiresAt), Scopes: row.Scopes, Account: row.Account, LastRefreshedAt: timePtr(row.LastRefreshedAt), RevokedAt: timePtr(row.RevokedAt), RotatedAt: timePtr(row.RotatedAt), FailureCode: textValue(row.FailureCode), FailureMessage: textValue(row.FailureMessage), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, GrantUpdatedAt: row.GrantUpdatedAt.Time}, nil
	}
	if params.OwnerKind != "" && params.OwnerKind != OwnerKindDataSource {
		return CredentialGrant{}, ErrCredentialNotFound
	}
	row, err := dbint.New(tx).FindOAuthGrantByDataSource(ctx, dbint.FindOAuthGrantByDataSourceParams{
		OrgSlug:        params.OrgSlug,
		DataSourceSlug: params.DataSourceSlug,
	})
	if err != nil {
		return CredentialGrant{}, mapRepositoryError(err)
	}
	grant := toCredentialGrant(row, params.OrgSlug, params.DataSourceSlug)
	if row.OrganizationOwned {
		grant.OwnerKind = OwnerKindOrganizationGitLab
		grant.DataSourceSlug = ""
	}
	return grant, nil
}

// MarkOAuthGrantRevoked records local grant revocation and safe provider diagnostics.
func (repository) MarkOAuthGrantRevoked(
	ctx context.Context,
	tx postgres.DBTX,
	grant CredentialGrant,
) (CredentialGrant, error) {
	row, err := dbint.New(tx).MarkCredentialRevoked(ctx, dbint.MarkCredentialRevokedParams{
		OwnerKind:                  ownerKindOrDataSource(grant.OwnerKind),
		RevokedAt:                  timePtrToTimestamptz(grant.RevokedAt),
		FailureCode:                stringToText(grant.FailureCode),
		FailureMessage:             stringToText(grant.FailureMessage),
		OrgSlug:                    grant.OrgSlug,
		DataSourceSlug:             grant.DataSourceSlug,
		NotificationConnectionSlug: grant.NotificationConnectionSlug,
		ExpectedCredentialSlug:     grant.Slug,
		ExpectedGrantUpdatedAt:     timeToTimestamptz(grant.GrantUpdatedAt),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) || xerror.Is(err, pgx.ErrNoRows) {
			return CredentialGrant{}, ErrOAuthGrantConflict.With(err)
		}
		return CredentialGrant{}, mapRepositoryError(err)
	}
	grant.Status = row.Status
	grant.RevokedAt = timePtr(row.RevokedAt)
	grant.RotatedAt = timePtr(row.RotatedAt)
	grant.FailureCode = textValue(row.FailureCode)
	grant.FailureMessage = textValue(row.FailureMessage)
	grant.UpdatedAt = row.UpdatedAt.Time
	grant.GrantUpdatedAt = row.GrantUpdatedAt.Time
	return grant, nil
}

// SaveHTTPOAuthApp stores the credential-service encrypted app for an existing HTTP source.
func (repository) SaveHTTPOAuthApp(ctx context.Context, tx postgres.DBTX, owner ResolveRuntimeCredentialParams, encrypted []byte) error {
	_, err := dbint.New(tx).SaveHTTPOAuthApp(ctx, dbint.SaveHTTPOAuthAppParams{OrgSlug: owner.OrgSlug, DataSourceSlug: owner.DataSourceSlug, EncryptedConfig: encrypted})
	return mapRepositoryError(err)
}

func (repository) FindHTTPOAuthApp(ctx context.Context, tx postgres.DBTX, owner ResolveRuntimeCredentialParams) ([]byte, error) {
	encrypted, err := dbint.New(tx).FindHTTPOAuthApp(ctx, dbint.FindHTTPOAuthAppParams{OrgSlug: owner.OrgSlug, DataSourceSlug: owner.DataSourceSlug})
	if err != nil {
		return nil, mapRepositoryError(err)
	}
	return encrypted, nil
}

func (repository) ClaimHTTPOAuthRefresh(ctx context.Context, db postgres.DBTX, grant CredentialGrant, claimID string) (bool, error) {
	claimed, err := dbint.New(db).ClaimHTTPOAuthRefresh(ctx, dbint.ClaimHTTPOAuthRefreshParams{OrgSlug: grant.OrgSlug, DataSourceSlug: grant.DataSourceSlug, CredentialSlug: grant.Slug, GrantUpdatedAt: timeToTimestamptz(grant.GrantUpdatedAt), ClaimID: claimID})
	return claimed, mapRepositoryError(err)
}

func (repository) ReleaseHTTPOAuthRefresh(ctx context.Context, db postgres.DBTX, owner ResolveRuntimeCredentialParams, claimID string) error {
	return mapRepositoryError(dbint.New(db).ReleaseHTTPOAuthRefresh(ctx, dbint.ReleaseHTTPOAuthRefreshParams{OrgSlug: owner.OrgSlug, DataSourceSlug: owner.DataSourceSlug, ClaimID: claimID}))
}
