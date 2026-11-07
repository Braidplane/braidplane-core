package testutil

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

var errFakeDataSourceRepoFnNotSet = xerror.Unexpected().Detail(
	"fake data source repo function is not set",
)

// FakeDataSourceRepo is a test double for datasources.Repository
type FakeDataSourceRepo struct {
	CreateFn                                func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, params *repods.CreateParams) (*repods.DataSource, error)
	FindBySlugFn                            func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error)
	IsSlugReservedFn                        func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (bool, error)
	ListByOrgFn                             func(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, req model.ListRequest) ([]*repods.DataSource, error)
	UpdateFn                                func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateParams) (*repods.DataSource, error)
	UpdateCredentialsFn                     func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpdateCredentialsParams) (*repods.DataSource, error)
	MarkTestSuccessFn                       func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.MarkTestSuccessParams) (*repods.DataSource, error)
	MarkTestFailureFn                       func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, expectedRevision int64, errMsg string, schema json.RawMessage) (*repods.DataSource, error)
	DeleteFn                                func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error
	DataSourceHasProjectBackendReferencesFn func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (bool, error)
	LockDataSourceForUpdateFn               func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error
	RestoreFn                               func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error
	SetReadOnlyFn                           func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, readOnly bool) error
}

// NewFakeDataSourceRepo creates a new test double
func NewFakeDataSourceRepo() *FakeDataSourceRepo {
	return &FakeDataSourceRepo{}
}

func (f *FakeDataSourceRepo) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *repods.CreateParams,
) (*repods.DataSource, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, tx, orgSlug, params)
	}
	return nil, errFakeDataSourceRepoFnNotSet
}

func (f *FakeDataSourceRepo) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*repods.DataSource, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil, repods.ErrDataSourceNotFound
}

// IsSlugReserved reports configured active-or-deleted slug reservations for service tests.
func (f *FakeDataSourceRepo) IsSlugReserved(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (bool, error) {
	if f.IsSlugReservedFn != nil {
		return f.IsSlugReservedFn(ctx, tx, orgSlug, dsSlug)
	}
	return false, nil
}

func (f *FakeDataSourceRepo) ListByOrg(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	req model.ListRequest,
) ([]*repods.DataSource, error) {
	if f.ListByOrgFn != nil {
		return f.ListByOrgFn(ctx, tx, orgSlug, req)
	}
	return []*repods.DataSource{}, nil
}

func (f *FakeDataSourceRepo) Update(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *repods.UpdateParams,
) (*repods.DataSource, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, tx, orgSlug, dsSlug, params)
	}
	return nil, errFakeDataSourceRepoFnNotSet
}

func (f *FakeDataSourceRepo) UpdateCredentials(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *repods.UpdateCredentialsParams,
) (*repods.DataSource, error) {
	if f.UpdateCredentialsFn != nil {
		return f.UpdateCredentialsFn(ctx, tx, orgSlug, dsSlug, params)
	}
	return nil, errFakeDataSourceRepoFnNotSet
}

func (f *FakeDataSourceRepo) MarkTestSuccess(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *repods.MarkTestSuccessParams,
) (*repods.DataSource, error) {
	if f.MarkTestSuccessFn != nil {
		return f.MarkTestSuccessFn(ctx, tx, orgSlug, dsSlug, params)
	}
	return &repods.DataSource{Slug: dsSlug}, nil
}

func (f *FakeDataSourceRepo) MarkTestFailure(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	expectedRevision int64,
	errMsg string,
	schema json.RawMessage,
) (*repods.DataSource, error) {
	if f.MarkTestFailureFn != nil {
		return f.MarkTestFailureFn(ctx, tx, orgSlug, dsSlug, expectedRevision, errMsg, schema)
	}
	return &repods.DataSource{Slug: dsSlug}, nil
}

func (f *FakeDataSourceRepo) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil
}

