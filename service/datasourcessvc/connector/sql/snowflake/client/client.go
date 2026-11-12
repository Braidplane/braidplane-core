package client

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/snowflakedb/gosnowflake/v2"

	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Snowflake verification includes session setup and a warehouse-backed grants query.
const defaultConnectTimeoutSeconds = 30

const (
	maxOpenConnections    = 5
	maxIdleConnections    = 2
	maxSchemaColumns      = 10_000
	connectionMaxIdleTime = 5 * time.Minute
	connectionMaxLifetime = 30 * time.Minute
)

var listColumnsQuery = fmt.Sprintf(`
SELECT table_schema, table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = ?
ORDER BY table_schema, table_name, ordinal_position
LIMIT %d`, maxSchemaColumns+1)

type Config struct {
	WIFObjects     []snowflakewif.ObjectGrant            `json:"wif_objects,omitempty"`
	Account        string                                `json:"account" validate:"required"`
	Warehouse      string                                `json:"warehouse" validate:"required"`
	Database       string                                `json:"database" validate:"required"`
	Schema         string                                `json:"schema" validate:"required"`
	Role           string                                `json:"role,omitempty"`
	ConnectTimeout int                                   `json:"connect_timeout,omitempty"`
	WIFTokenFile   string                                `json:"_wif_token_file,omitempty"`
	WIFTokenSource func(context.Context) (string, error) `json:"-"`
}

