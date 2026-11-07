// Package s3 provides an S3-compatible object-store connector for Braidplane.
package s3

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

var s3FileAdapter = remotefile.Adapter{
	ConnectorName:       "S3",
	ErrorKind:           remotefile.ErrorKindInvalidQuery,
	SpreadsheetFilePath: true,
	SchemaParsedColumns: true,
	LimitPolicy: remotefile.LimitPolicy{
		UseHTTPFallbackForRead:  true,
		UseHTTPFallbackForParse: true,
	},
}

// Connector adapts S3 provider object operations to Braidplane file connector contracts.
type Connector struct {
	config Config
	client providerClient
}

// New creates an S3 connector backed by the provider client.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	client, err := s3client.New(ctx, config, creds)
	if err != nil {
		return nil, err
	}

	return newWithClient(config, client), nil
}

// newWithClient creates a connector with an injected provider client for deterministic tests.
func newWithClient(config Config, client providerClient) *Connector {
	return &Connector{config: config, client: client}
}

// Close releases provider client resources.
func (c *Connector) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// TestConnection verifies that the configured S3 bucket is reachable.
func (c *Connector) TestConnection(ctx context.Context) error {
	return c.client.TestConnection(ctx, s3client.TestConnectionRequest{
		Prefix: normalizePrefix(c.config.Prefix),
		Bucket: strings.TrimSpace(c.config.Bucket),
	})
}

// IntrospectSchema maps S3 objects under the configured prefix into file schema metadata.
func (c *Connector) IntrospectSchema(ctx context.Context) (*filedata.Schema, error) {
	return s3FileAdapter.IntrospectSchema(ctx, s3SchemaProvider{connector: c}, remotefile.SchemaOptions{
		MaxBytes: introspectionMaxObjectBytes,
	})
}

// Execute runs an S3 object read or write operation and returns a file result envelope.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return c.execute(ctx, queryData, params, nil, limits...)
}

func (c *Connector) ExecuteScoped(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam, scope resourcescope.Scope, limits ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	return c.execute(ctx, queryData, params, &scope, limits...)
}

func (c *Connector) execute(ctx context.Context, queryData json.RawMessage, params map[string]types.TypedParam, scope *resourcescope.Scope, limits ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	start := time.Now()
	execLimits := types.ResolveExecutionLimits(limits...)
	if scope != nil && (scope.ValidateStatement(queryData) != nil || !c.matchesScope(*scope)) {
		return nil, resourcescope.ErrInvalid
	}

	normalized, err := types.NormalizeParams(params)
	if err != nil {
		if scope != nil {
			return nil, resourcescope.ErrInvalid
		}
		return result.NewS3Result(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("parameter normalization failed: %w", err)), nil
	}

	stmt, err := remotefile.DecodeStatement(queryData)
	if err != nil {
		return result.NewS3Result(nil, time.Since(start).Milliseconds(),
			xerror.InvalidInput(xerror.Query).Detailf("invalid S3 operation: %w", err)), nil
	}
	original := stmt

	operation := normalizeOperation(stmt.Operation)
	if writegate.HasGuard(ctx) && operation != operationWrite {
		return result.NewS3Result(nil, time.Since(start).Milliseconds(), xerror.InvalidInput(xerror.Query).WithPublicDetail("S3 sinks require a write operation")), nil
	}
	if operation == operationWrite {
		applyWriteParamOverrides(&stmt, normalized)
	}

	if err := substituteStatementParams(&stmt, normalized); err != nil {
		if scope != nil {
			return nil, resourcescope.ErrInvalid
		}
		return result.NewS3Result(nil, time.Since(start).Milliseconds(), err), nil
	}

	bucket := firstNonEmpty(stmt.Bucket, c.config.Bucket)
	if strings.TrimSpace(bucket) == "" {
		return result.NewS3Result(nil, time.Since(start).Milliseconds(),
			xerror.InvalidInput(xerror.Query).Detail("S3 bucket is required")), nil
	}
	if strings.TrimSpace(stmt.Key) == "" {
		return result.NewS3Result(nil, time.Since(start).Milliseconds(),
			xerror.InvalidInput(xerror.Query).Detail("S3 query data must specify a key")), nil
	}

	if scope != nil && !resourcescope.ValidRelativeKey(stmt.Key, false) {
		return nil, resourcescope.ErrInvalid
	}
	key, err := joinS3Key(c.config.Prefix, stmt.Key)
	if err != nil {
		return result.NewS3Result(nil, time.Since(start).Milliseconds(), err), nil
	}
	if scope != nil && !validScopedSelector(*scope, original, stmt, key) {
		return nil, resourcescope.ErrInvalid
	}
	if scope != nil {
		ctx = s3client.WithRedirectsDisabled(ctx)
	}

	var fileResult *result.FileResult
	switch operation {
	case operationRead:
		fileResult, err = c.executeRead(ctx, bucket, key, stmt, execLimits)
	case operationWrite:
		fileResult, err = c.executeWrite(ctx, bucket, key, stmt)
	default:
		err = xerror.InvalidInput(xerror.Query).Detailf("unsupported S3 operation %q", stmt.Operation)
	}
	if err != nil {
		if scope != nil {
			for _, limit := range []error{xerror.ExecutionRowLimitExceeded(), xerror.ExecutionResponseSizeExceeded()} {
				if xerror.Is(err, limit) {
					return nil, limit
				}
			}
			// Keep only the client's fixed public explanation; the cause can contain resolved keys or signed URLs.
			return nil, errScopedExecutionFailed.WithPublicDetail(xerror.PublicDetail(err))
		}
		return result.NewS3Result(nil, time.Since(start).Milliseconds(), err), nil
	}
	if scope != nil && fileResult != nil {
		fileResult.TargetPath, fileResult.Name, fileResult.ResourceID = "", "", ""
		fileResult.File = nil
	}

	return result.NewS3Result(fileResult, time.Since(start).Milliseconds(), nil), nil
}

