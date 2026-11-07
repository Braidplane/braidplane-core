package mongo

import "github.com/Braidplane/braidplane-core/pkg/xerror"

// ErrSchemaIntrospection indicates that schema introspection failed.
var ErrSchemaIntrospection = xerror.Unavailable(xerror.SchemaMetadata)
