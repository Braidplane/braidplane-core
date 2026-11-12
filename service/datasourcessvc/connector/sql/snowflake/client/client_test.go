package client

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/snowflakedb/gosnowflake/v2"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestCustomerIdentityRenewsForDriverSessionsAndSupportsOperations(t *testing.T) {
	issued, logins := 0, 0
	var currentToken string
	transport := snowflakeRoundTripper(func(request *http.Request) (*http.Response, error) {
		response := `{"success":true,"data":{}}`
		switch request.URL.Path {
		case "/session/v1/login-request":
			var body struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			require.Equal(t, "WORKLOAD_IDENTITY", body.Data["AUTHENTICATOR"])
			require.Equal(t, "OIDC", body.Data["PROVIDER"])
			require.Equal(t, currentToken, body.Data["TOKEN"])
			require.NotContains(t, body.Data, "PASSWORD")
			require.NotContains(t, body.Data, "LOGIN_NAME")
			logins++
			response = `{"success":true,"data":{"token":"test-session","masterToken":"test-master","validityInSeconds":3600,"masterValidityInSeconds":3600,"sessionInfo":{"databaseName":"DB","schemaName":"PUBLIC","warehouseName":"WH","roleName":"ROLE"}}}`
		case "/queries/v1/query-request":
			var body struct {
				SQL string `json:"sqlText"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
			if strings.Contains(body.SQL, "information_schema") {
				response = `{"success":true,"data":{"rowtype":[{"name":"TABLE_SCHEMA","type":"text"},{"name":"TABLE_NAME","type":"text"},{"name":"COLUMN_NAME","type":"text"},{"name":"DATA_TYPE","type":"text"},{"name":"IS_NULLABLE","type":"text"}],"rowset":[["PUBLIC","ORDERS","ID","NUMBER","NO"]],"total":1}}`
			} else {
				response = `{"success":true,"data":{"rowtype":[{"name":"ID","type":"fixed","scale":0,"precision":38}],"rowset":[["1"]],"total":1}}`
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})
	source := func(context.Context) (string, error) {
		issued++
		// The SPIRE boundary verifies signatures; this driver test verifies the handoff.
		claims := fmt.Sprintf(`{"iss":"https://issuer.test","sub":"spiffe://issuer.test/customer","nonce":%d}`, issued)
		currentToken = "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".eA"
		return currentToken, nil
	}
	config, err := newDriverConfig(Config{
		Account: "test-account", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
		WIFTokenSource: source, WIFTokenFile: "/must-not-use-fixture-token",
	}, Credentials{AuthType: AuthTypeWorkloadIdentity})
	require.NoError(t, err)
	require.Empty(t, config.TokenFilePath)
	config.Transporter = transport
	db := stdsql.OpenDB(&workloadConnector{config: *config, token: source})
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	client := &Client{db: db, schema: "PUBLIC", connectTimeoutSeconds: 5}
	require.NoError(t, client.TestConnection(t.Context()))
	rows, err := client.Execute(t.Context(), "SELECT 1", nil, true, func(rows *stdsql.Rows) (Result, error) {
		var result Result
		for rows.Next() {
			result.RowCount++
		}
		return result, rows.Err()
	})
	require.NoError(t, err)
	require.Equal(t, 1, rows.RowCount)
	_, err = client.Execute(t.Context(), "INSERT INTO ORDERS SELECT 1", nil, false, nil)
	require.NoError(t, err)
	columns, err := client.ListColumns(t.Context())
	require.NoError(t, err)
	require.Equal(t, []Column{{Schema: "PUBLIC", Table: "ORDERS", Name: "ID", DataType: "NUMBER", IsNullable: "NO"}}, columns)
	require.GreaterOrEqual(t, issued, 4)
	require.Equal(t, issued, logins)
	denied := &workloadConnector{config: *config, token: func(context.Context) (string, error) {
		return "", xerror.Unavailable(xerror.DataSource).Detail("identity revoked")
	}}
	_, err = denied.Connect(t.Context())
	require.ErrorContains(t, err, "identity revoked")
	require.Equal(t, issued, logins, "revoked identities must not reach Snowflake")
}

type snowflakeRoundTripper func(*http.Request) (*http.Response, error)

func (f snowflakeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestMapStartupErrorRedactsProviderAuthenticationFailure(t *testing.T) {
	t.Parallel()

	err := mapStartupError(&gosnowflake.SnowflakeError{
		SQLState: "28000",
		Message:  "provider detail containing account information",
	})

	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.NotContains(t, err.Error(), "account information")
}

func TestWorkloadIdentityRejectionExplainsTrustWithoutProviderDetails(t *testing.T) {
	err := mapConnectionError(&gosnowflake.SnowflakeError{SQLState: "28000", Message: "token-secret-canary"}, AuthTypeWorkloadIdentity)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.Contains(t, xerror.PublicDetail(err), "issuer, subject, audience")
	require.Contains(t, xerror.PublicDetail(err), "authentication policy")
	require.NotContains(t, err.Error(), "token-secret-canary")
}

func TestMapProviderErrorRedactsSnowflakeDetails(t *testing.T) {
	t.Parallel()

	err := mapProviderError(&gosnowflake.SnowflakeError{
		SQLState: "42000",
		Message:  "provider detail containing account and session information",
	}, xerror.Unexpected(), "Snowflake query failed")

	require.ErrorIs(t, err, xerror.Unexpected())
	require.EqualError(t, err, "Snowflake query failed")
	require.NotContains(t, err.Error(), "account")
	require.NotContains(t, err.Error(), "session")
}

func TestMapProviderErrorPreservesLocalXError(t *testing.T) {
	t.Parallel()

	cause := xerror.InvalidInput(xerror.Query).Detail("local database/sql failure")
	err := mapProviderError(cause, xerror.Unexpected(), "Snowflake query failed")

	require.Same(t, cause, err)
}

func TestMapProviderErrorRedactsNonProviderCause(t *testing.T) {
	t.Parallel()

	err := mapProviderError(
		rawProviderError("dial acct-123.snowflakecomputing.com: provider session detail"),
		xerror.Unexpected(),
		"Snowflake query failed",
	)

	require.ErrorIs(t, err, xerror.Unexpected())
	require.EqualError(t, err, "Snowflake query failed")
	require.NotContains(t, err.Error(), "snowflakecomputing.com")
	require.NotContains(t, err.Error(), "session detail")
}

func TestNewDriverConfigUsesPasswordAuthentication(t *testing.T) {
	t.Parallel()

	cfg, err := newDriverConfig(Config{
		Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
	}, Credentials{AuthType: AuthTypePassword, Username: "svc", Password: "secret"})

	require.NoError(t, err)
	require.Equal(t, gosnowflake.AuthTypeSnowflake, cfg.Authenticator)
	require.Equal(t, "svc", cfg.User)
	require.Equal(t, "secret", cfg.Password)
	require.Empty(t, cfg.TokenFilePath)
}

func TestNewDriverConfigUsesDeploymentOIDCWorkloadIdentity(t *testing.T) {
	t.Parallel()

	cfg, err := newDriverConfig(Config{
		Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
		WIFTokenFile: "/var/run/braidplane/snowflake-identity.jwt",
	}, Credentials{AuthType: AuthTypeWorkloadIdentity})

	require.NoError(t, err)
	require.Equal(t, gosnowflake.AuthTypeWorkloadIdentityFederation, cfg.Authenticator)
	require.Equal(t, "OIDC", cfg.WorkloadIdentityProvider)
	require.Equal(t, "/var/run/braidplane/snowflake-identity.jwt", cfg.TokenFilePath)
	require.Empty(t, cfg.User)
	require.Empty(t, cfg.Password)
}

func TestNewDriverConfigRejectsUnavailableWorkloadIdentity(t *testing.T) {
	t.Parallel()

	_, err := newDriverConfig(Config{
		Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
	}, Credentials{AuthType: AuthTypeWorkloadIdentity})

	require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
	require.NotContains(t, err.Error(), "token")
}

func TestValidateCredentialsRejectsStaticSecretsForWorkloadIdentity(t *testing.T) {
	t.Parallel()

	err := ValidateCredentials(Credentials{
		AuthType: AuthTypeWorkloadIdentity, Username: "must-not-persist", Password: "must-not-persist",
	})

	require.ErrorIs(t, err, xerror.InvalidCredentials())
	require.NotContains(t, err.Error(), "must-not-persist")
}

func TestListColumnsScopesIntrospection(t *testing.T) {
	t.Parallel()

	recorder := &queryRecorder{}
	db := stdsql.OpenDB(queryConnector{recorder: recorder})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	client := &Client{db: db, schema: "ANALYTICS"}

	columns, err := client.ListColumns(context.Background())

	require.NoError(t, err)
	require.Equal(t, []Column{{
		Schema: "ANALYTICS", Table: "ORDERS", Name: "ID", DataType: "NUMBER", IsNullable: "NO",
	}}, columns)
	require.Equal(t, listColumnsQuery, recorder.query)
	require.Equal(t, []driver.NamedValue{
		{Ordinal: 1, Value: "ANALYTICS"},
	}, recorder.args)
}

func TestListColumnsRejectsTruncatedIntrospection(t *testing.T) {
	t.Parallel()

	recorder := &queryRecorder{rowCount: maxSchemaColumns + 1}
	db := stdsql.OpenDB(queryConnector{recorder: recorder})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	client := &Client{db: db, schema: "ANALYTICS"}

	columns, err := client.ListColumns(context.Background())

	require.Nil(t, columns)
	require.ErrorIs(t, err, xerror.ExecutionUnavailable())
	require.ErrorContains(t, err, "Snowflake schema exceeds the 10000-column introspection limit")
	require.Equal(t, "Snowflake schema exceeds the 10000-column introspection limit", xerror.PublicDetail(err))
}

type queryRecorder struct {
	query           string
	args            []driver.NamedValue
	rowCount        int
	closed          bool
	ctx             context.Context
	canceledOnClose bool
	iterationErr    error
	closeErr        error
}

type queryConnector struct {
	recorder *queryRecorder
}

func (c queryConnector) Connect(context.Context) (driver.Conn, error) {
	return &queryConnection{recorder: c.recorder}, nil
}

func (queryConnector) Driver() driver.Driver { return queryDriver{} }

type queryDriver struct{}

func (queryDriver) Open(string) (driver.Conn, error) {
	return nil, rawProviderError("queryDriver.Open must not be called")
}

type queryConnection struct {
	recorder *queryRecorder
}

func (*queryConnection) Prepare(string) (driver.Stmt, error) {
	return nil, rawProviderError("queryConnection.Prepare must not be called")
}

func (*queryConnection) Close() error { return nil }

func (*queryConnection) Begin() (driver.Tx, error) {
	return nil, rawProviderError("queryConnection.Begin must not be called")
}

func (c *queryConnection) QueryContext(
	ctx context.Context, query string, args []driver.NamedValue,
) (driver.Rows, error) {
	c.recorder.ctx = ctx
	c.recorder.query = query
	c.recorder.args = append([]driver.NamedValue(nil), args...)
	rowCount := c.recorder.rowCount
	if rowCount == 0 {
		rowCount = 1
	}
	return &queryRows{remaining: rowCount, recorder: c.recorder}, nil
}

type queryRows struct {
	remaining int
	recorder  *queryRecorder
}

func (*queryRows) Columns() []string {
	return []string{"table_schema", "table_name", "column_name", "data_type", "is_nullable"}
}

func (r *queryRows) Close() error {
	r.recorder.closed = true
	r.recorder.canceledOnClose = r.recorder.ctx.Err() != nil
	return r.recorder.closeErr
}

func (r *queryRows) Next(values []driver.Value) error {
	if r.remaining == 0 {
		if r.recorder.iterationErr != nil {
			return r.recorder.iterationErr
		}
		return io.EOF
	}
	r.remaining--
	copy(values, []driver.Value{"ANALYTICS", "ORDERS", "ID", "NUMBER", "NO"})
	return nil
}

var _ driver.QueryerContext = (*queryConnection)(nil)

type rawProviderError string

func (e rawProviderError) Error() string { return string(e) }

func TestDriverDefaultsRequireHTTPSAndCertificateChecks(t *testing.T) {
	t.Parallel()
	cfg, err := newDriverConfig(Config{Account: "test-account", Warehouse: "WH", Database: "DB", Schema: "PUBLIC"}, Credentials{AuthType: AuthTypePassword, Username: "synthetic-user", Password: "synthetic-password"})
	require.NoError(t, err)
	require.Equal(t, "https", cfg.Protocol)
	require.False(t, cfg.DisableOCSPChecks)
	require.Nil(t, cfg.Transporter)
}

func TestExecuteRequiresScannerAndSanitizesItsErrors(t *testing.T) {
	for _, cause := range []error{
		rawProviderError("synthetic-provider-canary"),
		xerror.Unexpected().Detail("synthetic-provider-canary"),
		xerror.ExecutionResponseSizeExceeded().Detail("synthetic-provider-canary"),
		xerror.ExecutionRowLimitExceeded().Detail("synthetic-provider-canary"),
	} {
		recorder := &queryRecorder{}
		db := stdsql.OpenDB(queryConnector{recorder: recorder})
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		client := &Client{db: db}
		got, err := client.Execute(t.Context(), "SELECT fixture", nil, true, nil)
		require.Error(t, err)
		require.Empty(t, recorder.query, "a missing scanner must fail before querying")
		require.Empty(t, got.Rows)
		got, err = client.Execute(t.Context(), "SELECT fixture", nil, true, func(*stdsql.Rows) (Result, error) {
			return Result{Rows: []map[string]any{{"secret": "synthetic-provider-canary"}}}, cause
		})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-provider-canary")
		require.Empty(t, got.Rows)
		require.True(t, recorder.closed)
		require.True(t, recorder.canceledOnClose, "cancel before closing unread driver rows")
		for _, limitErr := range []error{xerror.ExecutionResponseSizeExceeded(), xerror.ExecutionRowLimitExceeded()} {
			if xerror.Is(cause, limitErr) {
				require.ErrorIs(t, err, limitErr)
			}
		}
	}
}

func TestExecuteChecksAndRedactsIterationAndCloseErrors(t *testing.T) {
	for _, recorder := range []*queryRecorder{
		{iterationErr: rawProviderError("synthetic-provider-canary")},
		{closeErr: rawProviderError("synthetic-provider-canary")},
	} {
		db := stdsql.OpenDB(queryConnector{recorder: recorder})
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		client := &Client{db: db}
		got, err := client.Execute(t.Context(), "SELECT fixture", nil, true, func(rows *stdsql.Rows) (Result, error) {
			for rows.Next() {
			}
			return Result{RowCount: 1}, nil
		})
		require.ErrorIs(t, err, xerror.Unexpected())
		require.NotContains(t, err.Error(), "synthetic-provider-canary")
		require.Empty(t, got.Rows)
		require.True(t, recorder.closed)
	}
}
