// Package crm implements HubSpot CRM API requests using a portal access token.
package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultBaseURL       = "https://api.hubapi.com"
	supportedAPIVersion  = "2026-03"
	defaultTimeout       = 5 * time.Second
	defaultPageLimit     = 100
	MaxBatchUpsertInputs = 100
	maxResponseBodyBytes = 10 * 1024 * 1024
)

var (
	errInvalidConfig          = xerror.InvalidInput(xerror.DataSource)
	customObjectTypeIDPattern = regexp.MustCompile(`^2-[1-9][0-9]*$`)
	supportedObjects          = []Object{
		{Name: "contacts", Label: "Contacts"},
		{Name: "companies", Label: "Companies"},
		{Name: "deals", Label: "Deals"},
	}
)

type Client struct {
	baseURL string
	token   string
	client  *http.Client
}

// New validates the access token and base URL, then creates an SSRF-protected transport when private hosts are disabled.
func New(ctx context.Context, config Config, accessToken string) (*Client, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return nil, xerror.InvalidCredentials().Detail("hubspot access token is required")
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).With(err)
	}
	if parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail(
			"hubspot base_url must use http or https",
		)
	}

	if err := transportsecurity.ValidateURL(parsedBaseURL, transportsecurity.PlaintextAllowed(ctx)); err != nil {
		return nil, err
	}
	if !config.AllowPrivateHosts {
		if err := hostguard.ValidateResolvedPublicHost(ctx, parsedBaseURL.Hostname()); err != nil {
			return nil, xerror.Wrapf(
				"%w: hubspot base_url blocked by ssrf protection: %w",
				errInvalidConfig,
				err,
			)
		}
	}

	timeout := defaultTimeout
	if config.TimeoutSeconds > 0 {
		timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}

	var transport http.RoundTripper
	if !config.AllowPrivateHosts {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, xerror.Unexpected().Detail("default transport has unexpected type")
		}

		cloned := defaultTransport.Clone()
		dialer := &net.Dialer{}
		cloned.DialContext = func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, xerror.Unexpected().With(err)
			}
			if err := hostguard.ValidateResolvedPublicHost(ctx, host); err != nil {
				return nil, xerror.Wrapf("ssrf protection: %w", err)
			}
			return dialer.DialContext(ctx, network, address)
		}
		transport = cloned
	}

	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client: &http.Client{
			Timeout:       timeout,
			Transport:     transportsecurity.HTTPTransport(transport, transportsecurity.PlaintextAllowed(ctx)),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	c.client.CloseIdleConnections()
	return nil
}

func SupportedObjects() []Object {
	return append([]Object(nil), supportedObjects...)
}

func IsStandardObject(objectType string) bool {
	trimmed := strings.TrimSpace(objectType)
	for _, supportedObject := range supportedObjects {
		if supportedObject.Name == trimmed {
			return true
		}
	}
	return false
}

func IsCustomObjectTypeID(objectType string) bool {
	return customObjectTypeIDPattern.MatchString(strings.TrimSpace(objectType))
}

func (c *Client) AccountDetails(ctx context.Context) (*AccountDetails, error) {
	req, err := c.newRequest(ctx, http.MethodGet, AccountMetadataPath, nil)
	if err != nil {
		return nil, err
	}

	var payload accountDetailsResponse
	if _, err := c.doJSON(req, &payload, maxResponseBodyBytes); err != nil {
		return nil, err
	}

	return &AccountDetails{
		AccountID:   payload.AccountID,
		CompanyName: payload.CompanyName,
	}, nil
}

func (c *Client) ListProperties(ctx context.Context, objectType string) ([]Property, error) {
	return c.ListPropertiesBounded(ctx, objectType, maxResponseBodyBytes)
}

// ListPropertiesBounded lists schema properties while limiting the provider
// response before decoding it into an in-memory property slice.
func (c *Client) ListPropertiesBounded(ctx context.Context, objectType string, maxBytes int64) ([]Property, error) {
	trimmedObjectType, err := validateObjectType(objectType)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(
		ctx,
		http.MethodGet,
		"/crm/properties/"+supportedAPIVersion+"/"+trimmedObjectType,
		nil,
	)
	if err != nil {
		return nil, err
	}

	var payload propertiesResponse
	if maxBytes <= 0 || maxBytes > maxResponseBodyBytes {
		maxBytes = maxResponseBodyBytes
	}
	if _, err := c.doJSON(req, &payload, maxBytes); err != nil {
		return nil, err
	}

	properties := make([]Property, 0, len(payload.Results))
	for _, property := range payload.Results {
		properties = append(properties, Property{
			Name:           property.Name,
			Label:          property.Label,
			Type:           property.Type,
			HasUniqueValue: property.HasUniqueValue,
			ReadOnlyValue:  property.ReadOnlyValue,
		})
	}

	return properties, nil
}

