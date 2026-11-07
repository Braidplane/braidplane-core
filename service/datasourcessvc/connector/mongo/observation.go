package mongo

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
)

// CollectionMetadataObserver uses a name-filtered catalog command, never records.
type CollectionMetadataObserver struct {
	Host         string `json:"host"`
	Port         uint16 `json:"port"`
	Database     string `json:"database"`
	Collection   string `json:"collection"`
	AuthDatabase string `json:"auth_database,omitempty"`
}

type CollectionMetadataSnapshot struct {
	Database   string `json:"database"`
	Collection string `json:"collection"`
	Type       string `json:"type"`
}

func (o CollectionMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	for _, selector := range []string{o.Database, o.Collection} {
		if selector == "" || len(selector) > 128 || strings.ContainsRune(selector, 0) {
			return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
		}
	}
	if len(o.AuthDatabase) > 128 || strings.ContainsRune(o.AuthDatabase, 0) {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: "mongodb", Host: o.Host, Port: o.Port}
	plan := proofevidence.MetadataPlan{MaxConnections: 2, ConnectorType: model.DataSourceTypeMongoDB, Operation: "mongodb.collection_metadata", Resource: raw, Target: endpoint, Egress: []proofevidence.Endpoint{endpoint}, Routes: []proofevidence.MetadataRoute{{Method: "metadata", URL: "mongodb.collection_metadata"}}}
	if _, err := plan.Fingerprint(); err != nil {
		return proofevidence.MetadataPlan{}, err
	}
	return plan, nil
}

func (o CollectionMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (metadataJSON json.RawMessage, rows int64, outcome proofevidence.Outcome) {
	plan, err := o.Plan()
	if err != nil || transport.BeginNative(plan.Routes[0]) != nil {
		return nil, 0, proofevidence.OutcomeDenied
	}
	var creds Credentials
	if json.Unmarshal(credential, &creds) != nil || creds.Username == "" || creds.Password == "" {
		return nil, 0, proofevidence.OutcomeDenied
	}
	authDatabase := o.AuthDatabase
	if authDatabase == "" {
		authDatabase = "admin"
	}
	opts := options.Client().SetHosts([]string{net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port)))}).
		SetAuth(options.Credential{Username: creds.Username, Password: creds.Password, AuthSource: authDatabase}).
		SetDirect(true).SetMaxPoolSize(1).SetMinPoolSize(0).SetRetryReads(false).SetRetryWrites(false).
		SetServerMonitoringMode(options.ServerMonitoringModePoll).
		SetConnectTimeout(limits.Timeout).SetServerSelectionTimeout(limits.Timeout).SetDialer(transport)
	if !transportsecurity.PlaintextAllowed(ctx) {
		opts.SetTLSConfig(transportsecurity.TLS(o.Host))
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, 0, proofevidence.OutcomeUnreachable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if client.Disconnect(cleanup) != nil {
			metadataJSON, outcome = nil, proofevidence.OutcomeCleanupUncertain
		}
	}()
	cursor, err := client.Database(o.Database).ListCollections(ctx, bson.D{{Key: "name", Value: o.Collection}}, options.ListCollections().SetNameOnly(true).SetBatchSize(2))
	if err != nil {
		return nil, 0, proofevidence.OutcomeProviderError
	}
	// One selected name must fit in the initial batch; no getMore or document read.
	if cursor.RemainingBatchLength() != 1 || cursor.ID() != 0 || !cursor.Next(ctx) {
		return nil, 0, proofevidence.OutcomePartial
	}
	var metadata struct {
		Name string `bson:"name"`
		Type string `bson:"type"`
	}
	if cursor.Decode(&metadata) != nil || metadata.Name != o.Collection || metadata.Type != "collection" && metadata.Type != "view" || cursor.RemainingBatchLength() != 0 || cursor.ID() != 0 {
		return nil, 0, proofevidence.OutcomePartial
	}
	if limits.MaxRows < 1 {
		return nil, 1, proofevidence.OutcomeOverBudget
	}
	raw, err := json.Marshal(CollectionMetadataSnapshot{Database: o.Database, Collection: o.Collection, Type: metadata.Type})
	if err != nil {
		return nil, 1, proofevidence.OutcomePartial
	}
	if int64(len(raw)) > limits.MaxBytes {
		return nil, 1, proofevidence.OutcomeOverBudget
	}
	return raw, 1, proofevidence.OutcomeComplete
}

func (o CollectionMetadataObserver) Description() string {
	return "selected collection metadata is reachable with the bound data-source credential"
}

func (o CollectionMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	var cfg Config
	var statement struct{ Type, Operation, Collection string }
	if json.Unmarshal(config, &cfg) != nil || cfg.AllowPrivateHosts || cfg.Host != o.Host || cfg.Port != int(o.Port) || cfg.Database != o.Database || o.AuthDatabase != "" && cfg.AuthDatabase != o.AuthDatabase || json.Unmarshal(operation, &statement) != nil || statement.Type != "" && statement.Type != "mongodb" || statement.Collection != o.Collection {
		return false
	}
	if source {
		return statement.Operation == "find" || statement.Operation == "aggregate" || statement.Operation == "count_documents"
	}
	var op Operation
	if op.UnmarshalText([]byte(statement.Operation)) != nil {
		return false
	}
	return op == OperationInsertOne || op == OperationInsertMany || op == OperationUpdateOne || op == OperationUpdateMany || op == OperationDeleteOne || op == OperationDeleteMany
}

func (o CollectionMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot CollectionMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Database != o.Database || snapshot.Collection != o.Collection || snapshot.Type != "collection" && snapshot.Type != "view" {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	return model.ProofVerdictPassed, nil, nil
}
