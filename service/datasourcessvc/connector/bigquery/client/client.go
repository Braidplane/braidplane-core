package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/bigquery/v2"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/googleclient"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultBaseURL = "https://bigquery.googleapis.com"
	// #nosec G101 -- This is the public OAuth token endpoint, not credential material.
	defaultTokenURL         = "https://oauth2.googleapis.com/token"
	defaultTimeout          = 5 * time.Second
	defaultMaxResponseBytes = googleclient.DefaultMaxResponseBytes
	bigQueryScope           = "https://www.googleapis.com/auth/bigquery"
	userAgent               = "braidplane-bigquery/2026-06"
)

// Client adapts the official BigQuery REST SDK to Braidplane resource policies.
type Client struct {
	baseURL         string
	tokenURL        string
	scope           string
	serviceAccount  serviceAccount
	accessTokenOnly string
	client          *http.Client
	api             *bigquery.Service
	tokenMu         sync.Mutex
	token           string
	tokenExpiresAt  time.Time
}

// New creates a BigQuery REST client from runtime configuration and write-only credentials.
func New(ctx context.Context, config Config, creds Credentials) (*Client, error) {
	account, err := parseCredentials(creds)
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	tokenURL := strings.TrimSpace(config.TokenURL)
	if tokenURL == "" {
		tokenURL = strings.TrimSpace(account.TokenURI)
	}
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}

	parsedBaseURL, err := parseProviderURL(baseURL, "bigquery base_url")
	if err != nil {
		return nil, err
	}
	parsedTokenURL, err := parseProviderURL(tokenURL, "bigquery token_url")
	if err != nil {
		return nil, err
	}

	for _, endpoint := range []*url.URL{parsedBaseURL, parsedTokenURL} {
		if err := transportsecurity.ValidateURL(endpoint, transportsecurity.PlaintextAllowed(ctx)); err != nil {
			return nil, err
		}
	}
	if !config.AllowPrivateHosts {
		if err := validateProviderHost(ctx, parsedBaseURL.Hostname(), "base_url"); err != nil {
			return nil, err
		}
		if err := validateProviderHost(ctx, parsedTokenURL.Hostname(), "token_url"); err != nil {
			return nil, err
		}
	}

	timeout := defaultTimeout
	if config.TimeoutSeconds > 0 {
		timeout = time.Duration(config.TimeoutSeconds) * time.Second
	}

	transport, err := providerTransport(config.AllowPrivateHosts)
	if err != nil {
		return nil, err
	}

	c := &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		tokenURL:        tokenURL,
		serviceAccount:  account,
		accessTokenOnly: creds.AccessToken,
		client:          googleclient.NewHTTPClient(timeout, transportsecurity.HTTPTransport(transport, transportsecurity.PlaintextAllowed(ctx))),
	}
	if err := c.initSDK(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Close releases idle HTTP connections held by the BigQuery client.
func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	c.client.CloseIdleConnections()
	return nil
}

// TestConnection verifies credentials with a low-risk dataset request.
func (c *Client) TestConnection(ctx context.Context, req TestConnectionRequest) error {
	if req.DatasetID != "" {
		_, _, err := callSDK(ctx, 0, 0, func(ctx context.Context) (*bigquery.Dataset, error) {
			return c.api.Datasets.Get(req.ProjectID, req.DatasetID).Context(ctx).Do()
		})
		return err
	}
	_, _, err := callSDK(ctx, 0, 0, func(ctx context.Context) (*bigquery.DatasetList, error) {
		return c.api.Datasets.List(req.ProjectID).MaxResults(1).Context(ctx).Do()
	})
	return err
}