// executeRead fetches an object and normalizes it according to the requested file format.
func (c *Connector) executeRead(
	ctx context.Context,
	bucket,
	key string,
	stmt statement,
	limits types.ExecutionLimits,
) (*result.FileResult, error) {
	return s3FileAdapter.ExecuteRead(ctx, s3ObjectProvider{
		client: c.client,
		bucket: bucket,
	}, key, stmt, limits)
}

// executeWrite serializes the input payload and stores it as an S3 object.
func (c *Connector) executeWrite(
	ctx context.Context,
	bucket,
	key string,
	stmt statement,
) (*result.FileResult, error) {
	return s3FileAdapter.ExecuteWrite(ctx, s3ObjectProvider{
		client: c.client,
		bucket: bucket,
	}, key, stmt)
}

// applyWriteParamOverrides lets runtime sink bindings override saved write statement fields.
func applyWriteParamOverrides(stmt *statement, params map[string]any) {
	if stmt == nil || len(params) == 0 {
		return
	}

	remotefile.ApplyWriteParamOverrides(
		stmt,
		params,
		remotefile.WriteOverrideOptions{TargetParamName: "key"},
	)

	if bucket, ok := params["bucket"].(string); ok && strings.TrimSpace(bucket) != "" {
		stmt.Bucket = bucket
	}
	if stmt.ContentType == "" {
		if contentType, ok := params["content_type"].(string); ok {
			stmt.ContentType = contentType
		}
	}
	if len(stmt.Metadata) == 0 {
		stmt.Metadata = metadataOverride(params)
	}
}

// metadataOverride decodes safe object metadata supplied by pipeline sink config.
func metadataOverride(params map[string]any) map[string]string {
	raw, exists := params["metadata"]
	if !exists {
		return nil
	}
	if typed, ok := raw.(map[string]string); ok {
		return cloneStringMap(typed)
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	metadata := make(map[string]string, len(values))
	for key, value := range values {
		stringValue, ok := value.(string)
		if !ok {
			return nil
		}
		metadata[key] = stringValue
	}

	return metadata
}

// cloneStringMap copies metadata so runtime parameter maps are not retained.
func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}

	return cloned
}

// substituteStatementParams resolves supported string placeholders before provider calls.
func substituteStatementParams(stmt *statement, params map[string]any) error {
	if stmt == nil {
		return nil
	}

	if err := remotefile.SubstituteCommonParams(stmt, params, "key"); err != nil {
		return err
	}
	if err := remotefile.SubstituteStringField("bucket", &stmt.Bucket, params); err != nil {
		return err
	}
	if err := remotefile.SubstituteStringField("content_type", &stmt.ContentType, params); err != nil {
		return err
	}

	return remotefile.SubstituteMetadata(stmt.Metadata, params)
}

