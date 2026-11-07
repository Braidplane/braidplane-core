package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const defaultTimeout = 5 * time.Second

// Client performs S3-compatible provider requests through the AWS SDK.
type Client struct {
	client     *s3.Client
	httpClient *http.Client
}

// New creates an S3 client from runtime configuration and write-only credentials.
func New(ctx context.Context, config Config, creds Credentials) (*Client, error) {
	if strings.TrimSpace(creds.AccessKeyID) == "" ||
		strings.TrimSpace(creds.SecretAccessKey) == "" {
		return nil, xerror.InvalidCredentials().Detail(
			"S3 access_key_id and secret_access_key are required",
		)
	}

	endpoint := strings.TrimSpace(config.Endpoint)
	policy := hostguard.NewDestinationPolicy(config.AllowPrivateHosts)
	if endpoint != "" {
		parsed, err := parseEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		if err := transportsecurity.ValidateURL(parsed, transportsecurity.PlaintextAllowed(ctx)); err != nil {
			return nil, err
		}
		if _, err := policy.Resolve(ctx, parsed.Hostname()); err != nil {
			return nil, xerror.Wrapf("connect to S3 endpoint: %w", err)
		}
	}

	timeout := defaultTimeout
	if config.TimeoutSeconds > 0 {
		timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}
	httpClient, err := newPinnedHTTPClient(timeout, policy)
	if err != nil {
		return nil, err
	}

	httpClient.Transport = transportsecurity.HTTPTransport(httpClient.Transport.(*http.Transport), transportsecurity.PlaintextAllowed(ctx))
	awsCfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(strings.TrimSpace(config.Region)),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(
			creds.AccessKeyID,
			creds.SecretAccessKey,
			creds.SessionToken,
		)),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, xerror.Wrapf("load S3 config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
		options.UsePathStyle = config.ForcePathStyle
	})

	return &Client{client: s3Client, httpClient: httpClient}, nil
}

// newPinnedHTTPClient keeps URL hostnames intact for TLS and SigV4 while forcing each socket
// through the shared destination policy, including every followed redirect target.
func newPinnedHTTPClient(timeout time.Duration, policy hostguard.DestinationPolicy) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, xerror.Unexpected().Detail("default HTTP transport has unexpected type")
	}

	clonedTransport := transport.Clone()
	clonedTransport.Proxy = nil
	clonedTransport.DialContext = policy.DialContext

	return &http.Client{
		Timeout:   timeout,
		Transport: clonedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := transportsecurity.CheckRedirect(req, via); err != nil {
				return err
			}
			if disabled, _ := req.Context().Value(redirectsDisabledKey{}).(bool); disabled {
				return xerror.ExecutionRedirectLimitExceeded()
			}
			if len(via) >= 5 {
				return xerror.ExecutionRedirectLimitExceeded()
			}
			if _, err := policy.Resolve(req.Context(), req.URL.Hostname()); err != nil {
				return xerror.Wrapf("validate S3 redirect destination: %w", err)
			}
			return nil
		},
	}, nil
}

type redirectsDisabledKey struct{}

// WithRedirectsDisabled prevents a scoped operation from switching resources
// through an otherwise public, network-policy-compliant redirect.
func WithRedirectsDisabled(ctx context.Context) context.Context {
	return context.WithValue(ctx, redirectsDisabledKey{}, true)
}

// Close releases idle HTTP connections held by the S3 client.
func (c *Client) Close() error {
	if c == nil || c.httpClient == nil {
		return nil
	}
	c.httpClient.CloseIdleConnections()
	return nil
}

// TestConnection checks only the configured prefix when credentials are prefix-restricted.
func (c *Client) TestConnection(ctx context.Context, req TestConnectionRequest) error {
	if req.Prefix != "" {
		_, err := c.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(req.Bucket), Prefix: aws.String(req.Prefix), MaxKeys: aws.Int32(1)})
		if err != nil {
			return mapProviderError(err)
		}
		return nil
	}
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(req.Bucket),
	})
	if err != nil {
		return mapProviderError(err)
	}

	return nil
}

// ListObjects lists object metadata under a bucket/prefix for schema introspection.
func (c *Client) ListObjects(ctx context.Context, req ListObjectsRequest) ([]Object, error) {
	paginator := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(req.Bucket),
		Prefix: aws.String(req.Prefix),
	})

	objects := make([]Object, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, mapProviderError(err)
		}
		for _, item := range page.Contents {
			if item.Key == nil {
				continue
			}
			key := aws.ToString(item.Key)
			if err := schemaquota.Charge(ctx, 1, len(key)); err != nil {
				return nil, err
			}

			var lastModified *time.Time
			if item.LastModified != nil {
				copied := *item.LastModified
				lastModified = &copied
			}

			objects = append(objects, Object{
				Key:          key,
				SizeBytes:    aws.ToInt64(item.Size),
				ETag:         strings.Trim(aws.ToString(item.ETag), `"`),
				LastModified: lastModified,
			})
		}
	}

	return objects, nil
}

// GetObject reads one S3 object body into memory within the supplied byte limit.
func (c *Client) GetObject(ctx context.Context, req GetObjectRequest) (*ObjectContent, error) {
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(req.Bucket),
		Key:    aws.String(req.Key),
	})
	if err != nil {
		return nil, mapProviderError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	content, err := readLimited(resp.Body, req.MaxBytes)
	if err != nil {
		return nil, err
	}

	return &ObjectContent{
		Key:         req.Key,
		Content:     content,
		ContentType: aws.ToString(resp.ContentType),
		Metadata:    cloneMetadata(resp.Metadata),
	}, nil
}

