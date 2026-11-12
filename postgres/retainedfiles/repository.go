package retainedfiles

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	internalpg "github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Repository struct{}

func New() Repository                 { return Repository{} }
func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id.UUID, Valid: id != uuid.Nil} }

func (Repository) Begin(ctx context.Context, db postgres.DBTX, claim filestream.Claim, maxBytes int64, expires time.Time) error {
	ok, err := internalpg.New(db).BeginRetainedFile(ctx, internalpg.BeginRetainedFileParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String(), MaxBytes: maxBytes, ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true}})
	return capabilityResult(ok, err)
}

func (Repository) Append(ctx context.Context, db postgres.DBTX, claim filestream.Claim, sequence, plainBytes int32, ciphertext []byte) error {
	ok, err := internalpg.New(db).AppendRetainedFileChunk(ctx, internalpg.AppendRetainedFileChunkParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String(), Sequence: sequence, PlainBytes: plainBytes, Ciphertext: ciphertext})
	return capabilityResult(ok, err)
}

func (Repository) Seal(ctx context.Context, db postgres.DBTX, claim filestream.Claim, sizeBytes int64, digest string) error {
	ok, err := internalpg.New(db).SealRetainedFile(ctx, internalpg.SealRetainedFileParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String(), SizeBytes: sizeBytes, Sha256: digest})
	return capabilityResult(ok, err)
}

func (Repository) Read(ctx context.Context, db postgres.DBTX, claim filestream.Claim, source uuid.UUID) (filestream.Manifest, error) {
	row, err := internalpg.New(db).ReadRetainedFile(ctx, internalpg.ReadRetainedFileParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String(), SourceRequestID: pgUUID(source)})
	if xerror.Is(err, pgx.ErrNoRows) {
		return filestream.Manifest{}, filestream.ErrUnavailable
	}
	if err != nil {
		return filestream.Manifest{}, xerror.Unexpected().With(err)
	}
	return filestream.Manifest{SizeBytes: row.SizeBytes, ChunkCount: row.ChunkCount, SHA256: row.Sha256, ExpiresAt: row.ExpiresAt.Time}, nil
}

func (Repository) ReadChunk(ctx context.Context, db postgres.DBTX, claim filestream.Claim, source uuid.UUID, sequence int32) ([]byte, error) {
	body, err := internalpg.New(db).ReadRetainedFileChunk(ctx, internalpg.ReadRetainedFileChunkParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String(), SourceRequestID: pgUUID(source), Sequence: sequence})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if len(body) == 0 {
		return nil, filestream.ErrUnavailable
	}
	return body, nil
}

func (Repository) Discard(ctx context.Context, db postgres.DBTX, claim filestream.Claim) error {
	ok, err := internalpg.New(db).DiscardRetainedFile(ctx, internalpg.DiscardRetainedFileParams{RequestID: pgUUID(claim.RequestID), ClaimToken: pgUUID(claim.ClaimToken), OrgSlug: claim.Organization.String()})
	return capabilityResult(ok, err)
}

func capabilityResult(ok bool, err error) error {
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if !ok {
		return filestream.ErrUnavailable
	}
	return nil
}

func (Repository) Preflight(ctx context.Context, db postgres.DBTX) error {
	ready, err := internalpg.New(db).RetainedFileCapabilitiesReady(ctx)
	if err != nil {
		return xerror.ExecutionUnavailable().With(err)
	}
	if !ready {
		return filestream.ErrUnavailable.WithPublicDetail("Native-file execution capabilities are unavailable; restart matching roles after applying migrations and grants.")
	}
	return nil
}
