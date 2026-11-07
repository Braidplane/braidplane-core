package odcscontracts

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) FindContractByIdentity(ctx context.Context, db postgres.DBTX, org, project slug.Slug, id string) (*model.ODCSContract, error) {
	row, err := dbgen.New(db).FindODCSContractByIdentity(ctx, dbgen.FindODCSContractByIdentityParams{OrgSlug: org, ProjectSlug: project, OdcsID: id})
	if err != nil {
		return nil, mapError(err)
	}
	return contract(row), nil
}

func (r *repository) FindRevisionByDigest(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug, digest string) (*model.ODCSRevision, error) {
	row, err := dbgen.New(db).FindODCSRevisionByDigest(ctx, dbgen.FindODCSRevisionByDigestParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity, Digest: digest})
	if err != nil {
		return nil, mapError(err)
	}
	return revision(row)
}

func (r *repository) CreateArtifact(ctx context.Context, db postgres.DBTX, org, project slug.Slug, a model.ODCSArtifact) error {
	rows, err := dbgen.New(db).CreateODCSArtifact(ctx, dbgen.CreateODCSArtifactParams{OrgSlug: org, ProjectSlug: project, ContractSlug: a.ContractSlug, RevisionSlug: a.RevisionSlug.String(), SourceRepo: a.SourceRepo, SourceCommit: a.Commit, SourcePath: a.Path, Format: a.Format, ArtifactDigest: a.ArtifactDigest, Original: a.Original})
	if err != nil {
		return mapError(err)
	}
	if rows != 1 {
		return ErrConflict
	}
	return nil
}

func (r *repository) FindArtifact(ctx context.Context, db postgres.DBTX, org, project slug.Slug, a model.ODCSArtifact) (*model.ODCSArtifact, error) {
	row, err := dbgen.New(db).FindODCSArtifact(ctx, dbgen.FindODCSArtifactParams{OrgSlug: org, ProjectSlug: project, SourceRepo: a.SourceRepo, SourceCommit: a.Commit, SourcePath: a.Path})
	if err != nil {
		return nil, mapError(err)
	}
	return &model.ODCSArtifact{ContractSlug: row.ContractSlug, RevisionSlug: slug.Slug(row.RevisionSlug), ArtifactDigest: row.ArtifactDigest, Original: row.Original, Path: row.SourcePath, Format: row.Format, SourceRepo: row.SourceRepo, Commit: row.SourceCommit}, nil
}
