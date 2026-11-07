package odcscontracts

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, sql.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	if postgres.IsUniqueViolation(err) {
		return ErrConflict.With(err)
	}
	return xerror.Unexpected().With(err)
}

func contract(row dbgen.OdcsContract) *model.ODCSContract {
	return &model.ODCSContract{
		Slug: row.Slug, Name: row.Name, ODCSID: row.OdcsID, Status: row.Status,
		Timestamps: model.Timestamps{CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time},
	}
}

func revision(row dbgen.OdcsContractRevision) (*model.ODCSRevision, error) {
	var objects []model.ODCSSchemaObject
	if err := json.Unmarshal(row.SchemaObjects, &objects); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &model.ODCSRevision{
		Slug: row.Slug, Digest: row.Digest, ODCSVersion: row.OdcsVersion,
		DocumentVersion: row.DocumentVersion, DeclaredStatus: row.DeclaredStatus, Status: row.Status,
		Document: row.Document, SchemaObjects: objects, CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (r *repository) CreateContract(ctx context.Context, db postgres.DBTX, org, project slug.Slug, input model.NewODCSContract) (*model.ODCSContract, error) {
	row, err := dbgen.New(db).CreateODCSContract(ctx, dbgen.CreateODCSContractParams{
		OrgSlug: org, ProjectSlug: project, ContractSlug: input.Slug, Name: input.Name, OdcsID: input.ODCSID,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return contract(row), nil
}

func (r *repository) GetContract(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug) (*model.ODCSContract, error) {
	row, err := dbgen.New(db).FindODCSContract(ctx, dbgen.FindODCSContractParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity})
	if err != nil {
		return nil, mapError(err)
	}
	return contract(row), nil
}

func (r *repository) LockContract(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug) (*model.ODCSContract, error) {
	row, err := dbgen.New(db).LockODCSContract(ctx, dbgen.LockODCSContractParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity})
	if err != nil {
		return nil, mapError(err)
	}
	return contract(row), nil
}

func (r *repository) UpdateContract(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug, input model.UpdateODCSContract) (*model.ODCSContract, error) {
	row, err := dbgen.New(db).UpdateODCSContract(ctx, dbgen.UpdateODCSContractParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity, Name: input.Name, Status: input.Status})
	if err != nil {
		return nil, mapError(err)
	}
	return contract(row), nil
}

func (r *repository) ListContracts(ctx context.Context, db postgres.DBTX, org, project slug.Slug, req model.ListRequest) ([]model.ODCSContract, error) {
	cursorSlug, timestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(db).ListODCSContracts(ctx, dbgen.ListODCSContractsParams{OrgSlug: org, ProjectSlug: project, CursorSlug: cursorSlug, CursorTimestamp: timestamp, OrderDir: req.Order, LimitCount: int64(req.Limit)})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]model.ODCSContract, 0, len(rows))
	for _, row := range rows {
		items = append(items, *contract(row))
	}
	return items, nil
}

func (r *repository) CreateRevision(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug, input model.ODCSRevision) (*model.ODCSRevision, error) {
	objects, err := json.Marshal(input.SchemaObjects)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	row, err := dbgen.New(db).CreateODCSRevision(ctx, dbgen.CreateODCSRevisionParams{
		OrgSlug: org, ProjectSlug: project, ContractSlug: identity, RevisionSlug: input.Slug,
		Digest: input.Digest, OdcsVersion: input.ODCSVersion, DocumentVersion: input.DocumentVersion,
		DeclaredStatus: input.DeclaredStatus, Document: input.Document, SchemaObjects: objects,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return revision(row)
}

func (r *repository) GetRevision(ctx context.Context, db postgres.DBTX, org, project, identity, revisionSlug slug.Slug) (*model.ODCSRevision, error) {
	row, err := dbgen.New(db).FindODCSRevision(ctx, dbgen.FindODCSRevisionParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity, RevisionSlug: revisionSlug})
	if err != nil {
		return nil, mapError(err)
	}
	return revision(row)
}

func (r *repository) ListRevisions(ctx context.Context, db postgres.DBTX, org, project, identity slug.Slug, req model.ListRequest) ([]model.ODCSRevision, error) {
	cursorSlug, timestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(db).ListODCSRevisions(ctx, dbgen.ListODCSRevisionsParams{OrgSlug: org, ProjectSlug: project, ContractSlug: identity, CursorSlug: cursorSlug, CursorTimestamp: timestamp, OrderDir: req.Order, LimitCount: int64(req.Limit)})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]model.ODCSRevision, 0, len(rows))
	for _, row := range rows {
		item, err := revision(dbgen.OdcsContractRevision{
			Slug: row.Slug, Digest: row.Digest,
			OdcsVersion: row.OdcsVersion, DocumentVersion: row.DocumentVersion, DeclaredStatus: row.DeclaredStatus,
			Status: row.Status, SchemaObjects: row.SchemaObjects, CreatedAt: row.CreatedAt,
		})
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}
