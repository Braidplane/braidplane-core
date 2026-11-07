package connector

import (
	"bytes"
	"encoding/json"
	"io"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type customerPostgresConfig struct {
	Host              string  `json:"host"`
	Port              int     `json:"port"`
	Database          string  `json:"database"`
	SSLMode           *string `json:"ssl_mode,omitempty"`
	ConnectTimeout    *int    `json:"connect_timeout,omitempty"`
	AllowPrivateHosts *bool   `json:"allow_private_hosts,omitempty"`
}

type customerMySQLConfig struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Database          string `json:"database"`
	ConnectTimeout    *int   `json:"connect_timeout,omitempty"`
	AllowPrivateHosts *bool  `json:"allow_private_hosts,omitempty"`
}

type customerBigQueryConfig struct {
	ProjectID          string  `json:"project_id"`
	Location           *string `json:"location,omitempty"`
	DefaultDataset     *string `json:"default_dataset,omitempty"`
	TimeoutSeconds     *int    `json:"timeout_seconds,omitempty"`
	MaximumBytesBilled *int64  `json:"maximum_bytes_billed,omitempty"`
}

type customerSnowflakeConfig struct {
	WIFObjects     []snowflakewif.ObjectGrant `json:"wif_objects,omitempty"`
	Account        string                     `json:"account"`
	Warehouse      string                     `json:"warehouse"`
	Database       string                     `json:"database"`
	Schema         string                     `json:"schema"`
	Role           *string                    `json:"role,omitempty"`
	ConnectTimeout *int                       `json:"connect_timeout,omitempty" validate:"omitempty,min=1,max=300"`
}

type customerHTTPConfig struct {
	BaseURL                    string  `json:"base_url"`
	HealthPath                 *string `json:"health_path,omitempty"`
	TimeoutSeconds             *int    `json:"timeout_seconds,omitempty"`
	OpenAPIURL                 *string `json:"openapi_url,omitempty"`
	OpenAPIFetchTimeoutSeconds *int    `json:"openapi_fetch_timeout_seconds,omitempty"`
	AllowPrivateHosts          *bool   `json:"allow_private_hosts,omitempty"`
}

type customerHubSpotConfig struct {
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}

type customerMongoDBConfig struct {
	Host              string  `json:"host"`
	Port              int     `json:"port"`
	Database          string  `json:"database"`
	AuthDatabase      *string `json:"auth_database,omitempty"`
	ConnectTimeout    *int    `json:"connect_timeout,omitempty"`
	AllowPrivateHosts *bool   `json:"allow_private_hosts,omitempty"`
}

type customerFTPConfig struct {
	Host              string  `json:"host"`
	Port              int     `json:"port"`
	BasePath          *string `json:"base_path,omitempty"`
	ConnectTimeout    *int    `json:"connect_timeout,omitempty"`
	AllowPrivateHosts *bool   `json:"allow_private_hosts,omitempty"`
}

type customerGitConfig struct {
	RepoURL           string  `json:"repo_url"`
	DefaultBranch     *string `json:"default_branch,omitempty"`
	BasePath          *string `json:"base_path,omitempty"`
	AllowPrivateHosts *bool   `json:"allow_private_hosts,omitempty"`
}

type customerS3Config struct {
	Endpoint          *string `json:"endpoint,omitempty"`
	Region            string  `json:"region"`
	Bucket            string  `json:"bucket"`
	Prefix            *string `json:"prefix,omitempty"`
	ForcePathStyle    *bool   `json:"force_path_style,omitempty"`
	TimeoutSeconds    *int    `json:"timeout_seconds,omitempty"`
	AllowPrivateHosts *bool   `json:"allow_private_hosts,omitempty"`
}

// NormalizeCustomerConfig enforces the public data-source config contract before
// customer payloads are validated, tested, or persisted.
func NormalizeCustomerConfig(dsType model.DataSourceType, raw []byte) ([]byte, bool, error) {
	switch dsType {
	case model.DataSourceTypeGoogleWorkspace:
		normalized, err := normalizeCustomerConfigPayload[workspace.Config](raw)
		return normalized, true, err
	case model.DataSourceTypePostgres:
		normalized, err := normalizeCustomerConfigPayload[customerPostgresConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeMySQL:
		normalized, err := normalizeCustomerConfigPayload[customerMySQLConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeBigQuery:
		normalized, err := normalizeCustomerConfigPayload[customerBigQueryConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeSnowflake:
		normalized, err := normalizeCustomerConfigPayload[customerSnowflakeConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeHTTP:
		normalized, err := normalizeCustomerConfigPayload[customerHTTPConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeMongoDB:
		normalized, err := normalizeCustomerConfigPayload[customerMongoDBConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeHubSpot:
		normalized, err := normalizeCustomerConfigPayload[customerHubSpotConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeFTP:
		normalized, err := normalizeCustomerConfigPayload[customerFTPConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeGit, model.DataSourceTypeGithub, model.DataSourceTypeGitlab:
		normalized, err := normalizeCustomerConfigPayload[customerGitConfig](raw)
		return normalized, true, err
	case model.DataSourceTypeS3:
		normalized, err := normalizeCustomerConfigPayload[customerS3Config](raw)
		return normalized, true, err
	default:
		return nil, false, nil
	}
}

// normalizeCustomerConfigPayload decodes one public config shape with unknown
// fields rejected, then marshals the canonical public JSON representation.
func normalizeCustomerConfigPayload[T any](raw []byte) ([]byte, error) {
	var payload T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			return nil, xerror.ValidationFailed().With(err)
		}
		return nil, xerror.ValidationFailed().Detail("invalid trailing config JSON")
	}

	if err := validate.Default().Struct(payload); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return normalized, nil
}
