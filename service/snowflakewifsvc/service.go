// Package snowflakewifsvc owns desired identity state and privileged reconciliation.
package snowflakewifsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	repowif "github.com/Braidplane/braidplane-core/postgres/snowflakewif"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// SetDesired participates in the owning data source's mutation transaction and returns readiness.
func SetDesired(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, issuer, account string, enabled bool) (bool, error) {
	if enabled {
		if _, err := snowflakewif.NewIdentity(issuer, org, ds, account); err != nil {
			return false, err
		}
	}
	repo := repowif.Repository{}
	if err := repo.LockConnection(ctx, db, org, ds); err != nil {
		return false, xerror.Wrapf("lock workload identity owner: %w", err)
	}
	if err := repo.RevokeOther(ctx, db, org, ds, issuer, account, enabled); err != nil {
		return false, xerror.Wrapf("revoke superseded workload identities: %w", err)
	}
	if !enabled {
		return false, nil
	}
	if err := repo.Upsert(ctx, db, org, ds, issuer, account); err != nil {
		return false, xerror.Wrapf("persist desired workload identity: %w", err)
	}
	return repo.Ready(ctx, db, org, ds, issuer, account)
}

// Registrar is implemented by the isolated SPIRE CLI client and deterministic fakes.
type Registrar interface {
	Ensure(context.Context, string, spiffeid.ID) error
	Revoke(context.Context, string, spiffeid.ID) error
}

type Reconciler struct {
	txm       postgres.TransactionManager
	registrar Registrar
	events    events.Repository
	repo      repowif.Repository
}

func NewReconciler(txm postgres.TransactionManager, registrar Registrar, audit events.Repository) (*Reconciler, error) {
	if txm == nil || registrar == nil || audit == nil {
		return nil, xerror.Unexpected().Detail("workload reconciler dependencies are required")
	}
	return &Reconciler{txm: txm, registrar: registrar, events: audit}, nil
}

// Reconcile bounds each pass and serializes against owner mutations. Retrying
// after a crash uses the persisted registration ID, including before first create.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	keys, err := postgres.InRead(ctx, r.txm, func(ctx context.Context, db postgres.DBTX) ([]repowif.Key, error) { return r.repo.Due(ctx, db) })
	if err != nil {
		return xerror.Wrapf("list workload reconciliation: %w", err)
	}
	var failures []error
	for _, key := range keys {
		if err := r.reconcileOne(ctx, key); err != nil {
			failures = append(failures, err)
		}
	}
	return xerror.Join(failures...)
}

func (r *Reconciler) reconcileOne(ctx context.Context, key repowif.Key) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return r.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		// The same owner-first lock order is used by SetDesired and data source updates.
		if err := r.repo.LockConnection(ctx, db, key.Organization, key.DataSource); err != nil {
			return xerror.Wrapf("lock workload reconciliation owner: %w", err)
		}
		binding, err := r.repo.Get(ctx, db, key)
		if xerror.Is(err, repowif.ErrNotFound) {
			return nil
		}
		if err != nil {
			return xerror.Wrapf("read workload reconciliation state: %w", err)
		}
		identity, identityErr := snowflakewif.NewIdentity(binding.Issuer, key.Organization, key.DataSource, binding.Account)
		state := "failed"
		if !binding.Desired {
			state = "revoking"
		}
		if identityErr == nil {
			if binding.Desired {
				if r.registrar.Ensure(ctx, binding.EntryID, identity.Subject) == nil {
					state = "ready"
				}
			} else if r.registrar.Revoke(ctx, binding.EntryID, identity.Subject) == nil {
				state = "revoked"
			}
		}
		// Provider errors are intentionally reduced to fixed lifecycle states.
		if state != binding.State {
			if err := r.record(ctx, db, binding, state); err != nil {
				return err
			}
		}
		if state == "revoked" {
			return r.repo.Delete(ctx, db, key)
		}
		return r.repo.SetState(ctx, db, binding, state)
	})
}

func (r *Reconciler) record(ctx context.Context, db postgres.DBTX, binding repowif.Binding, state string) error {
	eventSlug, err := slug.WithRandomSuffix("evt-snowflake-wif", 12)
	if err != nil {
		return xerror.Wrapf("create workload audit identity: %w", err)
	}
	metadata, err := json.Marshal(map[string]string{"lifecycle": "snowflake_wif", "state": state})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	_, err = r.events.Create(ctx, db, binding.Organization, &events.CreateParams{
		Slug: eventSlug, EventClass: model.EventClassSecurityAudit, ActorType: model.EventActorTypeSystem,
		EntityType: model.EventEntityTypeDataSource, EntitySlug: &binding.DataSource,
		Action: model.EventActionUpdate, OccurredAt: time.Now(), SchemaVersion: 1, Metadata: metadata,
	})
	if err != nil {
		return xerror.Unexpected().With(err).Detail("record workload lifecycle audit")
	}
	return nil
}

// Resolve exposes only a fresh readiness decision through the executor capability.
func Resolve(ctx context.Context, txm postgres.TransactionManager, issuer string, org, ds slug.Slug, account string) (snowflakewif.Identity, error) {
	identity, err := snowflakewif.NewIdentity(issuer, org, ds, account)
	if err != nil {
		return snowflakewif.Identity{}, err
	}
	ready, err := postgres.InRead(postgres.WithoutTransaction(ctx), txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
		return (repowif.Repository{}).Ready(ctx, db, org, ds, issuer, account)
	})
	if err != nil || !ready {
		return snowflakewif.Identity{}, xerror.Unavailable(xerror.DataSource).With(err).WithPublicDetail("Snowflake workload identity is pending, revoked, or unavailable. Retry after identity reconciliation.").Detail("Snowflake workload identity is not ready")
	}
	return identity, nil
}
