package http

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
)

// ValidateCredentials keeps the service and runtime authentication boundary consistent.
func ValidateCredentials(creds Credentials) error {
	if (creds.APIKey == "") != (creds.APIKeyHeader == "") {
		return ErrInvalidCredentials.Detail("api_key and api_key_header must be provided together")
	}
	if creds.APIKeyHeader != "" && creds.APIKeyHeader != "X-Api-Key" && creds.APIKeyHeader != "Authorization" {
		return ErrInvalidCredentials.Detail("api_key_header must be X-Api-Key or Authorization")
	}
	if strings.ContainsAny(creds.APIKey, "\r\n") {
		return ErrInvalidCredentials.Detail("api_key must not contain line breaks")
	}
	if len(creds.SecretHeaders) > 8 {
		return ErrInvalidCredentials.Detail("secret_headers supports at most 8 headers")
	}
	seen := make(map[string]bool, len(creds.SecretHeaders))
	for name, value := range creds.SecretHeaders {
		lower := strings.ToLower(name)
		if len(name) > 128 || !httpguts.ValidHeaderFieldName(name) || seen[lower] || reservedSecretHeader(lower) {
			return ErrInvalidCredentials.Detail("secret_headers requires unique, non-reserved HTTP header names")
		}
		if strings.TrimSpace(value) == "" || len(value) > 4096 || !httpguts.ValidHeaderFieldValue(value) {
			return ErrInvalidCredentials.Detail("secret_headers values must contain 1 to 4096 valid header bytes")
		}
		seen[lower] = true
	}
	methods := 0
	for _, present := range []bool{len(creds.SecretHeaders) > 0, creds.APIKey != "", creds.BearerToken != "", creds.Username != "" || creds.Password != ""} {
		if present {
			methods++
		}
	}
	if methods > 1 {
		return ErrInvalidCredentials.Detail("configure only one HTTP authentication method")
	}
	if (creds.Username == "") != (creds.Password == "") {
		return ErrInvalidCredentials.Detail("username and password must be provided together")
	}
	return nil
}

func reservedSecretHeader(name string) bool {
	switch name {
	case "proxy-authorization", "cookie", "set-cookie", "host", "connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "accept", "accept-encoding", "user-agent", "expect":
		return true
	default:
		return strings.HasPrefix(name, "content-") || strings.HasPrefix(name, "proxy-") || strings.HasPrefix(name, "sec-")
	}
}

func (c *Connector) authenticate(req *http.Request) {
	for name, value := range c.secretHeaders {
		req.Header.Set(name, value)
	}
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	if c.apiKey != "" {
		value := c.apiKey
		if c.apiKeyHeader == "Authorization" {
			value = "ApiKey " + value
		}
		req.Header.Set(c.apiKeyHeader, value)
	}
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (c *Connector) openAPIHeaders() http.Header {
	headers := make(http.Header)
	if c.oauthDestination != "" && httpoauth.ValidateDestination(c.oauthDestination, c.config.OpenAPIURL) != nil {
		return headers
	}
	base, baseErr := url.Parse(c.config.BaseURL)
	spec, specErr := url.Parse(c.config.OpenAPIURL)
	if baseErr == nil && specErr == nil && sameOrigin(base, spec) {
		req := &http.Request{Header: headers}
		c.authenticate(req)
	}
	return headers
}

// A provider must not echo credential material into persisted execution output.
func (c *Connector) responseExposesSecret(headers http.Header, body []byte) bool {
	secrets := c.credentialSecrets()
	for _, secret := range secrets {
		for _, values := range headers {
			for _, value := range values {
				if containsCredentialText(value, secret) {
					return true
				}
			}
		}
		if bytes.Contains(body, []byte(secret)) {
			return true
		}
	}
	if len(secrets) == 0 || !json.Valid(body) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		// Inspect every string, including duplicate keys and escaped characters.
		value := ""
		switch scalar := token.(type) {
		case string:
			value = scalar
		case json.Number:
			if number, err := scalar.Float64(); err == nil {
				if encoded, encodeErr := json.Marshal(number); encodeErr == nil {
					value = string(encoded)
				}
			}
		}
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
}

func (c *Connector) credentialSecrets() []string {
	secrets := make([]string, 0, len(c.secretHeaders)+4)
	for _, value := range c.secretHeaders {
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	for _, value := range []string{c.bearerToken, c.apiKey, c.password} {
		if value != "" {
			secrets = append(secrets, value)
		}
	}
	if c.username != "" {
		secrets = append(secrets, base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password)))
	}
	return secrets
}

func containsCredentialText(value, secret string) bool {
	if strings.Contains(value, secret) {
		return true
	}
	// JSON serialization replaces each invalid UTF-8 byte with a replacement rune.
	return !utf8.ValidString(value) && strings.Contains(string([]rune(value)), secret)
}

func (c *Connector) openAPIResponseExposesSecret(body []byte) bool {
	if c.responseExposesSecret(nil, body) {
		return true
	}
	secrets := c.credentialSecrets()
	if len(secrets) == 0 || json.Valid(body) {
		return false
	}
	type nodeContext struct {
		node       *yaml.Node
		mappingKey bool
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			return false
		}
		seen := map[nodeContext]bool{}
		pending := []nodeContext{{node: &document}}
		for len(pending) > 0 {
			current := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[current] {
				continue
			}
			seen[current] = true
			node := current.node
			value := node.Value
			if node.Kind == yaml.ScalarNode {
				var scalar any
				if err := node.Decode(&scalar); err == nil {
					if text, ok := scalar.(string); ok {
						value = text
					} else if number, ok := scalar.(float64); ok && current.mappingKey {
						// YAML floating-point keys use the loader's spelling, not JSON's.
						value = fmt.Sprint(number)
					} else if encoded, encodeErr := json.Marshal(scalar); encodeErr == nil {
						value = string(encoded)
					}
				}
			}
			for _, secret := range secrets {
				if containsCredentialText(value, secret) {
					return true
				}
			}
			for index, child := range node.Content {
				pending = append(pending, nodeContext{
					node:       child,
					mappingKey: node.Kind == yaml.MappingNode && index%2 == 0,
				})
			}
			if node.Alias != nil {
				pending = append(pending, nodeContext{node: node.Alias, mappingKey: current.mappingKey})
			}
		}
	}
}
