package filedata

import (
	"path"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// BuildFileResult returns a file execution result with safe metadata and copied bytes.
func BuildFileResult(content []byte, targetPath, operation string) *result.FileResult {
	return BuildFileResultWithMIMEType(content, targetPath, operation, "")
}

// BuildFileResultWithMIMEType returns a file result using a provider media type fallback.
func BuildFileResultWithMIMEType(
	content []byte,
	targetPath,
	operation,
	mimeType string,
) *result.FileResult {
	metadata := filepayload.NewFilePayloadMetadata(
		path.Base(targetPath),
		MIMETypeForPath(targetPath, mimeType),
		content,
	)

	return &result.FileResult{
		Format:     QueryFormatFile.String(),
		Operation:  operation,
		TargetPath: targetPath,
		File:       &metadata,
		Content:    append([]byte(nil), content...),
	}
}

// EncodeFileContent converts native file sink content into bytes for raw writes.
func EncodeFileContent(content any) ([]byte, error) {
	switch typed := content.(type) {
	case []byte:
		return append([]byte(nil), typed...), nil
	case string:
		return []byte(typed), nil
	default:
		return nil, xerror.Unexpected().Detailf(
			"file write content must be bytes or string, got %T",
			content,
		)
	}
}
