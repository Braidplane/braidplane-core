package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/kongcmd"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/service/instanceadminsvc"
)

type adminCLI struct {
	Bootstrap       bootstrapCommand `cmd:"" help:"Create the first instance administrator; read the password from stdin."`
	Grant           userCommand      `cmd:"" help:"Grant instance administrator access to a verified user."`
	Revoke          userCommand      `cmd:"" help:"Revoke instance administrator access."`
	CredentialRekey rekeyCommand     `cmd:"" help:"Re-encrypt stored credentials with the current key."`
}

type userCommand struct {
	User string `required:"" help:"Verified user slug."`
}

type bootstrapCommand struct {
	User                   string `required:"" help:"New account slug."`
	Name                   string `required:"" help:"Account full name."`
	Email                  string `required:"" help:"Operator-verified email address."`
	EmailOwnershipVerified bool   `help:"Attest independent verification of email ownership."`
}

type rekeyCommand struct {
	AfterFamily int   `help:"Resume after this storage family (0-6)."`
	AfterID     int64 `name:"after-id" help:"Resume after this row ID within the family."`
	BatchSize   int   `default:"50" help:"Rows per transaction (1-100), with one second between batches."`
}

func parseAdminCLI(args []string, stdout, stderr io.Writer) (kongcmd.ParseResult[adminCLI], []string, error) {
	configArgs, commandArgs, err := config.ExtractFlagArgs(&backendconfig.Config{}, args)
	if err != nil {
		return kongcmd.ParseResult[adminCLI]{}, nil, xerror.Wrapf("extract administrator configuration arguments: %w", err)
	}
	result, err := kongcmd.Parse(commandArgs, adminCLI{}, "braidplane-admin",
		"Run local instance administration using protected installation configuration.", stdout, stderr,
		func(code int) error {
			return xerror.ValidationFailed().Detailf("administrator command exited with status %d", code)
		})
	return result, configArgs, err
}

func runAdminCLI(args []string, stdout, stderr io.Writer) error {
	result, configArgs, err := parseAdminCLI(args, stdout, stderr)
	if result.Exited || err != nil {
		return err
	}
	command := result.Context.Command()
	if command == "bootstrap" {
		return runBootstrapCLI(configArgs, result.CLI.Bootstrap, os.Stdin, stdout)
	}
	if command == "credential-rekey" {
		return runCredentialRekeyCLI(configArgs, result.CLI.CredentialRekey, stdout)
	}
	user := result.CLI.Revoke.User
	grant := command == "grant"
	if grant {
		user = result.CLI.Grant.User
	}
	userSlug, err := slug.New(user)
	if err != nil {
		return xerror.InvalidInput(xerror.User).Detail("user must be a valid slug")
	}
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.toml"
	}
	cfg, err := backendconfig.LoadWithArgs(configPath, configArgs)
	if err != nil {
		return xerror.Wrapf("load administrator configuration: %w", err)
	}
	pool, err := pgxpool.New(context.Background(), cfg.Database.ConnectionString())
	if err != nil {
		return xerror.Unexpected().Detail("connect administrator database")
	}
	defer pool.Close()
	service, err := instanceadminsvc.New(postgres.NewTransactionManager(pool), instanceadmins.NewRepository())
	if err != nil {
		return err
	}
	if grant {
		err = service.Grant(context.Background(), userSlug)
	} else {
		err = service.Revoke(context.Background(), userSlug)
	}
	if err != nil {
		return err
	}
	action := "revoked"
	if grant {
		action = "granted"
	}
	if _, err = fmt.Fprintf(stdout, "instance administrator %s for user %s\n", action, userSlug); err != nil {
		return xerror.Wrapf("write administrator result: %w", err)
	}
	return nil
}