// joinS3Key resolves relative operation keys under the configured data-source prefix.
func joinS3Key(prefix, key string) (string, error) {
	cleanPrefix := normalizePrefix(prefix)
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return "", xerror.InvalidInput(xerror.Query).Detail("S3 key is required")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", xerror.InvalidInput(xerror.Query).Detail("absolute S3 keys are not allowed")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", xerror.InvalidInput(xerror.Query).Detail("S3 key traversal is not allowed")
		}
	}

	joined := path.Clean(path.Join(cleanPrefix, trimmed))
	if joined == "." {
		return "", xerror.InvalidInput(xerror.Query).Detail("S3 key is required")
	}
	if cleanPrefix == "" {
		return joined, nil
	}
	if joined != strings.TrimSuffix(cleanPrefix, "/") &&
		!strings.HasPrefix(joined, cleanPrefix) {
		return "", xerror.InvalidInput(xerror.Query).Detail("S3 key escapes configured prefix")
	}

	return joined, nil
}

type s3ObjectProvider struct {
	client providerClient
	bucket string
}

// ReadFile fetches S3 object bytes through the provider client boundary.
func (provider s3ObjectProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (*remotefile.ReadResult, error) {
	content, err := provider.client.GetObject(ctx, s3client.GetObjectRequest{
		Bucket:   provider.bucket,
		Key:      target,
		MaxBytes: maxBytes,
	})
	if err != nil {
		return nil, err
	}

	return &remotefile.ReadResult{
		Content:     content.Content,
		ContentType: content.ContentType,
	}, nil
}

// WriteFile stores S3 object bytes through the provider client boundary.
func (provider s3ObjectProvider) WriteFile(
	ctx context.Context,
	req remotefile.WriteRequest,
) (*remotefile.WriteResult, error) {
	if err := writegate.BeforeWrite(ctx); err != nil {
		return nil, err
	}
	writeResult, err := provider.client.PutObject(ctx, s3client.PutObjectRequest{
		Bucket:      provider.bucket,
		Key:         req.Target,
		Content:     req.Content,
		ContentType: req.ContentType,
		Metadata:    req.Metadata,
		Overwrite:   req.Overwrite,
	})
	if err != nil {
		return nil, err
	}

	return &remotefile.WriteResult{WrittenBytes: writeResult.WrittenBytes}, nil
}

type s3SchemaProvider struct {
	connector *Connector
}

// ListFiles maps S3 object listings into remote-file schema candidates.
func (provider s3SchemaProvider) ListFiles(ctx context.Context) ([]remotefile.SchemaFile, error) {
	objects, err := provider.connector.client.ListObjects(ctx, s3client.ListObjectsRequest{
		Bucket: strings.TrimSpace(provider.connector.config.Bucket),
		Prefix: normalizePrefix(provider.connector.config.Prefix),
	})
	if err != nil {
		return nil, err
	}

	files := make([]remotefile.SchemaFile, 0, len(objects))
	for _, object := range objects {
		if object.Key == "" {
			continue
		}
		files = append(files, remotefile.SchemaFile{Name: object.Key, Target: object.Key, SizeBytes: &object.SizeBytes})
	}

	return files, nil
}

// ReadFile samples S3 object bytes for shared schema inference.
func (provider s3SchemaProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (*remotefile.ReadResult, error) {
	return s3ObjectProvider{
		client: provider.connector.client,
		bucket: strings.TrimSpace(provider.connector.config.Bucket),
	}.ReadFile(ctx, target, maxBytes)
}

// normalizePrefix prepares a configured S3 prefix for safe key joining and listing.
func normalizePrefix(prefix string) string {
	trimmed := strings.Trim(strings.TrimSpace(prefix), "/")
	if trimmed == "" {
		return ""
	}
	return trimmed + "/"
}

// normalizeOperation normalizes external operation text before dispatch.
func normalizeOperation(operation string) string {
	return remotefile.OperationOrDefault(operation)
}

// firstNonEmpty returns the first non-empty string after trimming whitespace.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
