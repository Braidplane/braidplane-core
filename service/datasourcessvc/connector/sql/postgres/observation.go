package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

type TableMetadataObserver struct {
	sqlconnector.TableMetadataResource
}

func (o TableMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	return sqlconnector.TableMetadataPlan(model.DataSourceTypePostgres, o.TableMetadataResource)
}

func (o TableMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	plan, err := o.Plan()
	if err != nil || transport.BeginNative(plan.Routes[0]) != nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	var creds Credentials
	if json.Unmarshal(credential, &creds) != nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	config := Config{Host: o.Host, Port: int(o.Port), Database: o.Database, SSLMode: o.SSLMode, User: creds.Username, Password: creds.Password, ConnectTimeout: max(1, int(limits.Timeout.Seconds()))}
	if config.User == "" || config.Password == "" {
		return nil, 0, proofevidence.OutcomeDenied
	}
	if config.SSLMode == "" {
		config.SSLMode = "verify-full"
	}
	if !transportsecurity.PlaintextAllowed(ctx) && config.SSLMode != "verify-full" {
		return nil, 0, proofevidence.OutcomeDenied
	}
	connection, err := pgx.ParseConfig(buildDSN(config))
	if err != nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	// The admitted dialer owns DNS; pgx must retain the reviewed hostname.
	connection.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	connection.DialFunc = transport.DialContext
	connection.DefaultQueryExecMode = pgx.QueryExecModeExec
	db := sql.OpenDB(stdlib.GetConnector(*connection))
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	//nolint:rowserrcheck // ReadTableMetadata consumes, checks and closes rows.
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_catalog = $1 AND table_schema = $2 AND table_name = $3 ORDER BY ordinal_position LIMIT $4`, o.Database, o.Schema, o.Table, limits.MaxRows+1)
	if err != nil {
		return nil, 0, proofevidence.OutcomeProviderError
	}
	return sqlconnector.ReadTableMetadata(rows, o.TableMetadataResource, limits)
}

func (o TableMetadataObserver) Description() string {
	return "selected table metadata is reachable with the bound data-source credential"
}

func (o TableMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	var cfg Config
	return json.Unmarshal(config, &cfg) == nil && !cfg.AllowPrivateHosts && cfg.Host == o.Host && cfg.Port == int(o.Port) && cfg.Database == o.Database && (o.SSLMode == "" || o.SSLMode == cfg.SSLMode) &&
		sqlconnector.MetadataStatementMatches(model.DataSourceTypePostgres, o.TableMetadataResource, operation, source)
}

func (o TableMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	return sqlconnector.AssessTableMetadata(o.TableMetadataResource, raw)
}
