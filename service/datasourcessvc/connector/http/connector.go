package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

var (
	// ErrInvalidConfig indicates HTTP connector configuration is invalid.
	ErrInvalidConfig = xerror.InvalidInput(xerror.DataSource)
	// ErrInvalidCredentials indicates HTTP connector credentials are invalid.
	ErrInvalidCredentials = xerror.InvalidInput(xerror.DataSource)
)

const (
	maxExecuteResponseSizeBytes = 50 * 1024 * 1024
	httpContentTypeJSON         = "application/json"
	httpContentTypeFormEncoded  = "application/x-www-form-urlencoded"
	httpContentTypeMultipart    = "multipart/form-data"
)

// Config holds configuration for HTTP data source connectivity checks and schema introspection.
type Config struct {
	BaseURL                    string `json:"base_url"                                validate:"required,url"`
	HealthPath                 string `json:"health_path,omitempty"`
	TimeoutSeconds             int    `json:"timeout_seconds,omitempty"               validate:"omitempty,min=1,max=300"`
	OpenAPIURL                 string `json:"openapi_url,omitempty"                   validate:"omitempty,url"`
	OpenAPIFetchTimeoutSeconds int    `json:"openapi_fetch_timeout_seconds,omitempty" validate:"omitempty,min=1,max=60"`
	AllowPrivateHosts          bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds authentication settings for HTTP endpoints.
type Credentials struct {
	// OAuthDestination is an internal runtime binding emitted only by credentialssvc.
	OAuthDestination string            `json:"oauth_destination,omitempty"`
	BearerToken      string            `json:"bearer_token,omitempty"`
	Username         string            `json:"username,omitempty"`
	Password         string            `json:"password,omitempty"`
	APIKey           string            `json:"api_key,omitempty"`
	APIKeyHeader     string            `json:"api_key_header,omitempty"`
	SecretHeaders    map[string]string `json:"secret_headers,omitempty"`
}

// Connector validates HTTP connectivity by sending a request to a configured endpoint.
type Connector struct {
	allowPlaintext   bool
	config           Config
	client           *http.Client
	requestURL       string
	bearerToken      string
	username         string
	password         string
	oauthDestination string
	apiKey           string
	apiKeyHeader     string
	secretHeaders    map[string]string
}

// New creates a new HTTP connector.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	if config.BaseURL == "" {
		return nil, ErrInvalidConfig.Detail("base_url is required")
	}

	parsedBaseURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, ErrInvalidConfig.With(err)
	}

	if parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https" {
		return nil, ErrInvalidConfig.Detail("base_url must use http or https scheme")
	}
	if parsedBaseURL.User != nil || parsedBaseURL.Hostname() == "" {
		return nil, ErrInvalidConfig.Detail("base_url requires a hostname and must not contain user information")
	}
	if err := ValidateCredentials(creds); err != nil {
		return nil, err
	}
	if creds.OAuthDestination != "" {
		if creds.BearerToken == "" || creds.APIKey != "" || creds.APIKeyHeader != "" || creds.Username != "" || creds.Password != "" || len(creds.SecretHeaders) > 0 {
			return nil, ErrInvalidCredentials.Detail("HTTP OAuth requires only a bearer token")
		}
		if err := httpoauth.ValidateDestination(creds.OAuthDestination, config.BaseURL); err != nil {
			return nil, ErrInvalidConfig.With(err)
		}
	}
	if err := transportsecurity.ValidateURL(parsedBaseURL, transportsecurity.PlaintextAllowed(ctx)); err != nil {
		return nil, err
	}
	policy := hostguard.NewDestinationPolicy(config.AllowPrivateHosts)
	if _, err := policy.Resolve(ctx, parsedBaseURL.Hostname()); err != nil {
		return nil, xerror.Wrapf(
			"%w: base_url blocked by ssrf protection: %w",
			ErrInvalidConfig,
			err,
		)
	}

	healthPath := config.HealthPath
	if healthPath == "" {
		healthPath = "/"
	}

	requestURL := strings.TrimRight(config.BaseURL, "/") + "/" + strings.TrimLeft(healthPath, "/")

	timeout := 5 * time.Second
	if config.TimeoutSeconds > 0 {
		timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, ErrInvalidConfig.Detail("default transport has unexpected type")
	}
	clonedTransport := defaultTransport.Clone()
	clonedTransport.Proxy = nil
	clonedTransport.DialContext = policy.DialContext

	authenticated := creds.APIKey != "" || creds.BearerToken != "" || creds.Username != "" || len(creds.SecretHeaders) > 0
	return &Connector{
		config:         config,
		allowPlaintext: transportsecurity.PlaintextAllowed(ctx),
		client: &http.Client{
			Timeout:   timeout,
			Transport: transportsecurity.HTTPTransport(clonedTransport, transportsecurity.PlaintextAllowed(ctx)),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := transportsecurity.CheckRedirect(req, via); err != nil {
					return err
				}
				if creds.OAuthDestination != "" {
					if err := httpoauth.ValidateDestination(creds.OAuthDestination, req.URL.String()); err != nil {
						return ErrInvalidConfig.With(err)
					}
				}
				if authenticated && len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
					return ErrInvalidConfig.Detail("authenticated redirects must remain on the original origin")
				}
				if _, err := policy.Resolve(req.Context(), req.URL.Hostname()); err != nil {
					return xerror.Wrapf("validate HTTP redirect destination: %w", err)
				}
				return nil
			},
		},
		requestURL:       requestURL,
		bearerToken:      creds.BearerToken,
		username:         creds.Username,
		password:         creds.Password,
		oauthDestination: creds.OAuthDestination,
		apiKey:           creds.APIKey,
		apiKeyHeader:     creds.APIKeyHeader,
		secretHeaders:    maps.Clone(creds.SecretHeaders),
	}, nil
}

