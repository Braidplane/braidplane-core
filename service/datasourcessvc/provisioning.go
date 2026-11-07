package datasourcessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
)

// SourceProvisioner persists a trusted source inside the core creation transaction.
// HTTP request payloads cannot select this compiled distribution dependency.
type SourceProvisioner interface {
	Provision(context.Context, postgres.DBTX, slug.Slug, *repods.CreateParams) error
}

type sourceCreator interface {
	Create(context.Context, postgres.DBTX, slug.Slug, *repods.CreateParams) (*repods.DataSource, error)
}

func (s *service) Create(ctx context.Context, org slug.Slug, actor model.EventActor, params *CreateParams) (*DataSource, error) {
	return s.create(ctx, org, actor, params, s.repo, true)
}

// CreateProvisioned retains core validation and lifecycle around a required trusted insert.
func (s *service) CreateProvisioned(ctx context.Context, org slug.Slug, actor model.EventActor, params *CreateParams, provisioner SourceProvisioner) (*DataSource, error) {
	if provisioner == nil {
		return nil, ErrInvalidInput.Detail("trusted source provisioner is required")
	}
	return s.create(ctx, org, actor, params, provisionedCreator{provisioner: provisioner, repository: s.repo}, false)
}

type provisionedCreator struct {
	provisioner SourceProvisioner
	repository  repods.Repository
}

func (c provisionedCreator) Create(ctx context.Context, db postgres.DBTX, org slug.Slug, params *repods.CreateParams) (*repods.DataSource, error) {
	if err := c.provisioner.Provision(ctx, db, org, params); err != nil {
		return nil, err
	}
	return c.repository.FindBySlug(ctx, db, org, params.Slug)
}
