package hubspot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
)

// ContactMetadataObserver identifies the account whose contact schema is read.
type ContactMetadataObserver struct {
	AccountID int64 `json:"account_id"`
}

type ContactMetadataSnapshot struct {
	Properties []crm.Property `json:"properties"`
}

func (o ContactMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	if o.AccountID <= 0 {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	resource, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: "https", Host: "api.hubapi.com", Port: 443}
	return proofevidence.MetadataPlan{
		ConnectorType: model.DataSourceTypeHubSpot, Operation: "hubspot.contact_metadata", Resource: resource,
		Target: endpoint, Egress: []proofevidence.Endpoint{endpoint},
		Routes: []proofevidence.MetadataRoute{{Method: http.MethodGet, URL: "https://api.hubapi.com" + crm.AccountMetadataPath}, {Method: http.MethodGet, URL: "https://api.hubapi.com" + crm.ContactPropertiesPath}},
	}, nil
}

func (o ContactMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	var creds Credentials
	if json.Unmarshal(credential, &creds) != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	token := strings.TrimSpace(creds.AccessToken)
	if token == "" {
		token = strings.TrimSpace(creds.PrivateAppToken)
	}
	properties, err := crm.ReadContactMetadata(ctx, transport.HTTPClient(), token, o.AccountID, limits.MaxBytes)
	if err != nil || len(properties) == 0 {
		return nil, 0, proofevidence.OutcomePartial
	}
	seen := make(map[string]bool, len(properties))
	var rows int64
	for i := range properties {
		property := &properties[i]
		if property.Name == "" || property.Type == "" || seen[property.Name] {
			return nil, rows, proofevidence.OutcomePartial
		}
		seen[property.Name] = true
		property.Label = ""
		rows++
		if rows > limits.MaxRows {
			return nil, rows, proofevidence.OutcomeOverBudget
		}
	}
	encoded, err := json.Marshal(ContactMetadataSnapshot{Properties: properties})
	if err != nil {
		return nil, rows, proofevidence.OutcomePartial
	}
	return encoded, rows, proofevidence.OutcomeComplete
}

func (o ContactMetadataObserver) Description() string {
	return "source credential reaches the authorized HubSpot contact-metadata account"
}

func (o ContactMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	var cfg struct {
		BaseURL           string `json:"base_url"`
		AllowPrivateHosts bool   `json:"allow_private_hosts"`
	}
	var stmt struct {
		Type, Operation string
		ObjectType      string `json:"object_type"`
	}
	return source && json.Unmarshal(config, &cfg) == nil && !cfg.AllowPrivateHosts && (cfg.BaseURL == "" || cfg.BaseURL == "https://api.hubapi.com") &&
		json.Unmarshal(operation, &stmt) == nil && (stmt.Type == "" || stmt.Type == "hubspot") && o.AccountID > 0 && stmt.ObjectType == "contacts" && (stmt.Operation == "" || stmt.Operation == "list" || stmt.Operation == "search")
}

func (o ContactMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot ContactMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.Properties) == 0 {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	return model.ProofVerdictPassed, nil, nil
}
