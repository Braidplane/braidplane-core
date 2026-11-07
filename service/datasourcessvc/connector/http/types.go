package http

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// HTTPSchemaIntrospector defines the interface for HTTP connector schema introspection.
type HTTPSchemaIntrospector interface {
	IntrospectSchema(ctx context.Context) (*HTTPSchema, error)
}

// HTTPSchema represents the extracted schema from an OpenAPI specification.
type HTTPSchema struct {
	Operations []Operation `json:"operations"`
}

// Operation represents a single HTTP operation (endpoint) from an OpenAPI spec.
type Operation struct {
	OperationID string          `json:"operation_id"`           // Unique identifier (e.g., "listUsers")
	Path        string          `json:"path"`                   // API path (e.g., "/users")
	Method      string          `json:"method"`                 // HTTP method (GET, POST, PUT, DELETE, PATCH)
	Description string          `json:"description"`            // Human-readable description
	Parameters  json.RawMessage `json:"parameters,omitempty"`   // Parameter schema (JSON)
	RequestBody json.RawMessage `json:"request_body,omitempty"` // Request body schema (JSON)
	Responses   json.RawMessage `json:"responses,omitempty"`    // Response schemas (JSON)
	Draft       json.RawMessage `json:"draft,omitempty"`
	Diagnostics []string        `json:"diagnostics,omitempty"`
}

// OpenAPIFormat represents the format of an OpenAPI specification file.
type OpenAPIFormat string

const (
	OpenAPIFormatJSON OpenAPIFormat = "json"
	OpenAPIFormatYAML OpenAPIFormat = "yaml"
)

// formatFromURL detects the OpenAPI format from a URL by examining the file extension.
// Supports .json, .yaml, and .yml extensions (case-insensitive).
// Returns an error if the extension is missing or unsupported.
func formatFromURL(url string) (OpenAPIFormat, error) {
	// Remove query parameters
	if idx := strings.Index(url, "?"); idx != -1 {
		url = url[:idx]
	}

	// Extract file extension
	ext := filepath.Ext(url)
	if ext == "" {
		return "", xerror.InvalidSchemaURL().Detail("URL has no file extension")
	}

	// Normalize to lowercase and remove the dot
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))

	switch ext {
	case "json":
		return OpenAPIFormatJSON, nil
	case "yaml", "yml":
		return OpenAPIFormatYAML, nil
	default:
		return "", xerror.InvalidSchemaURL().With(
			xerror.Unexpected().Detailf("unsupported file extension: .%s", ext),
		)
	}
}