// TestConnection performs the configured HTTP request and validates response status.
func (c *Connector) TestConnection(ctx context.Context) error {
	if c.oauthDestination != "" {
		if err := httpoauth.ValidateDestination(c.oauthDestination, c.requestURL); err != nil {
			return ErrInvalidConfig.With(err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.requestURL, nil)
	if err != nil {
		return xerror.ConnectionFailed(xerror.DataSource).With(err)
	}

	c.authenticate(req)

	resp, err := doWithLimits(c.client, ctx, req, types.ExecutionLimits{}, false)
	if err != nil {
		return xerror.ConnectionFailed(xerror.DataSource).With(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return mapHealthStatus(resp.StatusCode)
	}

	return nil
}

func mapHealthStatus(statusCode int) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials().WithPublicDetail(
			fmt.Sprintf("Authentication failed (HTTP status %d). Check credentials and try again.", statusCode),
		)
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).WithPublicDetail(
			fmt.Sprintf("Permission denied (HTTP status %d). Check the connection permissions.", statusCode),
		)
	case http.StatusNotFound:
		return xerror.NotFound(xerror.DataSource).WithPublicDetail(
			fmt.Sprintf("Endpoint not found (HTTP status %d). Check the health endpoint.", statusCode),
		)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return xerror.Timeout(xerror.DataSource).WithPublicDetail(
			fmt.Sprintf("Connection timed out (HTTP status %d). Try again.", statusCode),
		)
	case http.StatusTooManyRequests:
		return xerror.Unavailable(xerror.DataSource).WithPublicDetail(
			fmt.Sprintf("Rate limited (HTTP status %d). Wait and try again.", statusCode),
		)
	default:
		if statusCode >= http.StatusInternalServerError {
			return xerror.Unavailable(xerror.DataSource).WithPublicDetail(
				fmt.Sprintf("Server error (HTTP status %d). Try again later.", statusCode),
			)
		}
		return xerror.ConnectionFailed(xerror.DataSource).WithPublicDetail(
			fmt.Sprintf("Health endpoint returned HTTP status %d.", statusCode),
		)
	}
}

// Close releases connector resources.
func (c *Connector) Close() error {
	return nil
}

// httpQueryData is the query definition stored in a query's Data field for HTTP data sources.
type httpQueryData struct {
	Method          string               `json:"method"`
	Path            string               `json:"path"`
	QueryParams     map[string]any       `json:"query_params,omitempty"`
	Headers         map[string]any       `json:"headers,omitempty"`
	Body            any                  `json:"body,omitempty"`
	Pagination      *httpPagination      `json:"pagination,omitempty"`
	Acknowledgement *httpAcknowledgement `json:"acknowledgement,omitempty"`
	WriteRetry      *httpWriteRetry      `json:"write_retry,omitempty"`
}

// Execute executes an HTTP request described by queryData with the provided parameters.
//
// queryData must be a JSON object with at minimum "method" and "path" fields.
// params are injected into the query_params, headers, or body structure as specified in queryData.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(queryData, &raw)
	if pagination, present := raw["pagination"]; present {
		var definition httpQueryData
		if err := json.Unmarshal(queryData, &definition); err != nil {
			return result.NewHTTPResult(nil, 0, invalidPagination("HTTP pagination must be a valid object").With(err)), nil
		}
		if definition.Pagination == nil {
			return result.NewHTTPResult(nil, 0, invalidPagination("HTTP pagination must be a valid object")), nil
		}
		decoder := json.NewDecoder(bytes.NewReader(pagination))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(definition.Pagination); err != nil {
			return result.NewHTTPResult(nil, 0, invalidPagination("HTTP pagination contains unsupported fields").With(err)), nil
		}
		return c.executePaginated(ctx, queryData, definition, params, types.ResolveExecutionLimits(limits...))
	}
	return c.executeSingle(ctx, queryData, params, nil, limits...)
}

