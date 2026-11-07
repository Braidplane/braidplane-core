// Package credentialrekey exposes operator-only ciphertext census and replacement.
package credentialrekey

import (
	"context"

	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Cursor struct {
	Family int32 `json:"family"`
	ID     int64 `json:"id"`
}

type Record struct {
	Cursor
	Tenant, Resource, ResourceAux, Purpose string
	Ciphertext                             []byte
}

type Repository interface {
	List(context.Context, postgres.DBTX, Cursor, int32) ([]Record, error)
	Replace(context.Context, postgres.DBTX, Record, []byte) (bool, error)
}

type repository struct{}

func New() Repository { return repository{} }

func (repository) List(ctx context.Context, db postgres.DBTX, after Cursor, limit int32) ([]Record, error) {
	rows, err := dbgen.New(db).ListCredentialCiphertexts(ctx, dbgen.ListCredentialCiphertextsParams{AfterFamily: after.Family, AfterID: after.ID, BatchSize: limit})
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, Record{Cursor: Cursor{Family: row.Family, ID: row.RecordID}, Tenant: row.Tenant, Resource: row.Resource, ResourceAux: row.ResourceAux, Purpose: row.Purpose, Ciphertext: row.Ciphertext})
	}
	return records, nil
}

func (repository) Replace(ctx context.Context, db postgres.DBTX, record Record, replacement []byte) (bool, error) {
	changed, err := dbgen.New(db).ReplaceCredentialCiphertext(ctx, dbgen.ReplaceCredentialCiphertextParams{
		Family: record.Family, RecordID: record.ID, Tenant: record.Tenant, Resource: record.Resource, ResourceAux: record.ResourceAux,
		Expected: record.Ciphertext, Replacement: replacement,
	})
	return changed == 1, err
}
