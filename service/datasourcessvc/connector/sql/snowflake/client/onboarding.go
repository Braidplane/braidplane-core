package client

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func verificationFailure(detail string) error {
	return xerror.InvalidCredentials().WithPublicDetail(detail).Detail(detail)
}

// verifySetup reads grants on one authenticated session; it never writes table data or administrative SQL.
func (c *Client) verifySetup(ctx context.Context) error {
	setup := *c.wifSetup
	if err := setup.Validate(); err != nil {
		return err
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		return mapConnectionError(err, AuthTypeWorkloadIdentity)
	}
	defer func() { _ = conn.Close() }()
	for _, statement := range []string{
		"USE ROLE " + snowflakewif.QuoteIdentifier(setup.Role),
		"USE SECONDARY ROLES NONE",
		"USE WAREHOUSE " + snowflakewif.QuoteIdentifier(setup.Warehouse),
		"USE DATABASE " + snowflakewif.QuoteIdentifier(setup.Database),
		"USE SCHEMA " + snowflakewif.QuoteIdentifier(setup.Database) + "." + snowflakewif.QuoteIdentifier(setup.Schema),
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return verificationFailure("Snowflake role or warehouse/database/schema access failed. Run the generated role and USAGE grants, then retry Verify.")
		}
	}
	// Separate metadata columns preserve quoted identifiers containing dots or spaces.
	// Filtering by grantee requires the explicit table grants produced by the script.
	args := []any{setup.Database, setup.Schema, setup.Role}
	placeholders := make([]string, len(setup.Objects))
	for i, object := range setup.Objects {
		args = append(args, object.Name)
		placeholders[i] = "?"
	}
	// #nosec G202 -- The database is one escaped identifier; placeholders are fixed SQL and all filter values are bound.
	query := `SELECT object_name, privilege_type FROM ` + snowflakewif.QuoteIdentifier(setup.Database) + `.INFORMATION_SCHEMA.OBJECT_PRIVILEGES
 WHERE object_catalog = ? AND object_schema = ? AND grantee = ? AND object_type = 'TABLE'
 AND privilege_type IN ('SELECT', 'INSERT') AND object_name IN (` + strings.Join(placeholders, ",") + `) LIMIT 201`
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return verificationFailure("Snowflake object privileges could not be verified. Check warehouse access and the generated table grants, then retry Verify.")
	}
	defer func() { _ = rows.Close() }()
	grants := map[snowflakewif.ObjectGrant]bool{}
	for count := 0; rows.Next(); count++ {
		if count >= 200 {
			return verificationFailure("Snowflake object privilege verification exceeded its result limit.")
		}
		var object snowflakewif.ObjectGrant
		if err := rows.Scan(&object.Name, &object.Privilege); err != nil {
			return verificationFailure("Snowflake object privileges could not be read safely. Retry Verify.")
		}
		grants[object] = true
	}
	if rows.Err() != nil {
		return verificationFailure("Snowflake object privileges could not be read safely. Retry Verify.")
	}
	for _, object := range setup.Objects {
		if !grants[object] {
			return verificationFailure("Snowflake selected table privileges are missing. Run the generated SELECT or INSERT grants for every selected table, then retry Verify.")
		}
	}
	return nil
}
