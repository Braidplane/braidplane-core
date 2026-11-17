package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// MatchObjectRequest describes the prepared output, never a replacement write.
type MatchObjectRequest struct {
	Bucket, Key, SHA256, ContentType string
	SizeBytes                        int64
	Metadata                         map[string]string
}

// MatchesObject confirms the complete prepared bytes and configured metadata with bounded memory.
func (c *Client) MatchesObject(ctx context.Context, req MatchObjectRequest) (bool, error) {
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(req.Bucket), Key: aws.String(req.Key)})
	if err != nil {
		return false, mapProviderError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	expectedMetadata := make(map[string]string, len(req.Metadata))
	for key, value := range req.Metadata {
		key = strings.ToLower(key)
		if previous, exists := expectedMetadata[key]; exists && previous != value {
			return false, nil
		}
		expectedMetadata[key] = value
	}
	if req.SizeBytes < 0 || (resp.ContentLength != nil && *resp.ContentLength != req.SizeBytes) || !maps.Equal(expectedMetadata, resp.Metadata) || (req.ContentType != "" && strings.TrimSpace(req.ContentType) != aws.ToString(resp.ContentType)) {
		return false, nil
	}
	// Copy's positive limit also handles empty objects without accepting a non-empty body.
	metadata, err := filestream.Copy(ctx, io.Discard, resp.Body, max(req.SizeBytes, int64(1)))
	if err != nil {
		return false, err
	}
	return metadata.SizeBytes == req.SizeBytes && metadata.SHA256 == req.SHA256, nil
}

// ContentDigest identifies the exact serialized bytes that a prepared write would dispatch.
func ContentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
