package client

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/encoding/httpbinding"
)

// ReadObjectMetadata uses the admitted HTTP client and static credentials only.
// No environment credential discovery, redirects, retries or bucket listing is authorized.
func ReadObjectMetadata(ctx context.Context, httpClient *http.Client, config Config, creds Credentials, request HeadObjectRequest) (bool, error) {
	if httpClient == nil || strings.TrimSpace(creds.AccessKeyID) == "" || strings.TrimSpace(creds.SecretAccessKey) == "" {
		return false, xerror.InvalidCredentials()
	}
	sdk := s3.NewFromConfig(aws.Config{Region: config.Region, Credentials: awscredentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken), HTTPClient: httpClient}, func(options *s3.Options) {
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
		options.UsePathStyle = true
		options.RetryMaxAttempts = 1
	})
	return (&Client{client: sdk, httpClient: httpClient}).HeadObject(ctx, request)
}

// ObjectMetadataURL uses the SDK's partition and path rules without provider I/O.
func ObjectMetadataURL(region, endpoint, bucket, key string) (*url.URL, error) {
	parameters := s3.EndpointParameters{Region: aws.String(region), Bucket: aws.String(bucket), ForcePathStyle: aws.Bool(true)}
	if endpoint != "" {
		parameters.Endpoint = aws.String(endpoint)
	}
	resolved, err := s3.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(), parameters)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).With(err).Detail("invalid object metadata endpoint")
	}
	address := resolved.URI
	address.RawPath = strings.TrimRight(address.EscapedPath(), "/") + "/" + httpbinding.EscapePath(key, false)
	address.Path = strings.TrimRight(address.Path, "/") + "/" + key
	return &address, nil
}