// ListDatasets lists datasets within the schema quota.
func (c *Client) ListDatasets(ctx context.Context, req ListDatasetsRequest) ([]Dataset, error) {
	datasets := []Dataset{}
	pageToken := ""
	for {
		limit, err := remainingSchemaResponseBytes(ctx)
		if err != nil {
			return nil, err
		}
		response, _, err := callSDK(ctx, 0, limit, func(ctx context.Context) (*bigquery.DatasetList, error) {
			call := c.api.Datasets.List(req.ProjectID)
			if pageToken != "" {
				call.PageToken(pageToken)
			}
			return call.Context(ctx).Do()
		})
		if err != nil {
			return nil, err
		}
		for _, item := range response.Datasets {
			if item == nil || item.DatasetReference == nil || item.DatasetReference.DatasetId == "" {
				continue
			}
			id := item.DatasetReference.DatasetId
			if err := schemaquota.Charge(ctx, 0, len(id)); err != nil {
				return nil, err
			}
			datasets = append(datasets, Dataset{ID: id})
		}
		if response.NextPageToken == "" {
			return datasets, nil
		}
		pageToken = response.NextPageToken
	}
}

// ListTables lists tables within the schema quota.
func (c *Client) ListTables(ctx context.Context, req ListTablesRequest) ([]Table, error) {
	tables := []Table{}
	pageToken := ""
	for {
		limit, err := remainingSchemaResponseBytes(ctx)
		if err != nil {
			return nil, err
		}
		response, _, err := callSDK(ctx, 0, limit, func(ctx context.Context) (*bigquery.TableList, error) {
			call := c.api.Tables.List(req.ProjectID, req.DatasetID)
			if pageToken != "" {
				call.PageToken(pageToken)
			}
			return call.Context(ctx).Do()
		})
		if err != nil {
			return nil, err
		}
		for _, item := range response.Tables {
			if item == nil || item.TableReference == nil {
				continue
			}
			ref := item.TableReference
			if ref.DatasetId == "" || ref.TableId == "" {
				continue
			}
			if err := schemaquota.Charge(ctx, 1, len(ref.DatasetId)+len(ref.TableId)); err != nil {
				return nil, err
			}
			tables = append(tables, Table{DatasetID: ref.DatasetId, TableID: ref.TableId})
		}
		if response.NextPageToken == "" {
			return tables, nil
		}
		pageToken = response.NextPageToken
	}
}

// GetTable preserves observed identity and schema facts without inferred defaults.
func (c *Client) GetTable(ctx context.Context, req GetTableRequest) (*TableMetadata, error) {
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.DatasetID) == "" || strings.TrimSpace(req.TableID) == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("BigQuery table metadata requires an exact project, dataset, and table")
	}
	limit, err := remainingSchemaResponseBytes(ctx)
	if err != nil {
		return nil, err
	}
	response, body, err := callSDK(ctx, 0, limit, func(ctx context.Context) (*bigquery.Table, error) {
		return c.api.Tables.Get(req.ProjectID, req.DatasetID, req.TableID).Context(ctx).Do()
	})
	if err != nil {
		return nil, err
	}
	ref := response.TableReference
	resourceType, err := TableTypeString(response.Type)
	if ref == nil || ref.ProjectId != req.ProjectID || ref.DatasetId != req.DatasetID || ref.TableId != req.TableID ||
		err != nil || resourceType == TableTypeUnknown || resourceType.String() != response.Type {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("BigQuery table metadata is incomplete, mismatched, or has an unsupported resource type")
	}
	fields, err := fieldsFromResponse(body)
	if err != nil {
		return nil, err
	}
	return &TableMetadata{ProjectID: ref.ProjectId, DatasetID: ref.DatasetId, TableID: ref.TableId, Type: resourceType, Fields: fields}, nil
}