func (c *Client) ListCustomObjectSchemas(ctx context.Context) ([]CustomObjectSchema, error) {
	return c.ListCustomObjectSchemasBounded(ctx, maxResponseBodyBytes)
}

// ListCustomObjectSchemasBounded retrieves account-defined object schemas and
// their property definitions under the caller's response-size budget.
func (c *Client) ListCustomObjectSchemasBounded(
	ctx context.Context,
	maxBytes int64,
) ([]CustomObjectSchema, error) {
	req, err := c.newRequest(
		ctx,
		http.MethodGet,
		"/crm-object-schemas/"+supportedAPIVersion+"/schemas?includePropertyDefinitions=true",
		nil,
	)
	if err != nil {
		return nil, err
	}

	if maxBytes <= 0 || maxBytes > maxResponseBodyBytes {
		maxBytes = maxResponseBodyBytes
	}
	var payload customObjectSchemasResponse
	if _, err := c.doJSON(req, &payload, maxBytes); err != nil {
		return nil, err
	}

	schemas := make([]CustomObjectSchema, 0, len(payload.Results))
	for _, item := range payload.Results {
		objectTypeID := strings.TrimSpace(item.ObjectTypeID)
		if item.Archived || !IsCustomObjectTypeID(objectTypeID) {
			continue
		}
		label := strings.TrimSpace(item.Labels.Plural)
		if label == "" {
			label = strings.TrimSpace(item.Name)
		}
		if label == "" {
			label = objectTypeID
		}
		properties := make([]Property, 0, len(item.Properties))
		for _, property := range item.Properties {
			properties = append(properties, Property{
				Name:           property.Name,
				Label:          property.Label,
				Type:           property.Type,
				HasUniqueValue: property.HasUniqueValue,
				ReadOnlyValue:  property.ReadOnlyValue,
			})
		}
		schemas = append(schemas, CustomObjectSchema{
			ObjectTypeID: objectTypeID,
			Name:         strings.TrimSpace(item.Name),
			Label:        label,
			Properties:   properties,
		})
	}

	return schemas, nil
}

func (c *Client) List(ctx context.Context, req ListRequest) (*Page, error) {
	objectType, err := validateObjectType(req.ObjectType)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("limit", strconv.Itoa(normalizeLimit(req.Limit)))
	if len(req.Properties) > 0 {
		query.Set("properties", strings.Join(req.Properties, ","))
	}
	if after := strings.TrimSpace(req.After); after != "" {
		query.Set("after", after)
	}

	request, err := c.newRequest(
		ctx,
		http.MethodGet,
		"/crm/objects/"+supportedAPIVersion+"/"+objectType+"?"+query.Encode(),
		nil,
	)
	if err != nil {
		return nil, err
	}

	var response objectsResponse
	if _, err := c.doJSON(request, &response, effectiveMaxResponseBytes(req.MaxResponseBytes)); err != nil {
		return nil, err
	}

	return pageFromResponse(objectType, response), nil
}

func (c *Client) Search(ctx context.Context, req SearchRequest) (*Page, error) {
	objectType, err := validateObjectType(req.ObjectType)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"limit": normalizeLimit(req.Limit),
	}

	if len(req.Properties) > 0 {
		payload["properties"] = append([]string(nil), req.Properties...)
	}
	if query := strings.TrimSpace(req.Query); query != "" {
		payload["query"] = query
	}
	if after := strings.TrimSpace(req.After); after != "" {
		payload["after"] = after
	}
	if len(req.Sorts) > 0 {
		payload["sorts"] = append([]string(nil), req.Sorts...)
	}
	if len(req.FilterGroups) > 0 {
		payload["filterGroups"] = buildFilterGroups(req.FilterGroups)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	request, err := c.newRequest(
		ctx,
		http.MethodPost,
		"/crm/objects/"+supportedAPIVersion+"/"+objectType+"/search",
		body,
	)
	if err != nil {
		return nil, err
	}

	var response objectsResponse
	if _, err := c.doJSON(request, &response, effectiveMaxResponseBytes(req.MaxResponseBytes)); err != nil {
		return nil, err
	}

	return pageFromResponse(objectType, response), nil
}

