package projects

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (*repository) RevokeChangedProposalAuthority(ctx context.Context, tx postgres.DBTX, p BindProposalAuthorityParams) error {
	err := dbgen.New(tx).RevokeChangedProjectProposalAuthority(ctx, dbgen.RevokeChangedProjectProposalAuthorityParams{
		OrgSlug: p.OrgSlug, ProjectSlug: p.ProjectSlug, DataSourceSlug: p.DataSourceSlug, CredentialSlug: p.CredentialSlug,
		ProviderAccountID: p.Authority.ProviderAccountID, ProviderRepositoryID: p.Authority.ProviderRepositoryID,
		ProviderCredentialID: p.Authority.ProviderCredentialID, ExecutingPrincipalID: p.Authority.ExecutingPrincipalID,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (*repository) BindGitHubProposalAuthority(ctx context.Context, tx postgres.DBTX, p BindProposalAuthorityParams) error {
	rows, err := dbgen.New(tx).BindProjectGitHubProposalAuthority(ctx, dbgen.BindProjectGitHubProposalAuthorityParams{
		OrgSlug: p.OrgSlug, ProjectSlug: p.ProjectSlug, DataSourceSlug: p.DataSourceSlug, CredentialSlug: p.CredentialSlug,
		ProviderAccountID: p.Authority.ProviderAccountID, ProviderRepositoryID: p.Authority.ProviderRepositoryID,
		ProviderCredentialID: p.Authority.ProviderCredentialID, ExecutingPrincipalID: p.Authority.ExecutingPrincipalID,
		ExecutingPrincipalDisplay: p.Authority.ExecutingPrincipalDisplay,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrPipelineBackendDataSourceUnavailable
	}
	return nil
}

func (*repository) BindGitBranchProposalAuthority(ctx context.Context, tx postgres.DBTX, p BindProposalAuthorityParams) error {
	rows, err := dbgen.New(tx).BindProjectGitBranchProposalAuthority(ctx, dbgen.BindProjectGitBranchProposalAuthorityParams{OrgSlug: p.OrgSlug, ProjectSlug: p.ProjectSlug, DataSourceSlug: p.DataSourceSlug})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrPipelineBackendDataSourceUnavailable
	}
	return nil
}
