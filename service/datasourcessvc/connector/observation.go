package connector

import (
	"bytes"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
)

var ErrMetadataUnsupported = xerror.Unsupported(xerror.DataSource).Detail("selected-resource Proof metadata is unsupported for this connector operation")

// ResourceMetadataObserver owns resource admission and claim semantics alongside
// its native client. Shared services never interpret provider selectors.
type ResourceMetadataObserver interface {
	proofevidence.MetadataObserver
	MatchesResource(json.RawMessage, json.RawMessage, bool) bool
	Description() string
	AssessMetadata(json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error)
}

// NewMetadataObserver reconstructs trusted connector code, never executable
// routes from a delivery. Its regenerated plan must match the admitted plan.
func NewMetadataObserver(plan proofevidence.MetadataPlan) (ResourceMetadataObserver, error) {
	descriptor, ok := catalog.DescriptorFor(plan.ConnectorType)
	if !ok {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	allowed := false
	for _, operation := range descriptor.ProofMetadata.Operations {
		if operation == plan.Operation {
			allowed = true
		}
	}
	if !allowed {
		return nil, ErrMetadataUnsupported
	}
	var observer ResourceMetadataObserver
	switch plan.ConnectorType {
	case model.DataSourceTypeMongoDB:
		observer = &mongo.CollectionMetadataObserver{}
	case model.DataSourceTypeS3:
		observer = &s3.ObjectMetadataObserver{}
	case model.DataSourceTypePostgres:
		observer = &postgres.TableMetadataObserver{}
	case model.DataSourceTypeMySQL:
		observer = &mysql.TableMetadataObserver{}
	case model.DataSourceTypeHubSpot:
		observer = &hubspot.ContactMetadataObserver{}
	case model.DataSourceTypeBigQuery:
		observer = &bigquery.TableMetadataObserver{}
	case model.DataSourceTypeGoogleWorkspace:
		if plan.Operation == "google_workspace.file_metadata" {
			observer = &googleworkspace.FileMetadataObserver{}
		} else {
			observer = &googleworkspace.WorksheetMetadataObserver{}
		}
	default:
		return nil, proofevidence.ErrAuthorizationDenied
	}
	decoder := json.NewDecoder(bytes.NewReader(plan.Resource))
	decoder.DisallowUnknownFields()
	if decoder.Decode(observer) != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	trusted, err := observer.Plan()
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	want, err := plan.Fingerprint()
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	got, err := trusted.Fingerprint()
	if err != nil || got != want {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return observer, nil
}

// MetadataPolicies is the explicit connector portfolio allowlist, independent of
// connection tests, introspection, pipeline execution and transport choice.
func MetadataPolicies(limits proofevidence.Limits) []proofevidence.OperationPolicy {
	var operations []proofevidence.OperationPolicy
	for _, descriptor := range catalog.AllDescriptors() {
		for _, operation := range descriptor.ProofMetadata.Operations {
			policy := proofevidence.OperationPolicy{ConnectorType: descriptor.Type, Operation: operation, EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly, NoWriteGuarantee: proofevidence.NoWriteReadOnlyAPI, Limits: limits, RunnerTrust: proofevidence.RunnerTrustManaged}
			switch operation {
			case "bigquery.table_metadata":
				policy.ProviderEndpoints = bigquery.MetadataProviderEndpoints()
			case "google_workspace.worksheet_metadata":
				policy.ProviderEndpoints = googleworkspace.WorksheetMetadataProviderEndpoints()
			}
			operations = append(operations, policy)
		}
	}
	return operations
}

// BindMetadataPlan derives protocol/authentication settings from the tenant's
// actual connection, without credentials or a provider call.
func BindMetadataPlan(plan proofevidence.MetadataPlan, config json.RawMessage, credentialKind string) (proofevidence.MetadataPlan, error) {
	observer, err := NewMetadataObserver(plan)
	if err != nil {
		return proofevidence.MetadataPlan{}, err
	}
	switch target := observer.(type) {
	case *mongo.CollectionMetadataObserver:
		var cfg mongo.Config
		if json.Unmarshal(config, &cfg) != nil {
			return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
		}
		target.AuthDatabase = cfg.AuthDatabase
	case *bigquery.TableMetadataObserver:
		// #nosec G101 -- Credential kind discriminator, not credential material.
		target.OAuth = credentialKind == "oauth2_grant"
	case *postgres.TableMetadataObserver:
		var cfg postgres.Config
		if json.Unmarshal(config, &cfg) != nil {
			return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
		}
		target.SSLMode = cfg.SSLMode
	}
	return observer.Plan()
}
