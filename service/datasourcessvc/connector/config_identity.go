package connector

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrUnsafeConfigIdentity = xerror.New(xerror.KindInvalidInput, "DATA_SOURCE_CONFIG_IDENTITY_UNAVAILABLE", "data source configuration cannot be pinned safely")

// PublicConfigDigest hashes only the public connector contract. URL credentials
// and late substitutions cannot become provenance, even through a hash.
func PublicConfigDigest(kind model.DataSourceType, raw []byte) (string, error) {
	canonical, supported, err := NormalizeCustomerConfig(kind, raw)
	if err != nil || !supported {
		return "", ErrUnsafeConfigIdentity
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil {
		return "", ErrUnsafeConfigIdentity
	}
	for name, value := range fields {
		if strings.Contains(string(value), "{{") || strings.Contains(string(value), "${") {
			return "", ErrUnsafeConfigIdentity
		}
		if name != "base_url" && name != "repo_url" && name != "endpoint" && name != "openapi_url" {
			continue
		}
		var address string
		if string(value) == "null" {
			continue
		}
		if json.Unmarshal(value, &address) != nil {
			return "", ErrUnsafeConfigIdentity
		}
		if kind == model.DataSourceTypeGit && name == "repo_url" && strings.HasPrefix(address, "git@") {
			host, path, ok := strings.Cut(strings.TrimPrefix(address, "git@"), ":")
			if !ok || host == "" || path == "" {
				return "", ErrUnsafeConfigIdentity
			}
			address = "ssh://git@" + host + "/" + path
		}
		u, err := url.Parse(address)
		if err != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", ErrUnsafeConfigIdentity
		}
		if u.User != nil {
			// Git's fixed SSH username is public; its key stays in the credential store.
			_, password := u.User.Password()
			if kind != model.DataSourceTypeGit || name != "repo_url" || u.Scheme != "ssh" || u.User.Username() != "git" || password || u.Hostname() == "" || strings.Trim(u.Path, "/") == "" {
				return "", ErrUnsafeConfigIdentity
			}
		}
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(append([]byte(kind.String()+"\n"), canonical...))), nil
}
