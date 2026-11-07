package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/snowflakedb/gosnowflake/v2"
	"github.com/stretchr/testify/require"
)

func TestVerifyOnboardingGrants(t *testing.T) {
	for _, test := range []struct {
		name, omit, want string
		delay            time.Duration
		timeout          time.Duration
		canceled         bool
	}{
		{name: "source and sink"},
		{name: "warehouse query exceeds old five second budget", delay: 6 * time.Second},
		{name: "deadline", delay: time.Second, timeout: 20 * time.Millisecond, want: "verification timed out"},
		{name: "cancellation", canceled: true, want: "verification was canceled"},
		{name: "missing warehouse", omit: "USAGE:WAREHOUSE", want: "role or warehouse/database/schema access failed"},
		{name: "missing source", omit: "SELECT:TABLE", want: "selected table privileges are missing"},
		{name: "missing sink", omit: "INSERT:TABLE", want: "selected table privileges are missing"},
		{name: "provider error", omit: "error", want: "object privileges could not be verified"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var statements []string
			transport := snowflakeRoundTripper(func(request *http.Request) (*http.Response, error) {
				response := `{"success":true,"data":{}}`
				switch request.URL.Path {
				case "/session/v1/login-request":
					response = `{"success":true,"data":{"token":"test-session","masterToken":"test-master","validityInSeconds":3600,"masterValidityInSeconds":3600,"sessionInfo":{"databaseName":"DB","schemaName":"PUBLIC","warehouseName":"WH","roleName":"DEDICATED"}}}`
				case "/queries/v1/query-request":
					var body struct {
						SQL string `json:"sqlText"`
					}
					require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
					statements = append(statements, body.SQL)
					if test.omit == "USAGE:WAREHOUSE" && strings.HasPrefix(body.SQL, "USE WAREHOUSE") {
						response = `{"success":false,"code":"1234","message":"private provider diagnostic","data":{"sqlState":"42501"}}`
					}
					if strings.HasPrefix(body.SQL, "SELECT object_name") {
						if test.delay > 0 {
							select {
							case <-time.After(test.delay):
							case <-request.Context().Done():
								return nil, request.Context().Err()
							}
						}
						if test.omit == "error" {
							response = `{"success":false,"code":"1234","message":"private provider diagnostic","data":{"sqlState":"42501"}}`
						} else {
							rows := [][]string{}
							for _, row := range [][]string{{`Source.with "quotes"`, "SELECT"}, {"SINK", "INSERT"}} {
								if row[1]+":TABLE" != test.omit {
									rows = append(rows, row)
								}
							}
							payload, err := json.Marshal(map[string]any{"success": true, "data": map[string]any{"rowtype": []map[string]string{{"name": "OBJECT_NAME", "type": "text"}, {"name": "PRIVILEGE_TYPE", "type": "text"}}, "rowset": rows, "total": len(rows)}})
							require.NoError(t, err)
							response = string(payload)
						}
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			config := gosnowflake.Config{Account: "test-account", User: "test-user", Password: "test-password", Transporter: transport}
			db := sql.OpenDB(gosnowflake.NewConnector(gosnowflake.SnowflakeDriver{}, config))
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			client := Client{db: db, connectTimeoutSeconds: defaultConnectTimeoutSeconds, wifSetup: &snowflakewif.Setup{Warehouse: "WH", Database: "DB", Schema: "PUBLIC", Role: "DEDICATED", Objects: []snowflakewif.ObjectGrant{{Name: `Source.with "quotes"`, Privilege: "SELECT"}, {Name: "SINK", Privilege: "INSERT"}}}}
			ctx := t.Context()
			if test.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.timeout)
				defer cancel()
			}
			if test.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := client.TestConnection(ctx)
			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
				require.Contains(t, xerror.PublicDetail(err), test.want)
				require.NotContains(t, err.Error(), "private provider diagnostic")
			}
			if test.timeout > 0 || test.canceled {
				require.True(t, xerror.Is(err, xerror.Unavailable(xerror.DataSource)))
				require.NotContains(t, xerror.PublicDetail(err), "Check warehouse access")
				return
			}
			if test.omit == "USAGE:WAREHOUSE" {
				require.Len(t, statements, 3)
			} else {
				require.Len(t, statements, 6)
			}
			for _, statement := range statements {
				require.True(t, strings.HasPrefix(statement, "USE ") || strings.HasPrefix(statement, "SELECT object_name"))
			}
		})
	}
}
