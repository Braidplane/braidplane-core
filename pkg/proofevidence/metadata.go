package proofevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// MetadataObserver is trusted connector code constructed from protected resource
// configuration. Implementations must use only the supplied transport for I/O and
// return sanitized metadata, counting every field against the row limit.
// It is not an extension point for job-supplied code or request plans.
type MetadataObserver interface {
	Plan() (MetadataPlan, error)
	ReadMetadata(context.Context, MetadataIO, []byte, Limits) (json.RawMessage, int64, Outcome)
}

// MetadataIO confines each protocol to admitted I/O. Native observers charge their
// fixed metadata operation before dialing; socket bytes include authentication.
type MetadataIO interface {
	HTTPClient() *http.Client
	BeginNative(MetadataRoute) error
	DialContext(context.Context, string, string) (net.Conn, error)
}

// MetadataPlan binds an exact resource and ordered request sequence to admission.
// Resource contains connector-owned identity JSON and must never enter logs.
type MetadataPlan struct {
	MaxConnections int                  `json:"max_connections"`
	ConnectorType  model.DataSourceType `json:"connector_type"`
	Operation      string               `json:"operation"`
	Resource       json.RawMessage      `json:"resource"`
	Target         Endpoint             `json:"target"`
	Egress         []Endpoint           `json:"egress"`
	Routes         []MetadataRoute      `json:"routes"`
}

type MetadataRoute struct{ Method, URL string }

func (p MetadataPlan) Fingerprint() (string, error) {
	if p.ConnectorType == model.DataSourceTypeUnknown || strings.TrimSpace(p.Operation) == "" || !json.Valid(p.Resource) ||
		string(p.Resource) == "null" || len(p.Routes) == 0 || validateEndpoint(p.Target) != nil || !slices.Contains(p.Egress, p.Target) {
		return "", ErrAuthorizationDenied
	}
	for _, endpoint := range p.Egress {
		if validateEndpoint(endpoint) != nil {
			return "", ErrAuthorizationDenied
		}
	}
	if p.Target.Scheme != "https" {
		if (p.ConnectorType != model.DataSourceTypePostgres || p.Target.Scheme != "postgres") &&
			(p.ConnectorType != model.DataSourceTypeMySQL || p.Target.Scheme != "mysql") &&
			(p.ConnectorType != model.DataSourceTypeMongoDB || p.Target.Scheme != "mongodb") ||
			p.MaxConnections < 1 || p.MaxConnections > 2 || len(p.Egress) != 1 || len(p.Routes) != 1 || p.Routes[0].Method != "metadata" ||
			p.Operation != p.Routes[0].URL {
			return "", ErrAuthorizationDenied
		}
	} else {
		if p.MaxConnections != 0 {
			return "", ErrAuthorizationDenied
		}
		for _, route := range p.Routes {
			u, err := url.Parse(route.URL)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
				(route.Method != http.MethodGet && route.Method != http.MethodPost && route.Method != http.MethodHead) {
				return "", ErrAuthorizationDenied
			}
			port := u.Port()
			if port != "" && port != "443" {
				return "", ErrAuthorizationDenied
			}
			if !slices.Contains(p.Egress, Endpoint{Scheme: u.Scheme, Host: u.Hostname(), Port: 443}) {
				return "", ErrAuthorizationDenied
			}
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return "", ErrAuthorizationDenied
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}
