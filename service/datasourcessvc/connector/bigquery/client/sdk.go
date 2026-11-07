package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"google.golang.org/api/bigquery/v2"
	"google.golang.org/api/option"

	"github.com/Braidplane/braidplane-core/pkg/googleclient"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type requestPolicyKey struct{}

type requestPolicy struct {
	maxRequestBytes  int64
	maxResponseBytes int64
	body             []byte
}

func (c *Client) initSDK(ctx context.Context) error {
	httpClient := *c.client
	httpClient.Transport = &sdkTransport{client: c}
	service, err := bigquery.NewService(ctx, option.WithHTTPClient(&httpClient), option.WithEndpoint(c.baseURL+"/bigquery/v2/"), option.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		googleclient.LogError(ctx, err)
		return xerror.ExecutionFailed().Detail("Cannot initialize BigQuery client")
	}
	service.UserAgent = userAgent
	c.api = service
	return nil
}

func callSDK[T any](ctx context.Context, maxRequestBytes, maxResponseBytes int64, call func(context.Context) (*T, error)) (*T, []byte, error) {
	policy := &requestPolicy{maxRequestBytes: maxRequestBytes, maxResponseBytes: effectiveMaxResponseBytes(maxResponseBytes)}
	response, err := call(context.WithValue(ctx, requestPolicyKey{}, policy))
	if err != nil {
		googleclient.LogError(ctx, err)
		if ctx.Err() != nil {
			return nil, nil, googleclient.TransportError(ctx, err)
		}
		for _, safe := range []error{xerror.ExecutionRequestSizeExceeded(), xerror.ExecutionResponseSizeExceeded(), xerror.InvalidCredentials(), xerror.Forbidden(xerror.DataSource), xerror.InvalidInput(xerror.Query), xerror.Unavailable(xerror.DataSource), xerror.Timeout(xerror.DataSource)} {
			if xerror.Is(err, safe) {
				if xerror.PublicDetail(err) == streamingBillingPublicDetail {
					return nil, nil, xerror.Forbidden(xerror.DataSource).WithPublicDetail(streamingBillingPublicDetail)
				}
				return nil, nil, safe
			}
		}
		return nil, nil, xerror.Unexpected().Detail("Invalid Google provider response")
	}
	// The SDK decodes interface-valued cells as float64; preserve exact decimals in its response types.
	decoder := json.NewDecoder(bytes.NewReader(policy.body))
	decoder.UseNumber()
	if err := decoder.Decode(response); err != nil {
		googleclient.LogError(ctx, err)
		return nil, nil, xerror.Unexpected().Detail("Invalid Google provider JSON response")
	}
	return response, policy.body, nil
}

type sdkTransport struct{ client *Client }

func (t *sdkTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	policy, ok := req.Context().Value(requestPolicyKey{}).(*requestPolicy)
	if !ok {
		return nil, xerror.Unexpected().Detail("BigQuery request policy is required")
	}
	request := req.Clone(req.Context())
	// Remove SDK formatting defaults so Proof retains its exact URL allowlist.
	query := request.URL.Query()
	query.Del("alt")
	query.Del("prettyPrint")
	request.URL.RawQuery = query.Encode()
	if request.Body != nil && policy.maxRequestBytes > 0 {
		body, err := googleclient.ReadResponse(request.Body, policy.maxRequestBytes)
		_ = request.Body.Close()
		if err != nil {
			googleclient.LogError(req.Context(), err)
			if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
				return nil, xerror.ExecutionRequestSizeExceeded()
			}
			return nil, xerror.Unexpected().Detail("Invalid BigQuery request body")
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
	}
	token, err := t.client.accessToken(req.Context())
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	// The inner client preserves resource host/redirect restrictions, including caller-owned Proof transport.
	// #nosec G704 -- SDK endpoints are hostguarded; Proof's caller-owned transport enforces its exact allowlist.
	response, err := t.client.client.Do(request)
	if err != nil {
		googleclient.LogError(req.Context(), err, token)
		return nil, googleclient.TransportError(req.Context(), err)
	}
	body, err := readProviderResponse(req.Context(), response, policy.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	policy.body = body
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func readProviderResponse(ctx context.Context, response *http.Response, limit int64) ([]byte, error) {
	defer func() { _ = response.Body.Close() }()
	body, err := googleclient.ReadResponse(response.Body, limit)
	if err != nil {
		googleclient.LogError(ctx, err)
		if xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
			return nil, err
		}
		return nil, xerror.Unexpected().Detail("Invalid or interrupted Google provider response")
	}
	if err := mapProviderError(response, body); err != nil {
		return nil, err
	}
	return body, nil
}

type tokenTransport struct {
	ctx    context.Context
	client *http.Client
	err    error
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// #nosec G704 -- Token URLs are hostguarded; Proof's caller-owned transport restricts the fixed OAuth endpoint.
	response, err := t.client.Do(req.WithContext(t.ctx))
	if err != nil {
		googleclient.LogError(t.ctx, err)
		t.err = googleclient.TransportError(t.ctx, err)
		return nil, t.err
	}
	// Match the OAuth library's token budget, rejecting excess bytes before its truncating read.
	body, err := readProviderResponse(t.ctx, response, 1<<20)
	if err != nil {
		t.err = err
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
