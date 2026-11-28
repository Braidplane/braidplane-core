package executionrequests

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (Repository) MarkFTPArtifactSubmitted(ctx context.Context, tx postgres.DBTX, deliveryID uuid.UUID, request Request, projectEnvironmentID int64, submissionToken uuid.UUID) (bool, error) {
	ok, err := dbgen.New(tx).MarkFTPArtifactSubmitted(ctx, dbgen.MarkFTPArtifactSubmittedParams{
		DeliveryID:           pgtype.UUID{Bytes: deliveryID.UUID, Valid: true},
		RequestID:            pgtype.UUID{Bytes: request.ID.UUID, Valid: true},
		OrgSlug:              request.OrganizationSlug,
		SourceSlug:           request.DataSourceSlug,
		ProjectEnvironmentID: projectEnvironmentID,
		SubmissionToken:      pgtype.UUID{Bytes: submissionToken.UUID, Valid: true},
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}

type FTPArtifactPermit struct {
	DeliveryID uuid.UUID
	DispatchAuthority
	ConfigDigest string
	Statement    []byte
	FinalPath    string
	StagingPath  string
	SizeBytes    int64
	SHA256       string
}

func (Repository) SpendFTPArtifactPermit(ctx context.Context, tx postgres.DBTX, permit FTPArtifactPermit) (bool, error) {
	if permit.CredentialSlug == nil || permit.CredentialUpdatedAt == nil || permit.ReadOnly {
		return false, nil
	}
	ok, err := dbgen.New(tx).SpendFTPArtifactPermit(ctx, dbgen.SpendFTPArtifactPermitParams{
		DeliveryID:         pgtype.UUID{Bytes: permit.DeliveryID.UUID, Valid: true},
		RequestID:          pgtype.UUID{Bytes: permit.RequestID.UUID, Valid: true},
		Claim:              pgtype.UUID{Bytes: permit.ClaimToken.UUID, Valid: true},
		OrgSlug:            permit.Organization,
		SourceSlug:         permit.DataSource,
		ExpectedConfig:     permit.Config,
		ConfigDigest:       permit.ConfigDigest,
		CredentialSlug:     *permit.CredentialSlug,
		CredentialRevision: pgtype.Timestamptz{Time: *permit.CredentialUpdatedAt, Valid: true},
		Statement:          permit.Statement,
		Target:             permit.FinalPath,
		Staging:            permit.StagingPath,
		SizeBytes:          permit.SizeBytes,
		Sha256:             permit.SHA256,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}

func (Repository) FTPArtifactSubmissionToken(ctx context.Context, db postgres.DBTX, deliveryID uuid.UUID, request Request, projectEnvironmentID int64) (uuid.UUID, error) {
	token, err := dbgen.New(db).FTPArtifactSubmissionToken(ctx, dbgen.FTPArtifactSubmissionTokenParams{
		DeliveryID:           pgtype.UUID{Bytes: deliveryID.UUID, Valid: true},
		RequestID:            pgtype.UUID{Bytes: request.ID.UUID, Valid: true},
		OrgSlug:              request.OrganizationSlug,
		SourceSlug:           request.DataSourceSlug,
		ProjectEnvironmentID: projectEnvironmentID,
	})
	return uuid.UUID{UUID: token.Bytes}, err
}

func (Repository) CheckFTPArtifactRead(ctx context.Context, db postgres.DBTX, permit FTPArtifactPermit, environmentID, queryID int64, queryRevision time.Time) (bool, error) {
	return dbgen.New(db).CheckFTPArtifactRead(ctx, dbgen.CheckFTPArtifactReadParams{
		RequestID:            pgtype.UUID{Bytes: permit.RequestID.UUID, Valid: true},
		Claim:                pgtype.UUID{Bytes: permit.ClaimToken.UUID, Valid: true},
		ReceiptID:            pgtype.UUID{Bytes: permit.DeliveryID.UUID, Valid: true},
		ProjectEnvironmentID: environmentID,
		QueryID:              queryID,
		QueryUpdatedAt:       pgtype.Timestamptz{Time: queryRevision, Valid: true},
		Statement:            permit.Statement,
		ConfigDigest:         permit.ConfigDigest,
		Target:               permit.FinalPath,
		SizeBytes:            permit.SizeBytes,
		Sha256:               permit.SHA256,
	})
}
