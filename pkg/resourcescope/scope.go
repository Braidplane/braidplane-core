// Package resourcescope holds non-secret, immutable connector scope rules.
// It does not resolve environments, credentials, or imported server metadata.
package resourcescope

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const S3PolicyID = catalog.S3ResourceScopePolicyID

var ErrInvalid = xerror.New(xerror.KindInvalidInput, "RESOURCE_SCOPE_INVALID", "resource scope or selector is unsupported or outside the permitted scope")

// Scope maps one pinned contract object to a logical saved operation. Concrete
// environment/binding selection remains a deployment responsibility.
type Scope struct {
	PolicyID       string                       `json:"policy_id"`
	DataSource     slug.Slug                    `json:"data_source"`
	Operation      slug.Slug                    `json:"operation"`
	Contract       slug.Slug                    `json:"contract"`
	Revision       slug.Slug                    `json:"revision"`
	ObjectID       string                       `json:"object_id"`
	Representation model.ContractRepresentation `json:"representation"`
	S3             *S3                          `json:"s3"`
}

// Identity omits concrete target and selector settings from historical readback.
type Identity struct {
	PolicyID, DataSource, Operation, Contract, Revision, ObjectID, Representation string
}

func (s *Scope) Identity() *Identity {
	if s == nil {
		return nil
	}
	return &Identity{PolicyID: s.PolicyID, DataSource: s.DataSource.String(), Operation: s.Operation.String(), Contract: s.Contract.String(), Revision: s.Revision.String(), ObjectID: s.ObjectID, Representation: s.Representation.String()}
}

// ValidateStatement checks the supported S3 field roles before compilation.
// Only key, content and object metadata may contain execution substitutions.
func (s Scope) ValidateStatement(raw json.RawMessage) error {
	if s.Validate() != nil {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	for name, value := range fields {
		switch name {
		case "key", "content", "content_type", "metadata":
		case "type", "operation", "bucket", "format", "delimiter", "has_header", "overwrite":
			if strings.ContainsAny(string(value), "{}$") {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	var stmt struct {
		Type      string `json:"type"`
		Operation string `json:"operation"`
		Bucket    string `json:"bucket"`
		Key       string `json:"key"`
		Format    string `json:"format"`
	}
	if json.Unmarshal(raw, &stmt) != nil || stmt.Type != "s3" || (stmt.Operation != "read" && stmt.Operation != "write") || (stmt.Bucket != "" && stmt.Bucket != s.S3.Bucket) || (stmt.Format != "csv" && stmt.Format != "json") || stmt.Key == "" {
		return ErrInvalid
	}
	if !s.S3.DynamicKey && !ValidRelativeKey(stmt.Key, false) {
		return ErrInvalid
	}
	return nil
}

type S3 struct {
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	Bucket         string `json:"bucket"`
	Prefix         string `json:"prefix"`
	DynamicKey     bool   `json:"dynamic_key"`
	ForcePathStyle bool   `json:"force_path_style"`
}

func (s Scope) Validate() error {
	for _, value := range []slug.Slug{s.DataSource, s.Operation, s.Contract, s.Revision} {
		if _, err := slug.New(value.String()); err != nil {
			return ErrInvalid.With(err)
		}
	}
	if s.Revision == "latest" || s.ObjectID == "" || strings.ContainsAny(s.ObjectID, "\x00\r\n") || s.PolicyID != S3PolicyID || s.S3 == nil || s.Representation != model.ContractRepresentationRows {
		return ErrInvalid
	}
	target := s.S3
	if !fixed(target.Region) || !fixed(target.Bucket) || strings.ContainsAny(target.Bucket, "/\\%:@?#") || !ValidRelativeKey(target.Prefix, true) {
		return ErrInvalid
	}
	u, err := url.Parse(target.Endpoint)
	if err != nil || !fixed(target.Endpoint) || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ErrInvalid
	}
	return nil
}

func fixed(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "{}$") && !strings.ContainsFunc(value, unicode.IsControl)
}

// ValidRelativeKey deliberately rejects encoded/ambiguous paths. S3 keys are
// literal; this policy does not decode input or normalize away traversal.
func ValidRelativeKey(value string, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\\%:?#{}$") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
