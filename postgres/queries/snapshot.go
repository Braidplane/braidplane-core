package queries

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

// FindSnapshot reads definition and parameters in one MVCC snapshot, so an edit
// cannot mix an old statement with new defaults during pipeline compilation.
func (*repository) FindSnapshot(ctx context.Context, db postgres.DBTX, org, source, operation slug.Slug) (*OperationSnapshot, error) {
	row, err := dbgen.New(db).FindSavedOperationSnapshot(ctx, dbgen.FindSavedOperationSnapshotParams{OrgSlug: org, DataSourceSlug: source, QuerySlug: operation})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrQueryNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	sourceType, err := model.DataSourceTypeString(row.DataSourceType)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("decode operation data source type")
	}
	return &OperationSnapshot{
		DataSourceType:           sourceType,
		Data:                     row.Data,
		ExpectedInputContract:    row.ExpectedInputContract,
		ExpectedInputContractRef: row.ExpectedInputContractRef,
		OutputContract:           row.OutputContract,
		OutputContractRef:        row.OutputContractRef,
		Parameters:               row.Parameters,
		UpdatedAt:                row.UpdatedAt.Time,
	}, nil
}