// Query executes jobs.query within cumulative response and retained-row budgets.
func (c *Client) Query(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	remainingBytes := effectiveMaxResponseBytes(req.MaxResponseBytes)
	response, body, err := callSDK(ctx, 0, remainingBytes, func(ctx context.Context) (*bigquery.QueryResponse, error) {
		return c.api.Jobs.Query(req.ProjectID, buildQueryPayload(req)).Context(ctx).Do()
	})
	if err != nil {
		return nil, err
	}
	schema, err := fieldsFromResponse(body)
	if err != nil {
		return nil, err
	}
	remainingBytes -= int64(len(body))
	queryResult := &QueryResult{Schema: schema, Rows: []map[string]any{}}
	remainingRowBytes := effectiveMaxResponseBytes(req.MaxResponseBytes) - 2
	if remainingRowBytes < 0 {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	if err := appendProviderRows(queryResult, response.Rows, req.MaxResultRows, &remainingRowBytes); err != nil {
		return nil, err
	}
	if response.PageToken == "" || req.MaxResultRows > 0 && len(queryResult.Rows) >= req.MaxResultRows {
		return queryResult, nil
	}
	if response.JobReference == nil || strings.TrimSpace(response.JobReference.JobId) == "" {
		return nil, xerror.Unavailable(xerror.Query).Detail("BigQuery query page missing job id")
	}
	jobID := response.JobReference.JobId
	location := firstNonEmpty(response.JobReference.Location, req.Location)
	pageToken := response.PageToken
	for pageToken != "" {
		if remainingBytes <= 0 {
			return nil, xerror.ExecutionResponseSizeExceeded()
		}
		next, body, err := callSDK(ctx, 0, remainingBytes, func(ctx context.Context) (*bigquery.GetQueryResultsResponse, error) {
			call := c.api.Jobs.GetQueryResults(req.ProjectID, jobID).PageToken(pageToken)
			if location != "" {
				call.Location(location)
			}
			if req.MaxResultRows > 0 {
				call.MaxResults(int64(req.MaxResultRows - len(queryResult.Rows)))
			}
			return call.Context(ctx).Do()
		})
		if err != nil {
			return nil, err
		}
		remainingBytes -= int64(len(body))
		fields, err := fieldsFromResponse(body)
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			queryResult.Schema = fields
		}
		if err := appendProviderRows(queryResult, next.Rows, req.MaxResultRows, &remainingRowBytes); err != nil {
			return nil, err
		}
		if req.MaxResultRows > 0 && len(queryResult.Rows) >= req.MaxResultRows {
			return queryResult, nil
		}
		pageToken = next.PageToken
	}
	return queryResult, nil
}

// InsertRows appends rows with one insertAll attempt.
func (c *Client) InsertRows(ctx context.Context, req InsertRowsRequest) (*InsertRowsResult, error) {
	response, _, err := callSDK(ctx, req.MaxRequestBytes, req.MaxResponseBytes, func(ctx context.Context) (*bigquery.TableDataInsertAllResponse, error) {
		return c.api.Tabledata.InsertAll(req.ProjectID, req.DatasetID, req.TableID, buildInsertRowsPayload(req)).Context(ctx).Do()
	})
	if err != nil {
		return nil, err
	}
	if len(response.InsertErrors) > 0 {
		return nil, xerror.InvalidInput(xerror.Query).Detail("BigQuery insert rows failed")
	}
	return &InsertRowsResult{InsertedRows: len(req.Rows)}, nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	// Grant refresh belongs to credentialssvc; Proof must never refresh in an observation.
	if c.accessTokenOnly != "" {
		return c.accessTokenOnly, nil
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token != "" && time.Now().Before(c.tokenExpiresAt.Add(-30*time.Second)) {
		return c.token, nil
	}

	token, expiresAt, err := c.exchangeToken(ctx)
	if err != nil {
		return "", err
	}
	c.token = token
	c.tokenExpiresAt = expiresAt
	return token, nil
}

func (c *Client) exchangeToken(ctx context.Context) (string, time.Time, error) {
	scope := c.scope
	if scope == "" {
		scope = bigQueryScope
	}
	policy := &tokenTransport{ctx: ctx, client: c.client}
	tokenClient := *c.client
	tokenClient.Transport = policy
	config := jwt.Config{Email: c.serviceAccount.ClientEmail, PrivateKey: []byte(c.serviceAccount.PrivateKey), Scopes: []string{scope}, TokenURL: c.tokenURL}
	token, err := config.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, &tokenClient)).Token()
	if err != nil {
		if policy.err != nil {
			return "", time.Time{}, mapTokenExchangeError(policy.err)
		}
		return "", time.Time{}, xerror.InvalidCredentials().Detail("Invalid BigQuery service account or token response")
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", time.Time{}, xerror.InvalidCredentials().Detail("BigQuery token response missing access token")
	}
	expiry := token.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}
	return token.AccessToken, expiry, nil
}

