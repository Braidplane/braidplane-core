package client

import "time"

// Config holds S3-compatible connector runtime configuration.
type Config struct {
	Endpoint          string `json:"endpoint,omitempty"            validate:"omitempty,url"`
	Region            string `json:"region"                        validate:"required,min=1"`
	Bucket            string `json:"bucket"                        validate:"required,min=1"`
	Prefix            string `json:"prefix,omitempty"`
	ForcePathStyle    bool   `json:"force_path_style,omitempty"`
	TimeoutSeconds    int    `json:"timeout_seconds,omitempty"    validate:"omitempty,min=1,max=300"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds write-only S3 access key credentials.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"               validate:"required,min=1"`
	SecretAccessKey string `json:"secret_access_key"           validate:"required,min=1"`
	SessionToken    string `json:"session_token,omitempty"`
}

// TestConnectionRequest identifies the bucket checked by connection tests.
type TestConnectionRequest struct {
	Prefix string
	Bucket string
}

// ListObjectsRequest identifies a bucket and prefix to introspect.
type ListObjectsRequest struct {
	Bucket string
	Prefix string
}

// Object is S3 object metadata needed for Braidplane schema introspection.
type Object struct {
	Key          string
	SizeBytes    int64
	ETag         string
	LastModified *time.Time
}

// GetObjectRequest identifies one S3 object read and its runtime byte guardrail.
type GetObjectRequest struct {
	Bucket   string
	Key      string
	MaxBytes int64
}

// ObjectContent is an in-memory S3 object body plus safe provider metadata.
type ObjectContent struct {
	Key         string
	Content     []byte
	ContentType string
	Metadata    map[string]string
}

// HeadObjectRequest identifies one S3 object metadata lookup.
type HeadObjectRequest struct {
	Bucket string
	Key    string
}

// PutObjectRequest describes one S3 object write after Braidplane binding.
type PutObjectRequest struct {
	Bucket      string
	Key         string
	Content     []byte
	ContentType string
	Metadata    map[string]string
	Overwrite   bool
}

// PutObjectResult reports the number of bytes accepted for an S3 write.
type PutObjectResult struct {
	WrittenBytes int
}
