package admissionsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

var ErrAdmissionDenied = xerror.Forbidden(xerror.Organization)

type Service interface {
	WithPipelineExecution(context.Context, slug.Slug, bool, func(context.Context) error) error
	CheckPipelineRunAdmission(ctx context.Context, orgSlug slug.Slug) error
	CheckPipelineDeploymentAdmission(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) error
	InitializeOrganization(ctx context.Context, orgSlug slug.Slug) error
	DataSourceConnected(ctx context.Context, orgSlug, sourceSlug slug.Slug, connectedAt time.Time) error
	CheckWriteAccess(ctx context.Context, orgSlug slug.Slug) error
	CheckMemberAdmission(ctx context.Context, orgSlug slug.Slug) error
	CheckProjectAdmission(ctx context.Context, orgSlug slug.Slug) error
	CheckDataSourceAdmission(ctx context.Context, orgSlug slug.Slug) error
	CheckDeploymentAdmission(ctx context.Context, orgSlug slug.Slug) error
	CheckRunAccess(ctx context.Context, orgSlug slug.Slug) error
}

type service struct {
	txm      postgres.TransactionManager
	orgsRepo organizations.Repository
}

func New(txm postgres.TransactionManager, orgsRepo organizations.Repository) Service {
	if txm == nil || orgsRepo == nil {
		panic("admissionsvc: transaction manager and organization repository are required")
	}
	return &service{txm: txm, orgsRepo: orgsRepo}
}

func (s *service) InitializeOrganization(context.Context, slug.Slug) error { return nil }

func (s *service) DataSourceConnected(context.Context, slug.Slug, slug.Slug, time.Time) error {
	return nil
}

func (s *service) CheckWriteAccess(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) CheckMemberAdmission(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) CheckProjectAdmission(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) CheckDataSourceAdmission(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) CheckDeploymentAdmission(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) CheckRunAccess(ctx context.Context, orgSlug slug.Slug) error {
	return s.check(ctx, orgSlug)
}

func (s *service) check(ctx context.Context, orgSlug slug.Slug) error {
	err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		_, err = s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
		return err
	})
	if err != nil {
		return xerror.Wrapf("check organization admission: %w", err)
	}
	return nil
}

// CheckPipelineRunAdmission verifies the organization before a run is queued.
func (s *service) CheckPipelineRunAdmission(ctx context.Context, orgSlug slug.Slug) error {
	return s.CheckWriteAccess(ctx, orgSlug)
}

func (s *service) CheckPipelineDeploymentAdmission(ctx context.Context, orgSlug, _, _ slug.Slug) error {
	return s.CheckWriteAccess(ctx, orgSlug)
}

func (s *service) WithPipelineExecution(ctx context.Context, orgSlug slug.Slug, _ bool, execute func(context.Context) error) error {
	if err := s.CheckWriteAccess(ctx, orgSlug); err != nil {
		return err
	}
	return execute(ctx)
}