func parseCredentials(creds Credentials) (serviceAccount, error) {
	if creds.AccessToken != "" {
		if strings.TrimSpace(creds.AccessToken) == "" || creds.ServiceAccountJSON != "" {
			return serviceAccount{}, xerror.InvalidCredentials().Detail("BigQuery requires exactly one authentication method")
		}
		return serviceAccount{}, nil
	}
	return parseServiceAccount(creds.ServiceAccountJSON)
}

func parseServiceAccount(raw string) (serviceAccount, error) {
	if strings.TrimSpace(raw) == "" {
		return serviceAccount{}, xerror.InvalidCredentials().Detail(
			"BigQuery service_account_json is required",
		)
	}

	var account serviceAccount
	if err := json.Unmarshal([]byte(raw), &account); err != nil {
		return serviceAccount{}, xerror.InvalidCredentials().Detail(
			"BigQuery service_account_json must be valid JSON",
		)
	}
	if strings.TrimSpace(account.ClientEmail) == "" {
		return serviceAccount{}, xerror.InvalidCredentials().Detail(
			"BigQuery service_account_json client_email is required",
		)
	}
	if strings.TrimSpace(account.PrivateKey) == "" {
		return serviceAccount{}, xerror.InvalidCredentials().Detail(
			"BigQuery service_account_json private_key is required",
		)
	}

	return account, nil
}

func parseProviderURL(value, field string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).With(err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detailf(
			"%s must use http or https",
			field,
		)
	}
	if parsed.Hostname() == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detailf("%s host is required", field)
	}
	return parsed, nil
}

func validateProviderHost(ctx context.Context, hostname, field string) error {
	if err := hostguard.ValidateResolvedPublicHost(ctx, hostname); err != nil {
		return xerror.InvalidInput(xerror.DataSource).Detailf(
			"BigQuery %s blocked by ssrf protection: %w",
			field,
			err,
		)
	}
	return nil
}

func providerTransport(allowPrivateHosts bool) (http.RoundTripper, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, xerror.Unexpected().Detail("default transport has unexpected type")
	}

	cloned := defaultTransport.Clone()
	if allowPrivateHosts {
		return cloned, nil
	}

	cloned.DialContext = hostguard.NewDestinationPolicy(false).DialContext

	return cloned, nil
}

func buildQueryPayload(req QueryRequest) *bigquery.QueryRequest {
	payload := &bigquery.QueryRequest{
		Query: req.SQL, UseLegacySql: new(false), Location: req.Location,
		MaximumBytesBilled: req.MaximumBytesBilled, TimeoutMs: req.Timeout.Milliseconds(), MaxResults: int64(req.MaxResultRows),
	}
	if len(req.Parameters) > 0 {
		payload.ParameterMode = "NAMED"
		for _, parameter := range req.Parameters {
			value := &bigquery.QueryParameterValue{Value: fmt.Sprint(parameter.Value), ForceSendFields: []string{"Value"}}
			if parameter.Value == nil {
				value = &bigquery.QueryParameterValue{NullFields: []string{"Value"}}
			}
			payload.QueryParameters = append(payload.QueryParameters, &bigquery.QueryParameter{
				Name: parameter.Name, ParameterType: &bigquery.QueryParameterType{Type: parameter.Type}, ParameterValue: value,
			})
		}
	}
	return payload
}

