package snowflakewif

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

type Key struct {
	ID           int
	Organization slug.Slug
	DataSource   slug.Slug
}

type Binding struct {
	Key
	Issuer  string
	Account string
	Desired bool
	State   string
	EntryID string
}

type Repository struct{}

func (Repository) LockConnection(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) error {
	_, err := dbgen.New(db).LockSnowflakeWIFConnection(ctx, dbgen.LockSnowflakeWIFConnectionParams{OrgSlug: org, DsSlug: ds})
	return mapError(err)
}

func (Repository) RevokeOther(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, issuer, account string, enabled bool) error {
	return mapError(dbgen.New(db).RevokeOtherSnowflakeWIFBindings(ctx, dbgen.RevokeOtherSnowflakeWIFBindingsParams{
		OrgSlug: org, DsSlug: ds, Issuer: issuer, Account: account, Enabled: enabled,
	}))
}

func (Repository) Upsert(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, issuer, account string) error {
	return mapError(dbgen.New(db).UpsertSnowflakeWIFBinding(ctx, dbgen.UpsertSnowflakeWIFBindingParams{
		OrgSlug: org, DsSlug: ds, Issuer: issuer, Account: account,
	}))
}

// Due is a privileged control-plane enumeration; runtime access uses Ready only.
func (Repository) Due(ctx context.Context, db postgres.DBTX) ([]Key, error) {
	rows, err := dbgen.New(db).ListSnowflakeWIFReconciliation(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]Key, len(rows))
	for i, row := range rows {
		result[i] = Key{ID: row.ID, Organization: row.Organization, DataSource: row.DataSource}
	}
	return result, nil
}

func (Repository) Get(ctx context.Context, db postgres.DBTX, key Key) (Binding, error) {
	row, err := dbgen.New(db).GetSnowflakeWIFBindingForReconciliation(ctx, dbgen.GetSnowflakeWIFBindingForReconciliationParams{
		BindingID: key.ID, OrgSlug: key.Organization, DsSlug: key.DataSource,
	})
	if err != nil {
		return Binding{}, mapError(err)
	}
	return Binding{Key: key, Issuer: row.Issuer, Account: row.Account, Desired: row.EffectiveDesired, State: row.State, EntryID: row.EntryID}, nil
}

func (Repository) SetState(ctx context.Context, db postgres.DBTX, binding Binding, state string) error {
	return mapError(dbgen.New(db).SetSnowflakeWIFBindingState(ctx, dbgen.SetSnowflakeWIFBindingStateParams{
		BindingID: binding.ID, OrgSlug: binding.Organization, DsSlug: binding.DataSource,
		Desired: binding.Desired, State: state, EntryID: binding.EntryID,
	}))
}

func (Repository) Delete(ctx context.Context, db postgres.DBTX, key Key) error {
	return mapError(dbgen.New(db).DeleteRevokedSnowflakeWIFBinding(ctx, dbgen.DeleteRevokedSnowflakeWIFBindingParams{
		BindingID: key.ID, OrgSlug: key.Organization, DsSlug: key.DataSource,
	}))
}

func (Repository) Ready(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, issuer, account string) (bool, error) {
	ready, err := dbgen.New(db).SnowflakeWIFReady(ctx, dbgen.SnowflakeWIFReadyParams{
		OrgSlug: org.String(), DsSlug: ds.String(), Issuer: issuer, Account: account,
	})
	return ready, mapError(err)
}

var ErrNotFound = xerror.NotFound(xerror.DataSource).Detail("workload identity binding not found")

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}

// OnboardingState exposes only the exact connection's public lifecycle state.
func (Repository) OnboardingState(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, issuer, account string) (string, error) {
	state, err := dbgen.New(db).GetSnowflakeWIFOnboardingState(ctx, dbgen.GetSnowflakeWIFOnboardingStateParams{OrgSlug: org, DsSlug: ds, Issuer: issuer, Account: account})
	if xerror.Is(err, pgx.ErrNoRows) {
		return "disabled", nil
	}
	return state, mapError(err)
}