func (c *Connector) executeSingle(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	cursor *paginationCursor,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	start := time.Now()
	execLimits := types.ResolveExecutionLimits(limits...)
	timeout := execLimits.HTTPTimeout
	if timeout <= 0 {
		timeout = execLimits.ConnectorTimeout
	}
	if timeout <= 0 {
		timeout = c.client.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Normalize typed parameters for HTTP formatting (e.g., integers without decimals)
	normalized, err := normalizeParams(params)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Wrapf("parameter normalization failed: %w", err),
		), nil
	}

	var qd httpQueryData
	if err := json.Unmarshal(queryData, &qd); err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().Detailf("invalid HTTP query data: %w", err),
		), nil
	}

	var rawQuery map[string]json.RawMessage
	_ = json.Unmarshal(queryData, &rawQuery)
	_, bodySet := rawQuery["body"]
	if raw, present := rawQuery["acknowledgement"]; present {
		acknowledgement, err := decodeAcknowledgement(raw)
		if err != nil {
			return result.NewHTTPResult(nil, 0, err), nil
		}
		qd.Acknowledgement = acknowledgement
	}

	if qd.Method == "" {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().Detail("HTTP query data must specify a method"),
		), nil
	}

	if qd.Path == "" {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().Detail("HTTP query data must specify a path"),
		), nil
	}

	resolvedPath, err := substitutePath(qd.Path, normalized)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Wrapf("failed to substitute path parameters: %w", err),
		), nil
	}

	// Build the target URL.
	targetURL := strings.TrimRight(
		c.config.BaseURL,
		"/",
	) + "/" + strings.TrimLeft(
		resolvedPath,
		"/",
	)

	resolvedQueryParams, err := param.SubstituteInMap(qd.QueryParams, normalized)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Wrapf("failed to substitute query parameters: %w", err),
		), nil
	}

	for name := range qd.Headers {
		for secretName := range c.secretHeaders {
			if strings.EqualFold(name, secretName) {
				return result.NewHTTPResult(nil, 0, ErrInvalidCredentials.Detail("operation headers conflict with credential headers")), nil
			}
		}
	}

	resolvedHeaders, err := param.SubstituteInMap(qd.Headers, normalized)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Wrapf("failed to substitute header parameters: %w", err),
		), nil
	}

	var resolvedBody any
	if bodySet {
		if qd.Body == nil {
			resolvedBody = nil
		} else {
			resolvedBody, err = param.SubstituteValue(qd.Body, normalized)
			if err != nil {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Wrapf("failed to substitute body parameters: %w", err),
				), nil
			}
		}
	}

	if cursor != nil && cursor.name != "" {
		if resolvedQueryParams == nil {
			resolvedQueryParams = map[string]any{}
		}
		resolvedQueryParams[cursor.name] = cursor.value
	}

	// Build query string from resolved query parameters.
	queryString := url.Values{}

	for k, v := range resolvedQueryParams {
		if v == nil {
			continue
		}
		queryString.Set(k, fmt.Sprintf("%v", v))
	}

	if len(queryString) > 0 {
		targetURL += "?" + queryString.Encode()
	}
	if cursor != nil && cursor.targetURL != "" {
		targetURL = cursor.targetURL
	}

	// Build request body if the method supports it.
	var bodyBytes []byte
	contentType := ""
	forceContentType := false
	requestedContentType := headerValueIgnoreCase(resolvedHeaders, "Content-Type")
	inputRepresentation := sinkInputRepresentation(normalized)

	if bodySet ||
		(qd.Method == http.MethodPost || qd.Method == http.MethodPut || qd.Method == http.MethodPatch) {
		switch {
		case isNativeFileInputBodyTemplate(qd.Body, normalized):
			fileBody, ok := resolvedBody.([]byte)
			if !ok {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Unexpected().Detail("native file body must resolve to byte content"),
				), nil
			}

			bodyBytes = append([]byte(nil), fileBody...)
			contentType = nativeFileInputContentType(normalized)
		case isNativeCSVInputBodyTemplate(qd.Body, normalized):
			csvBody, ok := resolvedBody.(string)
			if !ok {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Unexpected().Detail("native CSV body must resolve to string content"),
				), nil
			}

			bodyBytes = []byte(csvBody)
			contentType = "text/csv"
		case shouldRejectMultipartFormContentType(
			resolvedBody,
			requestedContentType,
			inputRepresentation,
		):
			durationMs := time.Since(start).Milliseconds()
			return result.NewHTTPResult(
				nil,
				durationMs,
				xerror.Unexpected().Detail(
					"multipart_form HTTP body requires a form content type",
				),
			), nil
		case shouldSendMultipartBody(resolvedBody, requestedContentType, inputRepresentation):
			boundary := ""
			if qd.WriteRetry != nil {
				// Recovery must reproduce encoded bytes, including multipart boundaries.
				boundary = fmt.Sprintf("bp-%x", sha256.Sum256([]byte(fmt.Sprint(normalized[sinkbinding.InternalIdempotencyKey]))))
			}
			multipartBody, multipartContentType, err := encodeMultipartBody(resolvedBody, boundary)
			if err != nil {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(nil, durationMs, err), nil
			}

			bodyBytes = multipartBody
			contentType = multipartContentType
			forceContentType = true
		case shouldSendFormEncodedBody(resolvedBody, requestedContentType):
			formBody, err := encodeFormBody(resolvedBody)
			if err != nil {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(nil, durationMs, err), nil
			}

			bodyBytes = []byte(formBody)
			contentType = requestedContentType
		case shouldSendRawStringBody(resolvedBody, requestedContentType):
			stringBody, ok := resolvedBody.(string)
			if !ok {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Unexpected().Detail("raw string body must resolve to string content"),
				), nil
			}

			bodyBytes = []byte(stringBody)
			contentType = requestedContentType
		case bodySet && resolvedBody == nil && requestedContentType != "" && !isJSONContentType(requestedContentType):
			durationMs := time.Since(start).Milliseconds()
			return result.NewHTTPResult(
				nil,
				durationMs,
				xerror.Unexpected().Detail("null HTTP body requires a JSON content type"),
			), nil
		case !bodySet && requestedContentType != "" && !isJSONContentType(requestedContentType):
			durationMs := time.Since(start).Milliseconds()
			return result.NewHTTPResult(
				nil,
				durationMs,
				xerror.Unexpected().Detail(
					"non-JSON HTTP content types require an explicit compatible body",
				),
			), nil
		default:
			bodyData := resolvedBody
			if bodyData == nil && !bodySet {
				bodyData = make(map[string]any)
			}

			bodyBytes, err = json.Marshal(bodyData)
			if err != nil {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Unexpected().With(err),
				), nil
			}

			contentType = httpContentTypeJSON
		}
	}

	if execLimits.HTTPMaxRequestBytes > 0 && int64(len(bodyBytes)) > execLimits.HTTPMaxRequestBytes {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.ExecutionRequestSizeExceeded().Detailf(
				"request body exceeds %d byte limit",
				execLimits.HTTPMaxRequestBytes,
			),
		), nil
	}

	var reqBody io.Reader
	if bodyBytes != nil {
		reqBody = bytes.NewReader(bodyBytes)
	}

	if c.oauthDestination != "" {
		if err := httpoauth.ValidateDestination(c.oauthDestination, targetURL); err != nil {
			return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), ErrInvalidConfig.With(err)), nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, qd.Method, targetURL, reqBody)
	if err != nil {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().With(err),
		), nil
	}

	req.Header.Set("Accept", "application/json")

	// Inject extra headers from queryData.
	for k, v := range resolvedHeaders {
		req.Header.Set(k, fmt.Sprintf("%v", v))
	}

	if bodyBytes != nil && (req.Header.Get("Content-Type") == "" || forceContentType) {
		req.Header.Set("Content-Type", contentType)
	}

	c.authenticate(req)

	writeRetry := false
	if raw, present := rawQuery["write_retry"]; present {
		expires, err := validateWriteRetry(ctx, raw, qd, normalized, req)
		if err != nil {
			return result.NewHTTPResult(nil, 0, err), nil
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, expires)
		defer cancel()
		req = req.WithContext(ctx)
		writeRetry = true
	}
	resp, err := doWithLimits(c.client, ctx, req, execLimits, writeRetry)
	if err != nil {
		if (qd.Acknowledgement != nil || writeRetry) && !isIdempotentHTTPMethod(qd.Method) {
			return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), xerror.HTTPAcknowledgementUncertain().With(err).WithPublicDetail("HTTP write acknowledgement was not received. Verify remote state before retrying.")), nil
		}
		durationMs := time.Since(start).Milliseconds()
		if xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()) {
			return result.NewHTTPResult(nil, durationMs, err), nil
		}
		var xe *xerror.Error
		if xerror.As(err, &xe) {
			return result.NewHTTPResult(nil, durationMs, err), nil
		}
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().With(err),
		), nil
	}
	defer func() { _ = resp.Body.Close() }()
	if writeRetry && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP keyed write retry attempts exhausted; delivery remains unconfirmed. Verify provider state before recovery.")), nil
		}
		return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), xerror.HTTPAcknowledgementRejected().WithPublicDetail("HTTP keyed write was rejected by the provider; no automatic retry. Verify provider state before recovery.")), nil
	}
	if cursor != nil && cursor.name == "" && resp.Request != nil {
		cursor.targetURL = resp.Request.URL.String()
	}

	maxResponseBytes := effectiveHTTPMaxResponseBytes(execLimits)
	limitedBody := io.LimitReader(resp.Body, maxResponseBytes+1)
	respBody, err := io.ReadAll(limitedBody)
	if err != nil {
		if (qd.Acknowledgement != nil || writeRetry) && !isIdempotentHTTPMethod(qd.Method) {
			return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP write response was incomplete. Verify remote state before retrying.")), nil
		}
		durationMs := time.Since(start).Milliseconds()
		if httpDeadlineExceeded(ctx, err) {
			return result.NewHTTPResult(nil, durationMs, xerror.Timeout(xerror.DataSource).With(err)), nil
		}
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.Unexpected().With(err),
		), nil
	}
	if int64(len(respBody)) > maxResponseBytes {
		durationMs := time.Since(start).Milliseconds()
		return result.NewHTTPResult(
			nil,
			durationMs,
			xerror.ExecutionResponseSizeExceeded().Detailf(
				"response body exceeds %d byte limit",
				maxResponseBytes,
			).WithPublicDetail(fmt.Sprintf(
				"HTTP request completed with status %d; its response exceeded the configured byte limit. Verify remote state before retrying a write.", resp.StatusCode,
			)),
		), nil
	}

	if c.responseExposesSecret(resp.Header, respBody) {
		return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP response contained credential material and was withheld. Verify remote state before retrying a write.")), nil
	}
	if qd.Acknowledgement != nil {
		if err := qd.Acknowledgement.evaluate(resp.StatusCode, respBody); err != nil {
			return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), err), nil
		}
	}

	// Capture headers from response
	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	resultData := &result.HTTPResult{
		StatusCode: resp.StatusCode,
		Headers:    headers,
	}

	if responseBodyIsNativeFile(headers) {
		metadata := filepayload.NewFilePayloadMetadata(
			httpResponseFilename(headers),
			responseHeaderValueIgnoreCase(headers, "Content-Type"),
			respBody,
		)
		resultData.File = &metadata
		resultData.RawBody = append([]byte(nil), respBody...)
	} else {
		// Keep non-file responses JSON-safe for storage and API responses.
		var body json.RawMessage
		if !json.Valid(respBody) {
			quoted, err := json.Marshal(string(respBody))
			if err != nil {
				durationMs := time.Since(start).Milliseconds()
				return result.NewHTTPResult(
					nil,
					durationMs,
					xerror.Unexpected().With(err),
				), nil
			}
			body = json.RawMessage(quoted)
		} else {
			body = json.RawMessage(respBody)
		}
		resultData.Body = body
		resultData.RawBody = append([]byte(nil), respBody...)
	}

	durationMs := time.Since(start).Milliseconds()

	if execLimits.MaxResultRows > 0 && resultData.RowCount() > execLimits.MaxResultRows {
		return result.NewHTTPResult(nil, durationMs, xerror.ExecutionRowLimitExceeded().Detailf(
			"query returned more than %d rows", execLimits.MaxResultRows,
		).WithPublicDetail(fmt.Sprintf(
			"HTTP request completed with status %d; its response exceeded the configured row limit. Verify remote state before retrying a write.", resp.StatusCode,
		))), nil
	}

	// Return transport-level success regardless of target API status code.
	// The caller receives status_code + body and decides how to interpret it.
	return result.NewHTTPResult(resultData, durationMs, nil), nil
}

