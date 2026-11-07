package bigquery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
)

// TableMetadataObserver identifies one table, including its owning dataset.
type TableMetadataObserver struct {
	OAuth     bool   `json:"oauth,omitempty"`
	ProjectID string `json:"project_id"`
	DatasetID string `json:"dataset_id"`
	TableID   string `json:"table_id"`
}

type TableMetadataSnapshot struct {
	TableType client.TableType `json:"table_type"`
	Fields    []client.Field   `json:"fields"`
}

func (o TableMetadataObserver) request() client.GetTableRequest {
	return client.GetTableRequest{ProjectID: o.ProjectID, DatasetID: o.DatasetID, TableID: o.TableID}
}

func (o TableMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	if !validMetadataResourceID(o.ProjectID) || !validMetadataResourceID(o.DatasetID) || !validMetadataResourceID(o.TableID) {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	resource, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: "https", Host: "bigquery.googleapis.com", Port: 443}
	plan := proofevidence.MetadataPlan{
		ConnectorType: model.DataSourceTypeBigQuery, Operation: "bigquery.table_metadata", Resource: resource,
		Target: endpoint, Egress: append([]proofevidence.Endpoint{endpoint}, MetadataProviderEndpoints()...),
		Routes: []proofevidence.MetadataRoute{{Method: http.MethodPost, URL: "https://oauth2.googleapis.com/token"}, {Method: http.MethodGet, URL: "https://bigquery.googleapis.com" + client.TableMetadataPath(o.request())}},
	}
	if o.OAuth {
		plan.Routes = plan.Routes[1:]
	}
	return plan, nil
}

func (o TableMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	var creds client.Credentials
	if json.Unmarshal(credential, &creds) != nil || o.OAuth != (creds.AccessToken != "") {
		return nil, 0, proofevidence.OutcomePartial
	}
	metadata, err := client.ReadTableMetadata(ctx, transport.HTTPClient(), creds, o.request())
	if err != nil || len(metadata.Fields) == 0 {
		return nil, 0, proofevidence.OutcomePartial
	}
	var rows int64
	if outcome := validateMetadataFields(metadata.Fields, limits.MaxRows, &rows); outcome != proofevidence.OutcomeComplete {
		return nil, rows, outcome
	}
	encoded, err := json.Marshal(TableMetadataSnapshot{TableType: metadata.Type, Fields: metadata.Fields})
	if err != nil {
		return nil, rows, proofevidence.OutcomePartial
	}
	return encoded, rows, proofevidence.OutcomeComplete
}

func validMetadataResourceID(value string) bool {
	// Naming extensions need review before they may enter an exact request route.
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validateMetadataFields(fields []client.Field, maxRows int64, rows *int64) proofevidence.Outcome {
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if field.Name == "" || field.Type == "" || seen[field.Name] {
			return proofevidence.OutcomePartial
		}
		seen[field.Name] = true
		*rows++
		if *rows > maxRows {
			return proofevidence.OutcomeOverBudget
		}
		if outcome := validateMetadataFields(field.Fields, maxRows, rows); outcome != proofevidence.OutcomeComplete {
			return outcome
		}
	}
	return proofevidence.OutcomeComplete
}

func MetadataProviderEndpoints() []proofevidence.Endpoint {
	return []proofevidence.Endpoint{{Scheme: "https", Host: "oauth2.googleapis.com", Port: 443}}
}

func (o TableMetadataObserver) Description() string {
	return "destination resource supports the declared append mode"
}

func (o TableMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	var cfg client.Config
	var stmt struct{ Type, Operation, Dataset, Table string }
	if source || json.Unmarshal(config, &cfg) != nil || cfg.AllowPrivateHosts ||
		cfg.BaseURL != "" && strings.TrimRight(cfg.BaseURL, "/") != "https://bigquery.googleapis.com" || cfg.TokenURL != "" && cfg.TokenURL != "https://oauth2.googleapis.com/token" ||
		json.Unmarshal(operation, &stmt) != nil || stmt.Type != "" && stmt.Type != "bigquery" || strings.ToLower(strings.TrimSpace(stmt.Operation)) != "insert_rows" {
		return false
	}
	dataset := strings.TrimSpace(stmt.Dataset)
	if dataset == "" {
		dataset = strings.TrimSpace(cfg.DefaultDataset)
	}
	return o.ProjectID == cfg.ProjectID && o.DatasetID == dataset && o.TableID == strings.TrimSpace(stmt.Table)
}

func (o TableMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot TableMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || !snapshot.TableType.IsATableType() || snapshot.TableType == client.TableTypeUnknown {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	if snapshot.TableType == client.TableTypeTable {
		return model.ProofVerdictUnverified, []model.ProofDiagnostic{{Code: "proof.append_permission_unverified", Summary: "table metadata does not establish append permission or successful insertion", Remediation: "collect supported permission evidence; metadata does not prove write success"}}, nil
	}
	return model.ProofVerdictFailed, []model.ProofDiagnostic{{Code: "proof.append_resource_incompatible", Summary: "the observed resource type cannot accept the declared append operation", Remediation: "select a compatible destination and collect new read-only evidence"}}, nil
}
