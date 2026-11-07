package s3_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const (
	s3FixtureRegion          = "us-east-1"
	s3FixtureAccessKeyID     = "braidplane"
	s3FixtureSecretAccessKey = "braidplanepass"
)

// Shares one SeaweedFS fixture service across connector E2E tests.
func TestConnector_TestConnectionAndIntrospectSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	requireS3Reachable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bucket := uniqueBucketName("schema")
	provider := newProviderClient(t, ctx)
	createBucket(t, ctx, provider, bucket)
	putObject(t, ctx, provider, bucket, "fixtures/customers.csv", "id,name\n1,Ada\n")

	conn, err := s3connector.New(transportsecurity.WithPlaintext(ctx), testConfig(bucket, "fixtures"), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, conn.TestConnection(ctx))

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.Len(t, schema.Files, 1)
	assert.Equal(t, "fixtures/customers.csv", schema.Files[0].Name)
	require.Len(t, schema.Files[0].Fields, 2)
	assert.Equal(t, "id", schema.Files[0].Fields[0].Name)
	assert.Equal(t, "name", schema.Files[0].Fields[1].Name)
}

// Shares one SeaweedFS fixture service across connector E2E tests.
func TestConnector_ExecuteReadAndWriteObject(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	requireS3Reachable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bucket := uniqueBucketName("execute")
	provider := newProviderClient(t, ctx)
	createBucket(t, ctx, provider, bucket)

	conn, err := s3connector.New(transportsecurity.WithPlaintext(ctx), testConfig(bucket, "exports"), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	targetKey := "runs/{{runtime.run.slug}}/customers.json"
	writeResult, err := executeS3(ctx, conn, map[string]any{
		"type":      "s3",
		"operation": "write",
		"key":       targetKey,
		"format":    "json",
		"content": []map[string]any{
			{"id": "1", "name": "Ada"},
		},
		"metadata": map[string]string{
			"run": "{{runtime.run.slug}}",
		},
	}, map[string]any{
		"runtime.run.slug": "s3-e2e",
	})
	require.NoError(t, err)
	require.Equal(t, "exports/runs/s3-e2e/customers.json", writeResult.TargetPath)
	require.NotNil(t, writeResult.WrittenRows)
	require.Equal(t, 1, *writeResult.WrittenRows)

	head, err := provider.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(writeResult.TargetPath),
	})
	require.NoError(t, err)
	assert.Equal(t, "s3-e2e", head.Metadata["run"])
	assert.Equal(t, "application/json", aws.ToString(head.ContentType))

	_, err = executeS3(ctx, conn, map[string]any{
		"type":      "s3",
		"operation": "write",
		"key":       "runs/s3-e2e/customers.json",
		"format":    "json",
		"overwrite": false,
		"content":   []map[string]any{{"id": "2", "name": "Grace"}},
	}, map[string]any{})
	require.ErrorIs(t, err, xerror.AlreadyExists(xerror.DataSource))

	readResult, err := executeS3(ctx, conn, map[string]any{
		"type":      "s3",
		"operation": "read",
		"key":       "runs/s3-e2e/customers.json",
		"format":    "json",
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 1, readResult.RowCount)
	require.Equal(t, "Ada", readResult.Rows[0]["name"])
}

// Shares one SeaweedFS fixture service across connector E2E tests.
func TestConnector_TestConnectionRejectsProviderAuthFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	requireS3Reachable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bucket := uniqueBucketName("invalid-creds")
	provider := newProviderClient(t, ctx)
	createBucket(t, ctx, provider, bucket)

	conn, err := s3connector.New(transportsecurity.WithPlaintext(ctx), testConfig(bucket, ""), s3connector.Credentials{
		AccessKeyID:     "wrong-access-key",
		SecretAccessKey: "wrong-secret",
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	err = conn.TestConnection(ctx)

	require.ErrorIs(t, err, xerror.Forbidden(xerror.DataSource))
}

// Shares one SeaweedFS fixture service across connector E2E tests.
func TestConnector_TestConnectionReportsMissingBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	requireS3Reachable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := s3connector.New(transportsecurity.WithPlaintext(ctx), testConfig(uniqueBucketName("missing"), ""), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	err = conn.TestConnection(ctx)

	require.ErrorIs(t, err, xerror.NotFound(xerror.DataSource))
}

// Shares one SeaweedFS fixture service across connector E2E tests.
func TestConnector_IntrospectMissingPrefixReturnsEmptySchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	requireS3Reachable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bucket := uniqueBucketName("missing-prefix")
	provider := newProviderClient(t, ctx)
	createBucket(t, ctx, provider, bucket)

	conn, err := s3connector.New(transportsecurity.WithPlaintext(ctx), testConfig(bucket, "not-present"), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	schema, err := conn.IntrospectSchema(ctx)

	require.NoError(t, err)
	require.Empty(t, schema.Files)
}

func testConfig(bucket, prefix string) s3connector.Config {
	return s3connector.Config{
		Endpoint:          depsports.S3ConnectorEndpoint(),
		Region:            s3FixtureRegion,
		Bucket:            bucket,
		Prefix:            prefix,
		ForcePathStyle:    true,
		TimeoutSeconds:    5,
		AllowPrivateHosts: true,
	}
}

func testCredentials() s3connector.Credentials {
	return s3connector.Credentials{
		AccessKeyID:     s3FixtureAccessKeyID,
		SecretAccessKey: s3FixtureSecretAccessKey,
	}
}

func executeS3(
	ctx context.Context,
	conn *s3connector.Connector,
	query map[string]any,
	params map[string]any,
) (*result.FileResult, error) {
	queryData, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	typedParams := make(map[string]types.TypedParam, len(params))
	for name, value := range params {
		typedParams[name] = types.TypedParam{Value: value, DataType: "text"}
	}

	execResult, err := conn.Execute(ctx, queryData, typedParams)
	if err != nil {
		return nil, err
	}
	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	fileResult, ok := execResult.S3()
	if !ok {
		return nil, xerror.Unexpected().Detailf("expected s3 execution result, got: %s", execResult.Type())
	}
	return fileResult, nil
}

func newProviderClient(t *testing.T, ctx context.Context) *awss3.Client {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(s3FixtureRegion),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(
			s3FixtureAccessKeyID,
			s3FixtureSecretAccessKey,
			"",
		)),
	)
	require.NoError(t, err)

	return awss3.NewFromConfig(cfg, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(depsports.S3ConnectorEndpoint())
		options.UsePathStyle = true
	})
}

func createBucket(t *testing.T, ctx context.Context, client *awss3.Client, bucket string) {
	t.Helper()

	_, err := client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err == nil || isProviderCode(err, "BucketAlreadyOwnedByYou", "BucketAlreadyExists") {
		return
	}

	require.NoError(t, err)
}

func putObject(
	t *testing.T,
	ctx context.Context,
	client *awss3.Client,
	bucket,
	key,
	content string,
) {
	t.Helper()

	_, err := client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader([]byte(content)),
	})
	require.NoError(t, err)
}

func uniqueBucketName(suffix string) string {
	return fmt.Sprintf("braidplane-s3-%s-%d", suffix, time.Now().UnixNano())
}

func isProviderCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !xerror.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

func requireS3Reachable(t *testing.T) {
	t.Helper()

	require.Truef(
		t,
		isReachable("127.0.0.1", depsports.S3ConnectorPort()),
		"S3 connector fixture is not reachable at %s",
		depsports.S3ConnectorEndpoint(),
	)
}

func isReachable(host string, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
