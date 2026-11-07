package s3

import (
	"context"

	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
)

const (
	operationRead  = remotefile.OperationRead
	operationWrite = remotefile.OperationWrite
)

const introspectionMaxObjectBytes = 1024 * 1024

// Config holds S3 connector configuration used by the Braidplane adapter.
type Config = s3client.Config

// Credentials holds write-only S3 access key credentials.
type Credentials = s3client.Credentials

// SchemaIntrospector retrieves S3 object schema metadata.
type SchemaIntrospector interface {
	IntrospectSchema(ctx context.Context) (*filedata.Schema, error)
}

type statement = remotefile.Statement

type providerClient interface {
	TestConnection(context.Context, s3client.TestConnectionRequest) error
	ListObjects(context.Context, s3client.ListObjectsRequest) ([]s3client.Object, error)
	GetObject(context.Context, s3client.GetObjectRequest) (*s3client.ObjectContent, error)
	HeadObject(context.Context, s3client.HeadObjectRequest) (bool, error)
	PutObject(context.Context, s3client.PutObjectRequest) (*s3client.PutObjectResult, error)
	Close() error
}