func doWithLimits(
	baseClient *http.Client,
	ctx context.Context,
	req *http.Request,
	limits types.ExecutionLimits,
	writeRetry bool,
) (*http.Response, error) {
	client := *baseClient
	timeout := limits.HTTPTimeout
	if timeout <= 0 {
		timeout = limits.ConnectorTimeout
	}
	if timeout > 0 && (client.Timeout == 0 || timeout < client.Timeout) {
		client.Timeout = timeout
	}
	maxRedirects := limits.HTTPMaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 4
	}
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if writeRetry {
			return xerror.InvalidInput(xerror.Query).WithPublicDetail("HTTP keyed write retries forbid redirects to preserve effect identity")
		}
		if len(via) > maxRedirects {
			return xerror.ExecutionRedirectLimitExceeded()
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		return nil
	}

	attempts := limits.HTTPRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctx.Err() != nil {
			return nil, xerror.Timeout(xerror.DataSource).With(ctx.Err())
		}
		if attempt > 1 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, xerror.Unexpected().Detailf("reset HTTP request body: %w", err)
			}
			req.Body = body
		}

		resp, err := client.Do(req)
		if !shouldRetryHTTP(req.Method, resp, err, attempt, attempts, writeRetry) {
			if err != nil {
				return resp, mapHTTPClientError(req, err)
			}
			return resp, nil
		}

		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, effectiveHTTPMaxResponseBytes(limits)+1))
			_ = resp.Body.Close()
		}
		if err == nil {
			lastErr = xerror.Unexpected().Detailf("transient HTTP status %d", resp.StatusCode)
		} else {
			lastErr = mapHTTPClientError(req, err)
		}

		delay, delayErr := httpRetryDelay(resp, limits.HTTPRetryBackoff, time.Now())
		if delayErr != nil {
			return nil, delayErr
		}
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			return nil, xerror.Timeout(xerror.DataSource).WithPublicDetail("HTTP retry delay exceeds the remaining operation deadline; retry stopped")
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, xerror.Timeout(xerror.DataSource).With(ctx.Err())
			case <-timer.C:
			}
		}
	}

	return nil, lastErr
}

