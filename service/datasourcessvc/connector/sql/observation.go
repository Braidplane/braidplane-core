package sql

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
)

// TableMetadataResource binds the selected table to the connection configuration.
// SSLMode is protected PostgreSQL configuration, never an API override.
type TableMetadataResource struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	SSLMode  string `json:"ssl_mode,omitempty"`
}

type TableMetadataSnapshot struct {
	Database string           `json:"database"`
	Schema   string           `json:"schema"`
	Table    string           `json:"table"`
	Columns  []MetadataColumn `json:"columns"`
}

type MetadataColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

func TableMetadataPlan(kind model.DataSourceType, resource TableMetadataResource) (proofevidence.MetadataPlan, error) {
	if kind != model.DataSourceTypePostgres && kind != model.DataSourceTypeMySQL {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	for _, selector := range []string{resource.Database, resource.Schema, resource.Table} {
		if selector == "" || len(selector) > 128 || strings.ContainsRune(selector, 0) {
			return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
		}
	}
	if resource.SSLMode != "" && resource.SSLMode != "disable" && resource.SSLMode != "require" && resource.SSLMode != "verify-ca" && resource.SSLMode != "verify-full" {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	if kind == model.DataSourceTypeMySQL && (resource.Schema != resource.Database || resource.SSLMode != "") {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: kind.String(), Host: resource.Host, Port: resource.Port}
	operation := kind.String() + ".table_metadata"
	plan := proofevidence.MetadataPlan{MaxConnections: 1, ConnectorType: kind, Operation: operation, Resource: raw, Target: endpoint, Egress: []proofevidence.Endpoint{endpoint}, Routes: []proofevidence.MetadataRoute{{Method: "metadata", URL: operation}}}
	if _, err := plan.Fingerprint(); err != nil {
		return proofevidence.MetadataPlan{}, err
	}
	return plan, nil
}

// ReadTableMetadata scans only a fixed, parameterized system-catalog query.
func ReadTableMetadata(rows *sql.Rows, resource TableMetadataResource, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	defer func() { _ = rows.Close() }()
	snapshot := TableMetadataSnapshot{Database: resource.Database, Schema: resource.Schema, Table: resource.Table}
	var count int64
	for rows.Next() {
		var column MetadataColumn
		var nullable string
		if rows.Scan(&column.Name, &column.Type, &nullable) != nil || column.Name == "" || column.Type == "" {
			return nil, count, proofevidence.OutcomePartial
		}
		count++
		if count > limits.MaxRows {
			return nil, count, proofevidence.OutcomeOverBudget
		}
		column.Nullable = nullable == "YES"
		snapshot.Columns = append(snapshot.Columns, column)
	}
	if rows.Err() != nil || count == 0 {
		return nil, count, proofevidence.OutcomePartial
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, count, proofevidence.OutcomePartial
	}
	if int64(len(raw)) > limits.MaxBytes {
		return nil, count, proofevidence.OutcomeOverBudget
	}
	return raw, count, proofevidence.OutcomeComplete
}

func AssessTableMetadata(resource TableMetadataResource, raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot TableMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || resource.Database != snapshot.Database || resource.Schema != snapshot.Schema || resource.Table != snapshot.Table || len(snapshot.Columns) == 0 {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	return model.ProofVerdictPassed, nil, nil
}