func buildInsertRowsPayload(req InsertRowsRequest) *bigquery.TableDataInsertAllRequest {
	payload := &bigquery.TableDataInsertAllRequest{ForceSendFields: []string{"SkipInvalidRows", "IgnoreUnknownValues"}}
	for idx, row := range req.Rows {
		item := &bigquery.TableDataInsertAllRequestRows{Json: make(map[string]bigquery.JsonValue, len(row))}
		for name, value := range row {
			item.Json[name] = value
		}
		if req.InsertIDPrefix != "" {
			item.InsertId = fmt.Sprintf("%s-%d", req.InsertIDPrefix, idx)
		}
		payload.Rows = append(payload.Rows, item)
	}
	return payload
}

func fieldsFromResponse(body []byte) ([]Field, error) {
	// SDK integer fields erase absent precision/scale; observation must preserve presence.
	var response struct{ Schema struct{ Fields []Field } }
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, xerror.Unexpected().Detail("Invalid Google provider schema")
	}
	return response.Schema.Fields, nil
}

func appendProviderRows(queryResult *QueryResult, rows []*bigquery.TableRow, maxRows int, remainingBytes *int64) error {
	lastField := make(map[string]int, len(queryResult.Schema))
	for i, field := range queryResult.Schema {
		lastField[field.Name] = i
	}
	keyBytes := int64(2)
	for name := range lastField {
		encoded, err := json.Marshal(name)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		keyBytes += int64(len(encoded) + 1)
	}
	if len(lastField) > 1 {
		keyBytes += int64(len(lastField) - 1)
	}
	for _, row := range rows {
		if maxRows > 0 && len(queryResult.Rows) >= maxRows {
			break
		}
		rowKeyBytes := keyBytes
		if len(queryResult.Rows) > 0 {
			rowKeyBytes++
		}
		// Each retained field needs at least one value byte before map allocation.
		if rowKeyBytes+int64(len(lastField)) > *remainingBytes {
			return xerror.ExecutionResponseSizeExceeded()
		}
		*remainingBytes -= rowKeyBytes
		item := make(map[string]any, len(lastField))
		for name, idx := range lastField {
			var value any
			if row != nil && idx < len(row.F) && row.F[idx] != nil {
				value = row.F[idx].V
			}
			if text, ok := value.(string); ok && int64(len(text)) > *remainingBytes {
				return xerror.ExecutionResponseSizeExceeded()
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return xerror.Unexpected().With(err)
			}
			if int64(len(encoded)) > *remainingBytes {
				return xerror.ExecutionResponseSizeExceeded()
			}
			*remainingBytes -= int64(len(encoded))
			item[name] = value
		}
		queryResult.Rows = append(queryResult.Rows, item)
	}
	return nil
}

func remainingSchemaResponseBytes(ctx context.Context) (int64, error) {
	remaining := schemaquota.RemainingBytes(ctx, defaultMaxResponseBytes)
	if remaining <= 0 {
		return 0, schemaquota.Charge(ctx, 0, 1)
	}
	return int64(remaining), nil
}

func effectiveMaxResponseBytes(limit int64) int64 {
	if limit > 0 {
		return limit
	}
	return defaultMaxResponseBytes
}

const streamingBillingPublicDetail = "BigQuery streaming inserts require billing to be enabled for this Google Cloud project."

func mapProviderError(resp *http.Response, body []byte) error {
	status := resp.StatusCode
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	if status == http.StatusForbidden {
		var response struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &response) == nil && strings.Contains(response.Error.Message, "Streaming insert is not allowed in the free tier") {
			return xerror.Forbidden(xerror.DataSource).WithPublicDetail(streamingBillingPublicDetail)
		}
	}
	// BigQuery query/token rejection retains its resource-specific error identity.
	if status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return xerror.InvalidInput(xerror.Query).Detailf("BigQuery request failed with status %d", status)
	}
	if status >= 300 && status < 400 {
		return xerror.Unavailable(xerror.DataSource)
	}
	return googleclient.StatusError(status)
}

func mapTokenExchangeError(err error) error {
	if xerror.Is(err, xerror.InvalidInput(xerror.Query)) {
		return xerror.InvalidCredentials().With(err)
	}
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