func effectiveHTTPMaxResponseBytes(limits types.ExecutionLimits) int64 {
	maxResponseBytes := limits.HTTPMaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = limits.MaxResponseBytes
	}
	if maxResponseBytes <= 0 {
		maxResponseBytes = maxExecuteResponseSizeBytes
	}
	return maxResponseBytes
}

func mapHTTPClientError(req *http.Request, err error) error {
	if err == nil {
		return nil
	}
	// Request URLs can contain credentials or opaque pagination cursors.
	var urlErr *url.Error
	if xerror.As(err, &urlErr) {
		err = urlErr.Err
	}
	if xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()) {
		return xerror.ExecutionRedirectLimitExceeded().With(err)
	}
	if httpDeadlineExceeded(req.Context(), err) {
		return xerror.Timeout(xerror.DataSource).With(err)
	}
	return xerror.ConnectionFailed(xerror.DataSource).With(err)
}

func httpDeadlineExceeded(ctx context.Context, err error) bool {
	return xerror.Is(err, context.DeadlineExceeded) ||
		xerror.Is(ctx.Err(), context.DeadlineExceeded)
}

func shouldRetryHTTP(
	method string,
	resp *http.Response,
	err error,
	attempt, attempts int,
	writeRetry bool,
) bool {
	if attempt >= attempts || (!isIdempotentHTTPMethod(method) && !writeRetry) {
		return false
	}
	if err != nil {
		if xerror.Is(err, context.Canceled) || xerror.Is(err, xerror.ExecutionRedirectLimitExceeded()) {
			return false
		}
		var networkError net.Error
		return (xerror.As(err, &networkError) && networkError.Timeout()) ||
			xerror.Is(err, io.EOF) || xerror.Is(err, io.ErrUnexpectedEOF) ||
			xerror.Is(err, syscall.ECONNRESET) || xerror.Is(err, syscall.ECONNREFUSED)
	}
	if resp == nil {
		return false
	}
	return resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode <= 599)
}

func isIdempotentHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func isNativeCSVInputBodyTemplate(body any, params map[string]any) bool {
	return isNativeInputBodyTemplate(body, params, model.ContractRepresentationCSV)
}

// isNativeFileInputBodyTemplate detects raw file sink bodies behind the input placeholder.
func isNativeFileInputBodyTemplate(body any, params map[string]any) bool {
	return isNativeInputBodyTemplate(body, params, model.ContractRepresentationFile)
}

// isNativeInputBodyTemplate checks whether {{input}} should bypass JSON encoding.
func isNativeInputBodyTemplate(
	body any,
	params map[string]any,
	expected model.ContractRepresentation,
) bool {
	template, ok := body.(string)
	if !ok {
		return false
	}

	if template != fmt.Sprintf("{{%s}}", sinkbinding.Input) {
		return false
	}

	return sinkInputRepresentation(params) == expected
}

// sinkInputRepresentation reads the internal runtime payload representation hint.
func sinkInputRepresentation(params map[string]any) model.ContractRepresentation {
	representation, ok := params[sinkbinding.InternalInputRepresentation]
	if !ok {
		return model.ContractRepresentationUnknown
	}

	switch typed := representation.(type) {
	case string:
		parsed, err := model.ContractRepresentationString(typed)
		if err != nil {
			return model.ContractRepresentationUnknown
		}
		return parsed
	case model.ContractRepresentation:
		return typed
	default:
		return model.ContractRepresentationUnknown
	}
}

// nativeFileInputContentType resolves the content type attached to the file sink payload.
func nativeFileInputContentType(params map[string]any) string {
	if mediaType, ok := params[sinkbinding.InternalInputFileMIMEType].(string); ok {
		return filepayload.NormalizeFilePayloadMIMEType(mediaType)
	}

	metadata, ok := params[sinkbinding.InternalInputFileMetadata].(map[string]any)
	if !ok {
		return filepayload.FilePayloadDefaultMIMEType
	}

	mediaType, _ := metadata["mime_type"].(string)
	return filepayload.NormalizeFilePayloadMIMEType(mediaType)
}

// responseBodyIsNativeFile classifies opaque HTTP responses as native files.
func responseBodyIsNativeFile(headers map[string]string) bool {
	contentType := responseHeaderValueIgnoreCase(headers, "Content-Type")
	mediaType := normalizedMediaType(contentType)
	if mediaType == "" {
		return false
	}
	if mediaType == "text/csv" || mediaType == "application/csv" ||
		mediaType == httpContentTypeMultipart || mediaType == httpContentTypeFormEncoded {
		return false
	}

	return !isJSONContentType(mediaType)
}

// httpResponseFilename extracts a safe filename hint from Content-Disposition.
func httpResponseFilename(headers map[string]string) string {
	contentDisposition := responseHeaderValueIgnoreCase(headers, "Content-Disposition")
	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		return ""
	}

	return filepayload.NormalizeFilePayloadFilename(params["filename"])
}

// responseHeaderValueIgnoreCase reads a response header from a normalized map.
func responseHeaderValueIgnoreCase(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}

	return ""
}

func shouldSendRawStringBody(resolvedBody any, contentType string) bool {
	if _, ok := resolvedBody.(string); !ok {
		return false
	}

	if contentType == "" {
		return false
	}

	return !isJSONContentType(contentType)
}

// shouldSendFormEncodedBody checks whether the requested media type is URL-encoded form data.
func shouldSendFormEncodedBody(resolvedBody any, contentType string) bool {
	if !isFormBody(resolvedBody) {
		return false
	}

	return isFormEncodedContentType(contentType)
}

// shouldSendMultipartBody checks whether the body should be sent as multipart/form-data.
func shouldSendMultipartBody(
	resolvedBody any,
	contentType string,
	representation model.ContractRepresentation,
) bool {
	if !isFormBody(resolvedBody) {
		return false
	}

	if isMultipartContentType(contentType) {
		return true
	}

	return contentType == "" && representation == model.ContractRepresentationMultipartForm
}

// shouldRejectMultipartFormContentType detects explicit non-form media types for form payloads.
func shouldRejectMultipartFormContentType(
	resolvedBody any,
	contentType string,
	representation model.ContractRepresentation,
) bool {
	if representation != model.ContractRepresentationMultipartForm || !isFormBody(resolvedBody) {
		return false
	}
	if contentType == "" {
		return false
	}
	return !isMultipartContentType(contentType) && !isFormEncodedContentType(contentType)
}

