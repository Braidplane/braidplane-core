package datasources

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// toDataSource converts a database row to a DataSource domain type.
func toDataSource(row dbgen.DataSource) *DataSource {
	var lastTestedAt *time.Time
	if row.LastTestedAt.Valid {
		lastTestedAt = &row.LastTestedAt.Time
	}

	var testError *string
	if row.TestError.Valid {
		testError = &row.TestError.String
	}

	t, err := model.DataSourceTypeString(row.Type)
	if err != nil {
		panic("invalid data source type in database: " + row.Type)
	}
	schemaImportStatus, ok := schemaImportStatusFromDB(row.SchemaImportStatus)
	if !ok {
		panic("invalid data source schema import status in database: " + row.SchemaImportStatus)
	}

	return &DataSource{
		OrganizationGitLabConnection: row.OrganizationGitlabConnectionID.Valid,
		Slug:                         row.Slug,
		Name:                         row.Name,
		Type:                         t,
		Config:                       row.Config,
		IsActive:                     row.IsActive,
		ReadOnly:                     row.ReadOnly,
		SchemaImportStatus:           schemaImportStatus,
		SchemaImportDiagnostic:       schemaImportDiagnosticFromDB(row.SchemaImportErrorCode, row.SchemaImportErrorMessage),
		SchemaImportStartedAt:        timePtrFromPgTimestamptz(row.SchemaImportStartedAt),
		SchemaImportFinishedAt:       timePtrFromPgTimestamptz(row.SchemaImportFinishedAt),
		LastTestedAt:                 lastTestedAt,
		TestError:                    testError,
		ConnectionTestRevision:       row.ConnectionTestRevision,
		Schema:                       nil,
		CreatedAt:                    row.CreatedAt.Time,
		UpdatedAt:                    row.UpdatedAt.Time,
	}
}

// toDataSourceWithCredential adds the payload-free credential metadata selected with a persisted data source.
func toDataSourceWithCredential(row dbgen.DataSource, rawCredentialMetadata []byte) (*DataSource, error) {
	credential := CredentialMetadata{}
	if err := json.Unmarshal(rawCredentialMetadata, &credential); err != nil {
		return nil, err
	}
	dataSource := toDataSource(row)
	dataSource.Credential = &credential
	return dataSource, nil
}

// toCreateDataSourceParams converts CreateParams to SQLC query parameters.
func toCreateDataSourceParams(orgSlug slug.Slug, p *CreateParams) dbgen.CreateDataSourceParams {
	return dbgen.CreateDataSourceParams{
		OrgSlug:        orgSlug,
		Name:           p.Name,
		Slug:           p.Slug,
		CredentialSlug: p.CredentialSlug,
		Type:           p.Type.String(),
		ConfigJson:     p.ConfigJSON,
		ReadOnly:       p.ReadOnly,
	}
}

// toUpdateDataSourceParams converts UpdateParams to SQLC query parameters.
func toUpdateDataSourceParams(
	orgSlug, dsSlug slug.Slug,
	p *UpdateParams,
) dbgen.UpdateDataSourceParams {
	isActive := false
	if p.IsActive != nil {
		isActive = *p.IsActive
	}

	return dbgen.UpdateDataSourceParams{
		OrgSlug:    orgSlug,
		DsSlug:     dsSlug,
		Name:       p.Name,
		ConfigJson: p.ConfigJSON,
		IsActive:   isActive,
		ReadOnly:   p.ReadOnly,
	}
}

// toUpdateDataSourceCredentialsParams converts UpdateCredentialsParams to SQLC query parameters.
func toUpdateDataSourceCredentialsParams(
	orgSlug, dsSlug slug.Slug,
	p *UpdateCredentialsParams,
) dbgen.UpdateDataSourceCredentialsParams {
	return dbgen.UpdateDataSourceCredentialsParams{
		OrgSlug:        orgSlug,
		DsSlug:         dsSlug,
		CredentialSlug: p.CredentialSlug,
	}
}

func toSchemaMetadata(row dbgen.SchemaMetadatum) *SchemaMetadata {
	return &SchemaMetadata{
		Slug:       row.Slug,
		SchemaData: row.SchemaData,
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
	}
}
