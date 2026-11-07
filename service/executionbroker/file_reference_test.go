package executionbroker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

func referenceFixture() filepayload.Reference {
	return filepayload.Reference{ID: uuid.New().String(), SizeBytes: 2 << 30, SHA256: strings.Repeat("a", 64)}
}

func referenceMetadata(ref filepayload.Reference) *filepayload.FilePayloadMetadata {
	return &filepayload.FilePayloadMetadata{Filename: "fixture.bin", MIMEType: "application/octet-stream", SizeBytes: ref.SizeBytes, DigestAlgorithm: filepayload.FilePayloadDigestSHA256, Digest: ref.SHA256}
}

func TestRetainedFileRequestRoundTrip(t *testing.T) {
	t.Parallel()
	ref := referenceFixture()
	request := queriessvc.ConnectorExecutionRequest{Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 4096, MaxFileBytes: 3 << 30}, Parameters: map[string]connectortypes.TypedParam{"input": {Value: ref, DataType: "text"}}}
	raw, err := marshalRequestPlaintext(kindQuery, request)
	require.NoError(t, err)
	require.Less(t, len(raw), 4096)
	decoded, err := unmarshalConnectorExecutionRequest(raw)
	require.NoError(t, err)
	require.Equal(t, ref, decoded.Parameters["input"].Value)
	request.Limits.MaxFileBytes = ref.SizeBytes - 1
	_, err = marshalRequestPlaintext(kindQuery, request)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	var envelope connectorExecutionRequestEnvelope
	require.NoError(t, json.Unmarshal(raw, &envelope))
	envelope.RuntimeParameterBytes = map[string][]byte{"input": {1}}
	mixed, err := json.Marshal(envelope)
	require.NoError(t, err)
	_, err = unmarshalConnectorExecutionRequest(mixed)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
}

func TestRetainedFileResponseRoundTripAndPrivacy(t *testing.T) {
	t.Parallel()
	ref := referenceFixture()
	for _, kind := range []string{"ftp", "git", "s3", "google_workspace", "http"} {
		t.Run(kind, func(t *testing.T) {
			var execution *result.ExecutionResult
			switch kind {
			case "http":
				execution = result.NewHTTPResult(&result.HTTPResult{File: referenceMetadata(ref), Reference: &ref}, 0, nil)
			case "ftp":
				execution = result.NewFTPResult(&result.FileResult{Format: "file", File: referenceMetadata(ref), Reference: &ref}, 0, nil)
			case "git":
				execution = result.NewGitResult(&result.FileResult{Format: "file", File: referenceMetadata(ref), Reference: &ref}, 0, nil)
			case "google_workspace":
				execution = result.NewWorkspaceFileResult(&result.FileResult{Format: "file", File: referenceMetadata(ref), Reference: &ref}, 0, nil)
			case "s3":
				execution = result.NewS3Result(&result.FileResult{Format: "file", File: referenceMetadata(ref), Reference: &ref}, 0, nil)
			}
			public, err := json.Marshal(execution)
			require.NoError(t, err)
			require.NotContains(t, string(public), ref.ID)
			raw, err := marshalResponsePlaintext(encryptedResponse{Result: execution, resultRuntimeMaxBytes: 4096, resultRuntimeMaxFileBytes: 3 << 30})
			require.NoError(t, err)
			require.Less(t, len(raw), 4096)
			restored, err := unmarshalResponsePlaintext(raw, 4096)
			require.NoError(t, err)
			if kind == "http" {
				output, ok := restored.Result.HTTP()
				require.True(t, ok)
				require.Equal(t, &ref, output.Reference)
				require.Empty(t, output.RawBody)
			} else {
				output, ok := restored.Result.File()
				require.True(t, ok)
				require.Equal(t, &ref, output.Reference)
				require.Empty(t, output.Content)
			}
			_, err = marshalResponsePlaintext(encryptedResponse{Result: execution, resultRuntimeMaxBytes: 4096, resultRuntimeMaxFileBytes: ref.SizeBytes - 1})
			require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
			clone := execution.Clone()
			if kind == "http" {
				output, _ := clone.HTTP()
				output.Reference.SizeBytes--
			} else {
				output, _ := clone.File()
				output.Reference.SizeBytes--
			}
			require.Equal(t, int64(2<<30), ref.SizeBytes)
		})
	}
}

func TestRetainedFileResponseRejectsAmbiguousOrMismatchedPayload(t *testing.T) {
	t.Parallel()
	ref := referenceFixture()
	mixed := &executionResultRuntimePayload{FileReference: &ref, FileContent: []byte{1}}
	require.ErrorIs(t, validateResultRuntimePayloadSize(mixed, 4096), filestream.ErrUnavailable)
	metadata := referenceMetadata(ref)
	metadata.SizeBytes--
	response := encryptedResponse{Result: result.NewS3Result(&result.FileResult{File: metadata}, 0, nil), ResultRuntime: &executionResultRuntimePayload{FileReference: &ref}}
	require.ErrorIs(t, response.restoreResultRuntimePayload(), filestream.ErrUnavailable)
}

func TestBrokerReadbackAppliesOriginalFileAllowance(t *testing.T) {
	t.Parallel()
	ref := referenceFixture()
	cipher, err := crypto.NewAES256(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	raw, err := marshalResponsePlaintext(encryptedResponse{Result: result.NewS3Result(&result.FileResult{Format: "file", File: referenceMetadata(ref), Reference: &ref}, 0, nil), resultRuntimeMaxBytes: 4096, resultRuntimeMaxFileBytes: 3 << 30})
	require.NoError(t, err)
	encryptedResult, err := cipher.Encrypt(raw)
	require.NoError(t, err)
	repo := &fakeRequestRepo{}
	broker := &Broker{cipher: cipher, txm: fakeTXM{}, repo: repo}
	for _, limit := range []int64{ref.SizeBytes - 1, ref.SizeBytes, ref.SizeBytes + 1} {
		request, err := marshalRequestPlaintext(kindQuery, queriessvc.ConnectorExecutionRequest{Limits: connectortypes.ExecutionLimits{MaxResponseBytes: 4096, MaxFileBytes: limit}})
		require.NoError(t, err)
		encryptedRequest, err := cipher.Encrypt(request)
		require.NoError(t, err)
		repo.request = requestrepo.Request{Kind: kindQuery, State: requestrepo.StateSucceeded, RequestCiphertext: encryptedRequest, ResultCiphertext: encryptedResult}
		response, terminal, err := broker.readResponse(t.Context(), uuid.New(), "fixture")
		require.True(t, terminal)
		if limit < ref.SizeBytes {
			require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
			continue
		}
		require.NoError(t, err)
		output, ok := response.Result.File()
		require.True(t, ok)
		require.Equal(t, &ref, output.Reference)
	}
}