// isFormBody reports whether a resolved body can be serialized as HTTP form data.
func isFormBody(resolvedBody any) bool {
	switch resolvedBody.(type) {
	case map[string]any, formpayload.FormData, *formpayload.FormData:
		return true
	default:
		return false
	}
}

func encodeFormBody(resolvedBody any) (string, error) {
	fields, files, err := collectFormBodyParts("form-encoded body", resolvedBody)
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		return "", xerror.Unexpected().Detail(
			"form-encoded body cannot include file parts",
		)
	}

	values := url.Values{}
	for _, field := range fields {
		values.Add(field.name, field.value)
	}

	return values.Encode(), nil
}

func encodeMultipartBody(resolvedBody any, boundary string) ([]byte, string, error) {
	fields, files, err := collectFormBodyParts("multipart body", resolvedBody)
	if err != nil {
		return nil, "", err
	}

	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	if boundary != "" {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, "", xerror.Unexpected().With(err)
		}
	}

	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			_ = writer.Close()
			return nil, "", xerror.Unexpected().Detailf(
				"write multipart body field %q: %w",
				field.name,
				err,
			)
		}
	}
	for _, file := range files {
		part, err := writer.CreatePart(multipartFileHeader(file))
		if err != nil {
			_ = writer.Close()
			return nil, "", xerror.Unexpected().Detailf(
				"create multipart body file %q: %w",
				file.name,
				err,
			)
		}
		if _, err := part.Write(file.content); err != nil {
			_ = writer.Close()
			return nil, "", xerror.Unexpected().Detailf(
				"write multipart body file %q: %w",
				file.name,
				err,
			)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", xerror.Unexpected().Detailf("close multipart body writer: %w", err)
	}

	return buffer.Bytes(), writer.FormDataContentType(), nil
}

type formBodyField struct {
	name  string
	value string
}

type formBodyFile struct {
	name        string
	filename    string
	contentType string
	content     []byte
}

// collectFormBodyParts normalizes map and multipart_form bodies into fields and files.
func collectFormBodyParts(
	kind string,
	resolvedBody any,
) ([]formBodyField, []formBodyFile, error) {
	switch typed := resolvedBody.(type) {
	case map[string]any:
		return collectMapFormBodyParts(kind, typed)
	case formpayload.FormData:
		return collectPayloadFormBodyParts(kind, &typed)
	case *formpayload.FormData:
		return collectPayloadFormBodyParts(kind, typed)
	default:
		return nil, nil, xerror.Unexpected().Detailf(
			"%s must resolve to object or multipart_form content",
			kind,
		)
	}
}

// collectMapFormBodyParts converts a map body into deterministic form parts.
func collectMapFormBodyParts(
	kind string,
	bodyMap map[string]any,
) ([]formBodyField, []formBodyFile, error) {
	keys := make([]string, 0, len(bodyMap))
	for key := range bodyMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var fields []formBodyField
	var files []formBodyFile
	for _, key := range keys {
		collectedFields, collectedFiles, err := collectMapFormValue(kind, key, bodyMap[key])
		if err != nil {
			return nil, nil, err
		}
		fields = append(fields, collectedFields...)
		files = append(files, collectedFiles...)
	}

	return fields, files, nil
}

// collectMapFormValue converts one map value into scalar or file form parts.
func collectMapFormValue(
	kind string,
	key string,
	value any,
) ([]formBodyField, []formBodyFile, error) {
	switch typed := value.(type) {
	case formpayload.FormFile:
		return nil, []formBodyFile{formBodyFileFromPayload(key, typed)}, nil
	case *formpayload.FormFile:
		if typed == nil {
			return []formBodyField{{name: key}}, nil, nil
		}
		return nil, []formBodyFile{formBodyFileFromPayload(key, *typed)}, nil
	case []any:
		return collectRepeatedMapFormValues(kind, key, typed)
	case []string:
		fields := make([]formBodyField, 0, len(typed))
		for _, item := range typed {
			fields = append(fields, formBodyField{name: key, value: item})
		}
		return fields, nil, nil
	default:
		fieldValue, err := encodeScalarBodyField(kind, key, value)
		if err != nil {
			return nil, nil, err
		}
		return []formBodyField{{name: key, value: fieldValue}}, nil, nil
	}
}

// collectRepeatedMapFormValues expands an array value into repeated form parts.
func collectRepeatedMapFormValues(
	kind string,
	key string,
	values []any,
) ([]formBodyField, []formBodyFile, error) {
	var fields []formBodyField
	var files []formBodyFile
	for _, value := range values {
		collectedFields, collectedFiles, err := collectMapFormValue(kind, key, value)
		if err != nil {
			return nil, nil, err
		}
		fields = append(fields, collectedFields...)
		files = append(files, collectedFiles...)
	}
	return fields, files, nil
}