func (f *FakeDataSourceRepo) DataSourceHasProjectBackendReferences(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (bool, error) {
	if f.DataSourceHasProjectBackendReferencesFn != nil {
		return f.DataSourceHasProjectBackendReferencesFn(ctx, tx, orgSlug, dsSlug)
	}
	return false, nil
}

func (f *FakeDataSourceRepo) LockDataSourceForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.LockDataSourceForUpdateFn != nil {
		return f.LockDataSourceForUpdateFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil
}

func (f *FakeDataSourceRepo) Restore(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil
}

func (f *FakeDataSourceRepo) SetReadOnly(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	readOnly bool,
) error {
	if f.SetReadOnlyFn != nil {
		return f.SetReadOnlyFn(ctx, tx, orgSlug, dsSlug, readOnly)
	}
	return nil
}

// FakeSchemaMetadataRepo is a test double for datasources.SchemaMetadataRepository.
type FakeSchemaMetadataRepo struct {
	FindByDataSourceSlugFn   func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.SchemaMetadata, error)
	DeleteByDataSourceSlugFn func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) error
	UpsertByDataSourceSlugFn func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.UpsertSchemaMetadataParams) (*repods.SchemaMetadata, error)
	FindImportStateBySlugFn  func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.SchemaImportState, error)
	StartImportBySlugFn      func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, attemptID string, startedAt time.Time) (*repods.SchemaImportState, error)
	FindCurrentAttemptFn     func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, attemptID string) (*repods.SchemaImportState, error)
	MarkImportReadyBySlugFn  func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, attemptID string, finishedAt time.Time) (*repods.SchemaImportState, error)
	MarkImportFailedBySlugFn func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug, params *repods.MarkSchemaImportFailedParams) (*repods.SchemaImportState, error)
	ResetImportBySlugFn      func(ctx context.Context, tx postgres.DBTX, orgSlug, dsSlug slug.Slug) (*repods.SchemaImportState, error)
}

// NewFakeSchemaMetadataRepo creates a new test double.
func NewFakeSchemaMetadataRepo() *FakeSchemaMetadataRepo {
	return &FakeSchemaMetadataRepo{}
}

func (f *FakeSchemaMetadataRepo) FindByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*repods.SchemaMetadata, error) {
	if f.FindByDataSourceSlugFn != nil {
		return f.FindByDataSourceSlugFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil, repods.ErrSchemaMetadataNotFound
}

func (f *FakeSchemaMetadataRepo) DeleteByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.DeleteByDataSourceSlugFn != nil {
		return f.DeleteByDataSourceSlugFn(ctx, tx, orgSlug, dsSlug)
	}
	return nil
}

func (f *FakeSchemaMetadataRepo) UpsertByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *repods.UpsertSchemaMetadataParams,
) (*repods.SchemaMetadata, error) {
	if f.UpsertByDataSourceSlugFn != nil {
		return f.UpsertByDataSourceSlugFn(ctx, tx, orgSlug, dsSlug, params)
	}
	return &repods.SchemaMetadata{}, nil
}

func (f *FakeSchemaMetadataRepo) FindImportStateByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*repods.SchemaImportState, error) {
	if f.FindImportStateBySlugFn != nil {
		return f.FindImportStateBySlugFn(ctx, tx, orgSlug, dsSlug)
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusNotImported,
	}, nil
}

func (f *FakeSchemaMetadataRepo) StartImportByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	startedAt time.Time,
) (*repods.SchemaImportState, error) {
	if f.StartImportBySlugFn != nil {
		return f.StartImportBySlugFn(ctx, tx, orgSlug, dsSlug, attemptID, startedAt)
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusImporting,
		StartedAt:      &startedAt,
		Started:        true,
	}, nil
}

func (f *FakeSchemaMetadataRepo) FindCurrentImportAttemptByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
) (*repods.SchemaImportState, error) {
	if f.FindCurrentAttemptFn != nil {
		return f.FindCurrentAttemptFn(ctx, tx, orgSlug, dsSlug, attemptID)
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusImporting,
	}, nil
}

func (f *FakeSchemaMetadataRepo) MarkImportReadyByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	finishedAt time.Time,
) (*repods.SchemaImportState, error) {
	if f.MarkImportReadyBySlugFn != nil {
		return f.MarkImportReadyBySlugFn(ctx, tx, orgSlug, dsSlug, attemptID, finishedAt)
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusReady,
		FinishedAt:     &finishedAt,
	}, nil
}

func (f *FakeSchemaMetadataRepo) MarkImportFailedByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	params *repods.MarkSchemaImportFailedParams,
) (*repods.SchemaImportState, error) {
	if f.MarkImportFailedBySlugFn != nil {
		return f.MarkImportFailedBySlugFn(ctx, tx, orgSlug, dsSlug, params)
	}
	finishedAt := time.Time{}
	var diagnostic *repods.SchemaImportDiagnostic
	if params != nil {
		finishedAt = params.FinishedAt
		diagnostic = &repods.SchemaImportDiagnostic{
			Code:    params.ErrorCode,
			Message: params.ErrorMessage,
		}
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusFailed,
		Diagnostic:     diagnostic,
		FinishedAt:     &finishedAt,
	}, nil
}

