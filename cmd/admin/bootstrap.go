package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/instanceadmins"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/instanceadminsvc"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runBootstrapCLI(configArgs []string, command bootstrapCommand, stdin io.Reader, stdout io.Writer) error {
	if !command.EmailOwnershipVerified {
		return xerror.ValidationFailed().Detail("bootstrap requires --email-ownership-verified; supply password on stdin")
	}
	userSlug, err := slug.New(command.User)
	if err != nil {
		return err
	}
	password, err := readBootstrapPassword(stdin)
	if err != nil {
		return err
	}
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.toml"
	}
	cfg, err := backendconfig.LoadWithArgs(configPath, configArgs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.Database.ConnectionString())
	if err != nil {
		return xerror.Unexpected().Detail("connect bootstrap database")
	}
	defer pool.Close()
	hasher := crypto.NewArgon2WithParams(cfg.Security.Argon2Memory, cfg.Security.Argon2Iterations, cfg.Security.Argon2Parallelism, cfg.Security.Argon2SaltLength, cfg.Security.Argon2KeyLength)
	bootstrap, err := instanceadminsvc.NewBootstrapper(postgres.NewTransactionManager(pool), instanceadmins.NewRepository(), users.NewRepository(), hasher)
	if err != nil {
		return err
	}
	if err := bootstrap.Bootstrap(ctx, userSlug, command.Name, command.Email, password); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "instance administrator bootstrapped for user %s\n", userSlug)
	if err != nil {
		return xerror.Wrapf("write bootstrap result: %w", err)
	}
	return nil
}

func readBootstrapPassword(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, 1027))
	if err != nil {
		return "", xerror.Unexpected().Detail("read bootstrap password")
	}
	defer clear(data)
	password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(password) < 8 || len(password) > 1024 || strings.ContainsAny(password, "\r\n") {
		return "", xerror.ValidationFailed().Detail("stdin must contain one password of 8 to 1024 bytes")
	}
	return password, nil
}
