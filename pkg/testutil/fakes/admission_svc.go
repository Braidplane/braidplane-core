package fakes

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type AdmissionService struct {
	WithPipelineExecutionFn            func(context.Context, slug.Slug, bool, func(context.Context) error) error
	CheckPipelineRunAdmissionFn        func(context.Context, slug.Slug) error
	CheckPipelineDeploymentAdmissionFn func(context.Context, slug.Slug, slug.Slug, slug.Slug) error
	InitializeOrganizationFn           func(context.Context, slug.Slug) error
	DataSourceConnectedFn              func(context.Context, slug.Slug, slug.Slug, time.Time) error
	CheckWriteAccessFn                 func(context.Context, slug.Slug) error
	CheckMemberAdmissionFn             func(context.Context, slug.Slug) error
	CheckProjectAdmissionFn            func(context.Context, slug.Slug) error
	CheckDataSourceAdmissionFn         func(context.Context, slug.Slug) error
	CheckDeploymentAdmissionFn         func(context.Context, slug.Slug) error
	CheckRunAccessFn                   func(context.Context, slug.Slug) error
}

func (f *AdmissionService) InitializeOrganization(ctx context.Context, orgSlug slug.Slug) error {
	if f.InitializeOrganizationFn != nil {
		return f.InitializeOrganizationFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) DataSourceConnected(ctx context.Context, orgSlug, sourceSlug slug.Slug, activatedAt time.Time) error {
	if f.DataSourceConnectedFn != nil {
		return f.DataSourceConnectedFn(ctx, orgSlug, sourceSlug, activatedAt)
	}
	return nil
}

func (f *AdmissionService) CheckWriteAccess(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckWriteAccessFn != nil {
		return f.CheckWriteAccessFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckMemberAdmission(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckMemberAdmissionFn != nil {
		return f.CheckMemberAdmissionFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckProjectAdmission(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckProjectAdmissionFn != nil {
		return f.CheckProjectAdmissionFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckDataSourceAdmission(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckDataSourceAdmissionFn != nil {
		return f.CheckDataSourceAdmissionFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckDeploymentAdmission(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckDeploymentAdmissionFn != nil {
		return f.CheckDeploymentAdmissionFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckRunAccess(ctx context.Context, orgSlug slug.Slug) error {
	if f.CheckRunAccessFn != nil {
		return f.CheckRunAccessFn(ctx, orgSlug)
	}
	return nil
}

func (f *AdmissionService) CheckPipelineRunAdmission(ctx context.Context, org slug.Slug) error {
	if f.CheckPipelineRunAdmissionFn != nil {
		return f.CheckPipelineRunAdmissionFn(ctx, org)
	}
	return nil
}

func (f *AdmissionService) CheckPipelineDeploymentAdmission(ctx context.Context, org, project, pipeline slug.Slug) error {
	if f.CheckPipelineDeploymentAdmissionFn != nil {
		return f.CheckPipelineDeploymentAdmissionFn(ctx, org, project, pipeline)
	}
	return nil
}

func (f *AdmissionService) WithPipelineExecution(ctx context.Context, org slug.Slug, admitted bool, execute func(context.Context) error) error {
	if f.WithPipelineExecutionFn != nil {
		return f.WithPipelineExecutionFn(ctx, org, admitted, execute)
	}
	return execute(ctx)
}