func (f *FakeSchemaMetadataRepo) ResetImportByDataSourceSlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*repods.SchemaImportState, error) {
	if f.ResetImportBySlugFn != nil {
		return f.ResetImportBySlugFn(ctx, tx, orgSlug, dsSlug)
	}
	return &repods.SchemaImportState{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusNotImported,
	}, nil
}

// FakeEncryptor is a test double for AES encryption
type FakeEncryptor struct {
	EncryptFn func(plaintext []byte) ([]byte, error)
	DecryptFn func(ciphertext []byte) ([]byte, error)
}

// NewFakeEncryptor creates a new test double
func NewFakeEncryptor() *FakeEncryptor {
	return &FakeEncryptor{}
}

func (f *FakeEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	if f.EncryptFn != nil {
		return f.EncryptFn(plaintext)
	}
	return plaintext, nil // By default, return plaintext as-is (not real encryption)
}

func (f *FakeEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	if f.DecryptFn != nil {
		return f.DecryptFn(ciphertext)
	}
	return ciphertext, nil // By default, return ciphertext as-is
}

func (f *FakeEncryptor) Seal(_ crypto.CredentialScope, plaintext []byte) ([]byte, error) {
	return f.Encrypt(plaintext)
}

func (f *FakeEncryptor) Open(_ crypto.CredentialScope, ciphertext []byte) ([]byte, error) {
	return f.Decrypt(ciphertext)
}

// FakeSQLConnector is a test double for sql.SQLConnector
type FakeSQLConnector struct {
	TestConnectionFn    func(ctx context.Context) error
	CloseFn             func() error
	ExecuteFn           func(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam) (*result.ExecutionResult, error)
	ExecuteWithLimitsFn func(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam, limits types.ExecutionLimits) (*result.ExecutionResult, error)
	IntrospectSchemaFn  func(ctx context.Context) (*sql.Schema, error)
	CapabilitiesFn      func() sql.Capabilities
}

// NewFakeSQLConnector creates a new test double for SQL connectors
func NewFakeSQLConnector() *FakeSQLConnector {
	return &FakeSQLConnector{}
}

func (f *FakeSQLConnector) TestConnection(ctx context.Context) error {
	if f.TestConnectionFn != nil {
		return f.TestConnectionFn(ctx)
	}
	return nil // By default, connection succeeds
}

func (f *FakeSQLConnector) Close() error {
	if f.CloseFn != nil {
		return f.CloseFn()
	}
	return nil
}

func (f *FakeSQLConnector) GetCapabilities() sql.Capabilities {
	if f.CapabilitiesFn != nil {
		return f.CapabilitiesFn()
	}
	// By default, support all capabilities
	return sql.Capabilities{
		SupportsSchemaIntrospection: true,
		SupportsPagination:          true,
		SupportsTransactions:        true,
	}
}

func (f *FakeSQLConnector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	if f.ExecuteWithLimitsFn != nil {
		return f.ExecuteWithLimitsFn(ctx, queryData, params, types.ResolveExecutionLimits(limits...))
	}
	if f.ExecuteFn != nil {
		return f.ExecuteFn(ctx, queryData, params)
	}
	// Return empty SQL result by default
	emptyResult := &result.SQLResult{
		Rows:        []map[string]any{},
		ColumnNames: []string{},
		RowCount:    0,
	}
	return result.NewSQLResult("fake", emptyResult, 0, nil), nil
}

func (f *FakeSQLConnector) IntrospectSchema(ctx context.Context) (*sql.Schema, error) {
	if f.IntrospectSchemaFn != nil {
		return f.IntrospectSchemaFn(ctx)
	}
	return &sql.Schema{Tables: []sql.Table{}}, nil
}

// FakeConnectorFactory is a test double that provides a NewConnectorFunc for testing.
type FakeConnectorFactory struct {
	NewConnectorFn connector.NewConnectorFunc
}

// NewFakeConnectorFactory creates a new test double with a default no-op connector factory
func NewFakeConnectorFactory() *FakeConnectorFactory {
	return &FakeConnectorFactory{
		NewConnectorFn: func(ctx context.Context, dsType model.DataSourceType, configJSON, credentialsJSON []byte) (connector.Connector, error) {
			// By default, return a fake connector that succeeds
			return NewFakeSQLConnector(), nil
		},
	}
}
