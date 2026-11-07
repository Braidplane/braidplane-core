package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/go-sql-driver/mysql"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

type TableMetadataObserver struct {
	sqlconnector.TableMetadataResource
}

func (o TableMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	return sqlconnector.TableMetadataPlan(model.DataSourceTypeMySQL, o.TableMetadataResource)
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
	if creds.Username == "" || creds.Password == nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	config := mysql.NewConfig()
	config.User, config.Passwd, config.Net, config.DBName = creds.Username, *creds.Password, "tcp", o.Database
	config.Addr = net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port)))
	if !transportsecurity.PlaintextAllowed(ctx) {
		config.TLS = transportsecurity.TLS(o.Host)
	}
	config.DialFunc = transport.DialContext
	config.Timeout, config.ReadTimeout, config.WriteTimeout = limits.Timeout, limits.Timeout, limits.Timeout
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	//nolint:rowserrcheck // ReadTableMetadata consumes, checks and closes rows.
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position LIMIT ?`, o.Database, o.Table, limits.MaxRows+1)
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
	return json.Unmarshal(config, &cfg) == nil && !cfg.AllowPrivateHosts && cfg.Host == o.Host && cfg.Port == int(o.Port) && cfg.Database == o.Database &&
		sqlconnector.MetadataStatementMatches(model.DataSourceTypeMySQL, o.TableMetadataResource, operation, source)
}

func (o TableMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	return sqlconnector.AssessTableMetadata(o.TableMetadataResource, raw)
}