// HeadObject reports whether one S3 object exists without returning its body.
func (c *Client) HeadObject(ctx context.Context, req HeadObjectRequest) (bool, error) {
	_, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(req.Bucket),
		Key:    aws.String(req.Key),
	})
	if err == nil {
		return true, nil
	}
	if isProviderNotFound(err) {
		return false, nil
	}

	return false, mapProviderError(err)
}

// PutObject writes one S3 object body after optional overwrite protection.
func (c *Client) PutObject(ctx context.Context, req PutObjectRequest) (*PutObjectResult, error) {
	if !req.Overwrite {
		// S3-compatible PutObject calls in this first slice do not provide an
		// atomic no-overwrite guard, so this preflight can race with concurrent
		// writers that create the same key after HeadObject returns.
		exists, err := c.HeadObject(ctx, HeadObjectRequest{Bucket: req.Bucket, Key: req.Key})
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, xerror.AlreadyExists(xerror.DataSource).Detailf(
				"S3 object %q already exists",
				req.Key,
			)
		}
	}

	input := &s3.PutObjectInput{
		Bucket: aws.String(req.Bucket),
		Key:    aws.String(req.Key),
		Body:   bytes.NewReader(req.Content),
	}
	if strings.TrimSpace(req.ContentType) != "" {
		input.ContentType = aws.String(strings.TrimSpace(req.ContentType))
	}
	if len(req.Metadata) > 0 {
		input.Metadata = cloneMetadata(req.Metadata)
	}

	if _, err := c.client.PutObject(ctx, input); err != nil {
		return nil, mapProviderError(err)
	}

	return &PutObjectResult{WrittenBytes: len(req.Content)}, nil
}

// parseEndpoint validates the optional S3-compatible endpoint URL.
func parseEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, xerror.ValidationFailed().Detail("S3 endpoint must use http or https")
	}
	if parsed.Hostname() == "" {
		return nil, xerror.ValidationFailed().Detail("S3 endpoint must have a valid hostname")
	}

	return parsed, nil
}

// readLimited drains a provider response body while enforcing an execution byte limit.
func readLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return content, nil
	}

	content, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if int64(len(content)) > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"S3 object exceeds %d byte limit",
			maxBytes,
		)
	}

	return content, nil
}

// mapProviderError maps AWS SDK and provider errors into Braidplane domain errors.
func mapProviderError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, context.Canceled) || xerror.Is(err, context.DeadlineExceeded) {
		return xerror.Timeout(xerror.DataSource).With(err).WithPublicDetail("S3 request timed out or was cancelled; the write outcome may be unknown.")
	}
	var netErr net.Error
	if xerror.As(err, &netErr) && netErr.Timeout() {
		return xerror.Timeout(xerror.DataSource).With(err).WithPublicDetail("S3 request timed out or was cancelled; the write outcome may be unknown.")
	}

	var mapped *xerror.Error
	summary := "S3 request failed. No supported provider error code was available."
	var apiErr smithy.APIError
	if xerror.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "ExpiredToken":
			mapped = xerror.InvalidCredentials()
			summary = "S3 " + apiErr.ErrorCode() + ": credentials were rejected."
		case "AccessDenied", "Forbidden":
			mapped = xerror.Forbidden(xerror.DataSource)
			summary = "S3 " + apiErr.ErrorCode() + ": access to the requested operation was denied."
		case "NoSuchBucket":
			mapped = xerror.NotFound(xerror.DataSource)
			summary = "S3 NoSuchBucket: the target bucket was not found."
		case "NoSuchKey", "NotFound", "404":
			mapped = xerror.NotFound(xerror.DataSource)
			summary = "S3 " + apiErr.ErrorCode() + ": the target object was not found."
		case "RequestTimeout", "RequestTimeoutException":
			mapped = xerror.Timeout(xerror.DataSource)
			summary = "S3 " + apiErr.ErrorCode() + ": the request timed out; the write outcome may be unknown."
		case "SlowDown", "ServiceUnavailable", "InternalError":
			mapped = xerror.Unexpected()
			summary = "S3 " + apiErr.ErrorCode() + ": the provider could not complete the request; verify the write outcome before retrying."
		case "InvalidRequest", "InvalidArgument":
			mapped = xerror.Unexpected()
			summary = "S3 " + apiErr.ErrorCode() + ": the provider rejected the request settings."
		}
	}
	if mapped == nil {
		mapped = xerror.Unexpected()
	}
	var responseErr interface{ HTTPStatusCode() int }
	if xerror.As(err, &responseErr) {
		if status := responseErr.HTTPStatusCode(); status >= 100 && status <= 599 {
			summary += " HTTP status " + strconv.Itoa(status) + "."
		}
	}
	return mapped.With(err).WithPublicDetail(summary)
}

// isProviderNotFound reports whether a provider error represents a missing object.
func isProviderNotFound(err error) bool {
	var apiErr smithy.APIError
	if !xerror.As(err, &apiErr) {
		return false
	}

	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound", "404":
		return true
	default:
		return false
	}
}

// cloneMetadata copies S3 object metadata so callers cannot mutate provider-owned maps.
func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