func (c *Client) Upsert(ctx context.Context, req UpsertRequest) (*UpsertResult, error) {
	objectType, err := validateObjectType(req.ObjectType)
	if err != nil {
		return nil, err
	}
	if len(req.Inputs) == 0 || len(req.Inputs) > MaxBatchUpsertInputs {
		return nil, xerror.InvalidInput(xerror.Query).Detailf(
			"hubspot batch upsert requires between 1 and %d inputs",
			MaxBatchUpsertInputs,
		)
	}

	inputs := make([]map[string]any, 0, len(req.Inputs))
	for _, input := range req.Inputs {
		id := strings.TrimSpace(input.ID)
		idProperty := strings.TrimSpace(input.IDProperty)
		if id == "" || idProperty == "" {
			return nil, xerror.InvalidInput(xerror.Query).Detail(
				"hubspot batch upsert id and id_property are required",
			)
		}
		inputs = append(inputs, map[string]any{
			"id":         id,
			"idProperty": idProperty,
			"properties": input.Properties,
		})
	}
	body, err := json.Marshal(map[string]any{"inputs": inputs})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	request, err := c.newRequest(
		ctx,
		http.MethodPost,
		"/crm/objects/"+supportedAPIVersion+"/"+objectType+"/batch/upsert",
		body,
	)
	if err != nil {
		return nil, err
	}

	var response batchUpsertResponse
	statusCode, _, err := c.doJSONStatus(
		request,
		&response,
		effectiveMaxResponseBytes(req.MaxResponseBytes),
	)
	if err != nil {
		return nil, sanitizeUpsertError(err)
	}
	if statusCode == http.StatusMultiStatus || len(response.Errors) > 0 ||
		!strings.EqualFold(strings.TrimSpace(response.Status), "COMPLETE") ||
		len(response.Results) != len(req.Inputs) {
		return nil, xerror.Unavailable(xerror.DataSource).Detail(
			"hubspot batch upsert did not complete for every input",
		)
	}

	records := make([]UpsertRecord, 0, len(response.Results))
	for _, record := range response.Results {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return nil, xerror.Unavailable(xerror.DataSource).Detail(
				"hubspot batch upsert returned an incomplete result",
			)
		}
		records = append(records, UpsertRecord{ID: id, Created: record.Created})
	}
	return &UpsertResult{Records: records}, nil
}

func sanitizeUpsertError(err error) error {
	switch {
	case xerror.Is(err, xerror.InvalidCredentials()):
		return xerror.InvalidCredentials().With(err)
	case xerror.Is(err, xerror.Forbidden(xerror.DataSource)):
		return xerror.Forbidden(xerror.DataSource).Detail("hubspot batch upsert is not authorized").With(err)
	case xerror.Is(err, xerror.InvalidInput(xerror.Query)):
		return xerror.InvalidInput(xerror.Query).Detail("hubspot rejected the batch upsert request").With(err)
	case xerror.Is(err, xerror.Timeout(xerror.DataSource)):
		return xerror.Timeout(xerror.DataSource).Detail("hubspot batch upsert timed out").With(err)
	case xerror.Is(err, xerror.ExecutionResponseSizeExceeded()):
		return xerror.ExecutionResponseSizeExceeded().With(err)
	case xerror.Is(err, xerror.ConnectionFailed(xerror.DataSource)):
		return xerror.ConnectionFailed(xerror.DataSource).Detail("hubspot batch upsert connection failed").With(err)
	default:
		return xerror.Unavailable(xerror.DataSource).Detail("hubspot batch upsert failed").With(err)
	}
}

func validateObjectType(objectType string) (string, error) {
	trimmed := strings.TrimSpace(objectType)
	if IsStandardObject(trimmed) || IsCustomObjectTypeID(trimmed) {
		return trimmed, nil
	}

	return "", xerror.InvalidInput(xerror.Query).Detailf(
		"unsupported HubSpot object type %q",
		trimmed,
	)
}

func normalizeLimit(limit int) int {
	if limit > 0 {
		return limit
	}
	return defaultPageLimit
}