type Credentials struct {
	AuthType string `json:"auth_type" validate:"required"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

const (
	AuthTypePassword         = "password"
	AuthTypeWorkloadIdentity = "workload_identity"
)

type Column struct {
	Schema, Table, Name, DataType, IsNullable string
}

type Result struct {
	Rows        []map[string]any
	ColumnNames []string
	RowCount    int
}

// RowScanner consumes rows synchronously under the adapter's resource policy.
type RowScanner func(*stdsql.Rows) (Result, error)

type Client struct {
	db                    *stdsql.DB
	connectTimeoutSeconds int
	schema                string
	wifSetup              *snowflakewif.Setup
}

func New(ctx context.Context, config Config, credentials Credentials) (*Client, error) {
	if strings.TrimSpace(config.Account) == "" ||
		strings.TrimSpace(config.Warehouse) == "" ||
		strings.TrimSpace(config.Database) == "" ||
		strings.TrimSpace(config.Schema) == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail(
			"account, warehouse, database, and schema are required",
		)
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = defaultConnectTimeoutSeconds
	}
	driverConfig, err := newDriverConfig(config, credentials)
	if err != nil {
		return nil, err
	}
	driverConnector := gosnowflake.NewConnector(gosnowflake.SnowflakeDriver{}, *driverConfig)
	if strings.TrimSpace(credentials.AuthType) == AuthTypeWorkloadIdentity && config.WIFTokenSource != nil {
		driverConnector = &workloadConnector{config: *driverConfig, token: config.WIFTokenSource}
	}
	db := stdsql.OpenDB(driverConnector)
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxIdleConnections)
	db.SetConnMaxIdleTime(connectionMaxIdleTime)
	db.SetConnMaxLifetime(connectionMaxLifetime)
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(config.ConnectTimeout)*time.Second)
	defer cancel()
	if err := db.PingContext(timeoutCtx); err != nil {
		_ = db.Close()
		return nil, mapConnectionError(err, credentials.AuthType)
	}
	client := &Client{db: db, connectTimeoutSeconds: config.ConnectTimeout, schema: config.Schema}
	if credentials.AuthType == AuthTypeWorkloadIdentity && config.WIFTokenSource != nil {
		client.wifSetup = &snowflakewif.Setup{Warehouse: config.Warehouse, Database: config.Database, Schema: config.Schema, Role: config.Role, Objects: config.WIFObjects}
	}
	return client, nil
}

func newDriverConfig(config Config, credentials Credentials) (*gosnowflake.Config, error) {
	if err := ValidateCredentials(credentials); err != nil {
		return nil, err
	}
	driverConfig := &gosnowflake.Config{
		Protocol: "https",
		Account:  config.Account, User: credentials.Username, Password: credentials.Password,
		Warehouse: config.Warehouse, Database: config.Database, Schema: config.Schema, Role: config.Role,
	}
	switch strings.TrimSpace(credentials.AuthType) {
	case AuthTypePassword:
		driverConfig.Authenticator = gosnowflake.AuthTypeSnowflake
	case AuthTypeWorkloadIdentity:
		if strings.TrimSpace(config.WIFTokenFile) == "" && config.WIFTokenSource == nil {
			return nil, xerror.Unavailable(xerror.DataSource).Detail(
				"Snowflake workload identity is not configured for this runtime",
			)
		}
		driverConfig.Authenticator = gosnowflake.AuthTypeWorkloadIdentityFederation
		driverConfig.WorkloadIdentityProvider = "OIDC"
		driverConfig.TokenFilePath = config.WIFTokenFile
		if config.WIFTokenSource != nil {
			driverConfig.TokenFilePath = ""
		}
	}
	if err := driverConfig.Validate(); err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).With(err)
	}
	return driverConfig, nil
}

// A new physical session must fetch its own identity; a long-running SQL
// operation can outlive the JWT used to open its first session.
type workloadConnector struct {
	config gosnowflake.Config
	token  func(context.Context) (string, error)
}

func (c *workloadConnector) Connect(ctx context.Context) (driver.Conn, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("Snowflake runtime identity is unavailable")
	}
	config := c.config
	config.Token = token
	config.TokenFilePath = ""
	conn, err := gosnowflake.NewConnector(gosnowflake.SnowflakeDriver{}, config).Connect(ctx)
	if err != nil {
		return nil, mapConnectionError(err, AuthTypeWorkloadIdentity)
	}
	return conn, nil
}

func (c *workloadConnector) Driver() driver.Driver { return gosnowflake.SnowflakeDriver{} }

// ValidateCredentials enforces the persisted Snowflake authentication shape.
func ValidateCredentials(credentials Credentials) error {
	switch strings.TrimSpace(credentials.AuthType) {
	case AuthTypePassword:
		if strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
			return xerror.InvalidCredentials().Detail(
				"Snowflake username and password are required for password authentication",
			)
		}
	case AuthTypeWorkloadIdentity:
		if strings.TrimSpace(credentials.Username) != "" || credentials.Password != "" {
			return xerror.InvalidCredentials().Detail(
				"Snowflake workload identity must not include username or password",
			)
		}
	default:
		return xerror.InvalidCredentials().Detail(
			"Snowflake auth_type must be password or workload_identity",
		)
	}
	return nil
}

func mapStartupError(err error) error {
	return mapProviderError(err, xerror.Unexpected(), "Snowflake connection failed")
}

func mapConnectionError(err error, authType string) error {
	mapped := mapStartupError(err)
	if strings.TrimSpace(authType) == AuthTypeWorkloadIdentity && xerror.Is(mapped, xerror.InvalidCredentials()) {
		detail := "Snowflake rejected workload identity authentication. Check the connection's issuer, subject, audience, and Snowflake authentication policy."
		return xerror.InvalidCredentials().WithPublicDetail(detail).Detail(detail)
	}
	return mapped
}

func mapProviderError(err error, fallback *xerror.Error, safeDetail string) error {
	var localErr *xerror.Error
	if xerror.As(err, &localErr) {
		return err
	}

	var providerErr *gosnowflake.SnowflakeError
	if xerror.As(err, &providerErr) {
		if providerErr.SQLState == "28000" {
			return xerror.InvalidCredentials().Detail("Snowflake credentials were rejected")
		}
		return fallback.Detail(safeDetail)
	}
	return fallback.Detail(safeDetail)
}

func (c *Client) Close() error {
	if err := c.db.Close(); err != nil {
		return mapProviderError(
			err, xerror.Unavailable(xerror.DataSource), "Snowflake connection close failed",
		)
	}
	return nil
}

func (c *Client) TestConnection(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(
		ctx, time.Duration(c.connectTimeoutSeconds)*time.Second,
	)
	defer cancel()
	if c.wifSetup != nil {
		err := c.verifySetup(timeoutCtx)
		if err != nil && timeoutCtx.Err() != nil {
			detail := "Snowflake verification was canceled. Retry Verify."
			if xerror.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
				detail = "Snowflake verification timed out. Retry Verify or increase the connection timeout; this does not establish that grants are missing."
			}
			return xerror.Unavailable(xerror.DataSource).With(timeoutCtx.Err()).Detail(detail).WithPublicDetail(detail)
		}
		return err
	}
	if err := c.db.PingContext(timeoutCtx); err != nil {
		return mapProviderError(
			err, xerror.Unavailable(xerror.DataSource), "Snowflake connection test failed",
		)
	}
	return nil
}

func (c *Client) Execute(
	ctx context.Context, statement string, args []any, query bool, scanRows RowScanner,
) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !query {
		execResult, err := c.db.ExecContext(ctx, statement, args...)
		if err != nil {
			return Result{}, mapProviderError(err, xerror.Unexpected(), "Snowflake query failed")
		}
		rowsAffected, _ := execResult.RowsAffected()
		return Result{Rows: []map[string]any{}, ColumnNames: []string{}, RowCount: int(rowsAffected)}, nil
	}
	if scanRows == nil {
		return Result{}, xerror.Unexpected().Detail("Snowflake row scanner is required")
	}
	rows, err := c.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return Result{}, mapProviderError(err, xerror.Unexpected(), "Snowflake query failed")
	}
	defer func() { _ = rows.Close() }()
	providerResult, err := scanRows(rows)
	if err != nil {
		cancel()
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		for _, limitErr := range []error{xerror.ExecutionRowLimitExceeded(), xerror.ExecutionResponseSizeExceeded()} {
			if xerror.Is(err, limitErr) {
				return Result{}, limitErr
			}
		}
		return Result{}, xerror.Unexpected().Detail("Snowflake result decoding failed")
	}
	return providerResult, nil
}

func (c *Client) ListColumns(ctx context.Context) ([]Column, error) {
	rows, err := c.db.QueryContext(ctx, listColumnsQuery, c.schema)
	if err != nil {
		return nil, mapProviderError(err, xerror.Unexpected(), "Snowflake schema query failed")
	}
	defer func() { _ = rows.Close() }()
	var columns []Column
	for rows.Next() {
		var column Column
		if err := rows.Scan(&column.Schema, &column.Table, &column.Name, &column.DataType, &column.IsNullable); err != nil {
			return nil, mapProviderError(err, xerror.Unexpected(), "Snowflake schema result decoding failed")
		}
		columns = append(columns, column)
		if len(columns) > maxSchemaColumns {
			detail := fmt.Sprintf(
				"Snowflake schema exceeds the %d-column introspection limit",
				maxSchemaColumns,
			)
			return nil, xerror.ExecutionUnavailable().WithPublicDetail(detail).Detail(detail)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapProviderError(err, xerror.Unexpected(), "Snowflake schema result iteration failed")
	}
	return columns, nil
}