// collectPayloadFormBodyParts converts a multipart_form payload into outgoing parts.
func collectPayloadFormBodyParts(
	kind string,
	form *formpayload.FormData,
) ([]formBodyField, []formBodyFile, error) {
	if form == nil {
		return nil, nil, xerror.Unexpected().Detailf(
			"%s must resolve to multipart_form content",
			kind,
		)
	}

	fields := make([]formBodyField, 0)
	for _, field := range form.Fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			return nil, nil, xerror.Unexpected().Detailf("%s field name is required", kind)
		}
		values := field.Values
		if len(values) == 0 && field.ValueCount > 0 {
			return nil, nil, xerror.Unexpected().Detailf(
				"%s field %q has no runtime values",
				kind,
				name,
			)
		}
		if len(values) == 0 {
			values = []string{""}
		}
		for _, value := range values {
			fields = append(fields, formBodyField{name: name, value: value})
		}
	}

	files := make([]formBodyFile, 0, len(form.Files))
	for _, file := range form.Files {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			return nil, nil, xerror.Unexpected().Detailf("%s file field name is required", kind)
		}
		files = append(files, formBodyFileFromPayload(name, file))
	}

	sort.SliceStable(fields, func(i, j int) bool {
		return fields[i].name < fields[j].name
	})
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].name == files[j].name {
			return files[i].filename < files[j].filename
		}
		return files[i].name < files[j].name
	})

	return fields, files, nil
}

// formBodyFileFromPayload copies runtime file content into the connector form part shape.
func formBodyFileFromPayload(name string, file formpayload.FormFile) formBodyFile {
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return formBodyFile{
		name:        name,
		filename:    safeMultipartFilename(file.Filename),
		contentType: contentType,
		content:     append([]byte(nil), file.Content...),
	}
}

// multipartFileHeader builds a safe multipart header for one file part.
func multipartFileHeader(file formBodyFile) textproto.MIMEHeader {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     file.name,
		"filename": file.filename,
	}))
	header.Set("Content-Type", file.contentType)
	return header
}

// safeMultipartFilename strips path components from a caller-provided form filename.
func safeMultipartFilename(filename string) string {
	normalized := strings.ReplaceAll(filename, "\\", "/")
	base := strings.TrimSpace(filepath.Base(normalized))
	if base == "." || base == "/" || base == "" {
		return "blob"
	}
	return base
}

func encodeScalarBodyField(kind, key string, value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool,
		float32,
		float64,
		int,
		int8,
		int16,
		int32,
		int64,
		uint,
		uint8,
		uint16,
		uint32,
		uint64:
		return fmt.Sprint(typed), nil
	default:
		return "", xerror.Unexpected().Detailf(
			"%s field %q must be scalar, got %T",
			kind,
			key,
			value,
		)
	}
}

func isJSONContentType(contentType string) bool {
	trimmed := normalizedMediaType(contentType)
	if trimmed == httpContentTypeJSON {
		return true
	}

	return strings.HasPrefix(trimmed, "application/") && strings.HasSuffix(trimmed, "+json")
}

func isFormEncodedContentType(contentType string) bool {
	return normalizedMediaType(contentType) == httpContentTypeFormEncoded
}

func isMultipartContentType(contentType string) bool {
	return normalizedMediaType(contentType) == httpContentTypeMultipart
}

func normalizedMediaType(contentType string) string {
	trimmed := strings.TrimSpace(strings.ToLower(contentType))
	mediaType, _, _ := strings.Cut(trimmed, ";")
	return strings.TrimSpace(mediaType)
}

func headerValueIgnoreCase(headers map[string]any, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return fmt.Sprintf("%v", value)
		}
	}

	return ""
}

// IntrospectSchema retrieves and parses the OpenAPI schema from the configured OpenAPIURL.
func (c *Connector) IntrospectSchema(ctx context.Context) (*HTTPSchema, error) {
	if c.config.OpenAPIURL == "" {
		return &HTTPSchema{Operations: []Operation{}}, nil
	}

	// Validate URL (SSRF check) against baseURL.
	// AllowPrivateHosts relaxes private/literal-IP checks end-to-end.
	if err := validateOpenAPIURL(
		c.config.OpenAPIURL,
		c.config.BaseURL,
		c.config.AllowPrivateHosts,
	); err != nil {
		return nil, xerror.InvalidSchemaURL().
			With(err).
			WithPublicDetail("OpenAPI URL failed safety validation")
	}

	dialCtxFn := hostguard.NewDestinationPolicy(c.config.AllowPrivateHosts).DialContext

	if c.allowPlaintext {
		ctx = transportsecurity.WithPlaintext(ctx)
	}
	specBytes, err := fetchOpenAPISpecWithDialer(
		ctx,
		c.config.OpenAPIURL,
		c.config.OpenAPIFetchTimeoutSeconds,
		dialCtxFn,
		c.config.AllowPrivateHosts,
		c.openAPIHeaders(),
		c.oauthDestination,
	)
	if err != nil {
		return nil, xerror.SchemaFetchFailed().With(
			xerror.Wrapf("failed to fetch OpenAPI spec: %w", err),
		).WithPublicDetail("failed to fetch OpenAPI spec")
	}

	if c.openAPIResponseExposesSecret(specBytes) {
		return nil, xerror.SchemaFetchFailed().WithPublicDetail("OpenAPI response contained credential material and was withheld")
	}

	// Parse the schema
	schema, err := parseOpenAPIWithQuota(ctx, specBytes)
	if err != nil {
		return nil, xerror.InvalidOpenAPI().With(
			xerror.Wrapf("failed to parse OpenAPI spec: %w", err),
		).WithPublicDetail("failed to parse OpenAPI spec")
	}

	return schema, nil
}