func buildFilterGroups(groups []FilterGroup) []map[string]any {
	resolvedGroups := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		filters := make([]map[string]any, 0, len(group.Filters))
		for _, filter := range group.Filters {
			resolvedFilter := map[string]any{
				"propertyName": filter.PropertyName,
				"operator":     filter.Operator,
			}
			if filter.Value != nil {
				resolvedFilter["value"] = filter.Value
			}
			if filter.HighValue != nil {
				resolvedFilter["highValue"] = filter.HighValue
			}
			if len(filter.Values) > 0 {
				resolvedFilter["values"] = append([]any(nil), filter.Values...)
			}
			filters = append(filters, resolvedFilter)
		}
		resolvedGroups = append(resolvedGroups, map[string]any{"filters": filters})
	}

	return resolvedGroups
}

func pageFromResponse(objectType string, response objectsResponse) *Page {
	records := make([]Record, 0, len(response.Results))
	for _, item := range response.Results {
		records = append(records, Record(item))
	}

	var nextAfter *string
	if after := strings.TrimSpace(response.Paging.Next.After); after != "" {
		nextAfter = &after
	}

	return &Page{
		ObjectType: objectType,
		Records:    records,
		NextAfter:  nextAfter,
		HasMore:    nextAfter != nil,
	}
}

func (c *Client) newRequest(
	ctx context.Context,
	method string,
	path string,
	body []byte,
) (*http.Request, error) {
	targetURL := c.baseURL + path
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, xerror.ConnectionFailed(xerror.DataSource).With(err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "braidplane-hubspot/"+supportedAPIVersion)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

func (c *Client) doJSON(req *http.Request, target any, maxResponseBytes int64) ([]byte, error) {
	_, body, err := c.doJSONStatus(req, target, maxResponseBytes)
	return body, err
}

func (c *Client) doJSONStatus(
	req *http.Request,
	target any,
	maxResponseBytes int64,
) (int, []byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		if deadlineExceeded(req.Context(), err) {
			return 0, nil, xerror.Timeout(xerror.DataSource).With(err)
		}
		return 0, nil, xerror.ConnectionFailed(xerror.DataSource).With(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, xerror.Unexpected().With(err)
	}
	if int64(len(body)) > maxResponseBytes {
		return resp.StatusCode, nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"HubSpot response body exceeds %d byte limit",
			maxResponseBytes,
		)
	}

	if err := mapProviderError(resp, body); err != nil {
		return resp.StatusCode, nil, err.WithPublicDetail("HubSpot request failed: " + err.Message())
	}

	if target == nil || len(body) == 0 {
		return resp.StatusCode, body, nil
	}

	if err := json.Unmarshal(body, target); err != nil {
		return resp.StatusCode, nil, xerror.Unexpected().With(err)
	}

	return resp.StatusCode, body, nil
}

func effectiveMaxResponseBytes(limit int64) int64 {
	if limit > 0 {
		return limit
	}
	return maxResponseBodyBytes
}

func mapProviderError(resp *http.Response, body []byte) *xerror.Error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	providerMessage := strings.TrimSpace(providerErrorMessage(body))
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		if providerMessage == "" {
			return xerror.InvalidCredentials()
		}
		return xerror.InvalidCredentials().Detail(providerMessage)
	case http.StatusForbidden:
		if providerMessage == "" {
			return xerror.Forbidden(xerror.DataSource)
		}
		return xerror.Forbidden(xerror.DataSource).Detail(providerMessage)
	case http.StatusTooManyRequests:
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		err := xerror.Unavailable(xerror.DataSource)
		if retryAfter != "" {
			return err.Detailf("hubspot rate limit exceeded; retry after %s seconds", retryAfter)
		}
		if providerMessage != "" {
			return err.Detail(providerMessage)
		}
		return err.Detail("hubspot rate limit exceeded")
	default:
		if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
			err := xerror.InvalidInput(xerror.Query)
			if providerMessage != "" {
				return err.Detail(providerMessage)
			}
			return err.Detailf("hubspot request failed with status %d", resp.StatusCode)
		}

		err := xerror.Unavailable(xerror.DataSource)
		if providerMessage != "" {
			return err.Detail(providerMessage)
		}
		return err.Detailf("hubspot request failed with status %d", resp.StatusCode)
	}
}

func providerErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var payload struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Message
}

func deadlineExceeded(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() == context.DeadlineExceeded {
		return true
	}
	var urlErr *url.Error
	if xerror.As(err, &urlErr) {
		return urlErr.Timeout()
	}
	return false
}
