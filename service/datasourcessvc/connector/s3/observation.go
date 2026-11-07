package s3

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
)

// ObjectMetadataObserver reads only the exact object's existence, never its body.
type ObjectMetadataObserver struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Key      string `json:"key"`
}

type ObjectMetadataSnapshot struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	Exists bool   `json:"exists"`
}

func (o ObjectMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	if o.Region == "" || len(o.Region) > 64 || o.Bucket == "" || len(o.Bucket) > 63 || strings.ContainsAny(o.Bucket, "/?#@") || o.Key == "" || len(o.Key) > 1024 || strings.ContainsRune(o.Key, 0) {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	address, err := s3client.ObjectMetadataURL(o.Region, o.Endpoint, o.Bucket, o.Key)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || address.Port() != "" && address.Port() != "443" {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: "https", Host: address.Hostname(), Port: 443}
	raw, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	plan := proofevidence.MetadataPlan{ConnectorType: model.DataSourceTypeS3, Operation: "s3.object_metadata", Resource: raw, Target: endpoint, Egress: []proofevidence.Endpoint{endpoint}, Routes: []proofevidence.MetadataRoute{{Method: http.MethodHead, URL: address.String()}}}
	if _, err := plan.Fingerprint(); err != nil {
		return proofevidence.MetadataPlan{}, err
	}
	return plan, nil
}

func (o ObjectMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	var creds Credentials
	if json.Unmarshal(credential, &creds) != nil || limits.MaxRows < 1 {
		return nil, 0, proofevidence.OutcomeDenied
	}
	exists, err := s3client.ReadObjectMetadata(ctx, transport.HTTPClient(), Config{Endpoint: o.Endpoint, Region: o.Region, Bucket: o.Bucket, ForcePathStyle: true}, creds, s3client.HeadObjectRequest{Bucket: o.Bucket, Key: o.Key})
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	raw, err := json.Marshal(ObjectMetadataSnapshot{Bucket: o.Bucket, Key: o.Key, Exists: exists})
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	if int64(len(raw)) > limits.MaxBytes {
		return nil, 1, proofevidence.OutcomeOverBudget
	}
	return raw, 1, proofevidence.OutcomeComplete
}

func (o ObjectMetadataObserver) Description() string {
	return "selected object existence is observed with the bound data-source credential"
}

func (o ObjectMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	var cfg Config
	stmt, err := remotefile.DecodeStatement(operation)
	if err != nil || json.Unmarshal(config, &cfg) != nil || cfg.AllowPrivateHosts || cfg.Region != o.Region || strings.TrimRight(cfg.Endpoint, "/") != strings.TrimRight(o.Endpoint, "/") || stmt.Type != "" && stmt.Type != "s3" {
		return false
	}
	if source && normalizeOperation(stmt.Operation) != operationRead || !source && normalizeOperation(stmt.Operation) != operationWrite {
		return false
	}
	bucket := firstNonEmpty(stmt.Bucket, cfg.Bucket)
	key, err := joinS3Key(cfg.Prefix, stmt.Key)
	return err == nil && bucket == o.Bucket && key == o.Key
}

func (o ObjectMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot ObjectMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Bucket != o.Bucket || snapshot.Key != o.Key {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	if !snapshot.Exists {
		return model.ProofVerdictFailed, []model.ProofDiagnostic{{Code: "proof.object_missing", Summary: "the selected object does not exist", Remediation: "select an existing object and collect fresh metadata"}}, nil
	}
	return model.ProofVerdictPassed, nil, nil
}
