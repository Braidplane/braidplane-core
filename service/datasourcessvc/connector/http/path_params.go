package http

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
)

func substitutePath(template string, values map[string]any) (string, error) {
	names := param.ExtractParamNames(template)
	replacements := make([]string, 0, len(names)*2)
	for _, name := range names {
		value, ok := values[name]
		if !ok || value == nil {
			return "", xerror.Unexpected().Detail("HTTP path parameter is required")
		}
		text := fmt.Sprint(value)
		if text == "." || text == ".." {
			return "", xerror.Unexpected().Detail("HTTP path parameters cannot be dot segments")
		}
		replacements = append(replacements, "{{"+name+"}}", url.PathEscape(text))
	}
	// One pass prevents parameter values from becoming new template expressions.
	return strings.NewReplacer(replacements...).Replace(template), nil
}
