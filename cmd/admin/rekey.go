package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/service/credentialrekeysvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/jackc/pgx/v5/pgxpool"
)

func runCredentialRekeyCLI(configArgs []string, command rekeyCommand, stdout io.Writer) error {
	after, limit, err := command.cursor()
	if err != nil {
		return err
	}
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.toml"
	}
	cfg, err := backendconfig.LoadWithArgs(configPath, configArgs)
	if err != nil {
		return xerror.ValidationFailed().Detail("invalid credential rekey configuration")
	}
	ring, err := cfg.Security.CredentialKeyring()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runID := uuid.New().String()
	ctx = reqctx.WithRequestID(ctx, runID)
	pool, err := pgxpool.New(ctx, cfg.Database.ConnectionString())
	if err != nil {
		return xerror.Unexpected().Detail("connect credential rekey database")
	}
	defer pool.Close()
	txm := postgres.NewTransactionManager(pool)
	sweep, err := credentialrekeysvc.New(txm, credentialrekey.New(), ring, eventssvc.New(txm, events.New(), clock.New()))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	if err := encoder.Encode(map[string]string{"run_id": runID, "current_key_id": ring.CurrentKeyID()}); err != nil {
		return xerror.Unexpected().Detail("write credential rekey progress")
	}
	conflicts := 0
	for {
		result, err := sweep.Batch(ctx, after, limit)
		if err != nil {
			return err
		}
		if err := encoder.Encode(result); err != nil {
			return xerror.Unexpected().Detail("write credential rekey progress")
		}
		after = result.After
		conflicts += result.Conflicts
		if result.Done {
			if conflicts > 0 {
				return xerror.ValidationFailed().Detail("concurrent credential changes detected; repeat a full sweep")
			}
			return nil
		}
		// At most 100 rows per batch, with a one-second pause outside the transaction.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return xerror.Unexpected().Detail("credential rekey interrupted; resume from the last emitted cursor")
		case <-timer.C:
		}
	}
}

func (c rekeyCommand) cursor() (credentialrekey.Cursor, int32, error) {
	if c.AfterFamily < 0 || c.AfterFamily > 6 || c.AfterID < 0 || c.AfterFamily == 0 && c.AfterID != 0 || c.BatchSize < 1 || c.BatchSize > 100 {
		return credentialrekey.Cursor{}, 0, xerror.ValidationFailed().Detail("invalid rekey cursor or batch size")
	}
	return credentialrekey.Cursor{Family: int32(c.AfterFamily), ID: c.AfterID}, int32(c.BatchSize), nil // #nosec G115 -- bounds checked above.
}
