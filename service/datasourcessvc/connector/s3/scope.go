package s3

import (
	"reflect"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var errScopedExecutionFailed = xerror.New(xerror.KindUnavailable, "SCOPED_EXECUTION_FAILED", "scoped object execution failed")

func (c *Connector) matchesScope(scope resourcescope.Scope) bool {
	s := scope.S3
	return s != nil && c.config.Endpoint == s.Endpoint && c.config.Region == s.Region && strings.TrimSpace(c.config.Bucket) == s.Bucket && strings.TrimSuffix(normalizePrefix(c.config.Prefix), "/") == s.Prefix && c.config.ForcePathStyle == s.ForcePathStyle
}

func validScopedSelector(scope resourcescope.Scope, original, resolved statement, key string) bool {
	if !resourcescope.ValidRelativeKey(resolved.Key, false) || !resourcescope.ValidRelativeKey(key, false) || firstNonEmpty(resolved.Bucket, scope.S3.Bucket) != scope.S3.Bucket {
		return false
	}
	if prefix := scope.S3.Prefix; prefix != "" && !strings.HasPrefix(key, prefix+"/") {
		return false
	}
	if !scope.S3.DynamicKey && original.Key != resolved.Key {
		return false
	}
	// Runtime sink overrides may supply content and metadata, but cannot change
	// parsing, write policy, or any fixed operation field.
	for _, stmt := range []*statement{&original, &resolved} {
		stmt.Key, stmt.Bucket, stmt.ContentType = "", "", ""
		stmt.Content, stmt.Metadata = nil, nil
		stmt.ContentSet = false
	}
	return reflect.DeepEqual(original, resolved)
}
