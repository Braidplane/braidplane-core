package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	sdk "github.com/stripe/stripe-go/v87"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const maxResponseBytes = 10 << 20

type Config struct {
	Mode string `json:"mode" validate:"required,oneof=test live"`
}
type Credentials struct {
	APIKey string `json:"api_key" validate:"required"`
}

var (
	keyBody    = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	cursorBody = regexp.MustCompile(`^[a-zA-Z0-9_]{1,255}$`)
)

func Validate(cfg *Config, creds Credentials) error {
	if cfg == nil || (cfg.Mode != "test" && cfg.Mode != "live") {
		return xerror.InvalidInput(xerror.DataSource).Detail("Stripe mode must be test or live")
	}
	suffix, ok := strings.CutPrefix(creds.APIKey, "rk_"+cfg.Mode+"_")
	if !ok || !keyBody.MatchString(suffix) {
		return xerror.InvalidCredentials().Detail("Stripe requires a restricted API key matching the configured mode")
	}
	return nil
}

type Connector struct {
	key       string
	live      bool
	transport http.RoundTripper
}

func New(_ context.Context, cfg Config, creds Credentials) (*Connector, error) {
	if err := Validate(&cfg, creds); err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: hostguard.DialPublicHost, ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second}
	return &Connector{key: creds.APIKey, live: cfg.Mode == "live", transport: transport}, nil
}

func (c *Connector) Close() error {
	(&http.Client{Transport: c.transport}).CloseIdleConnections()
	return nil
}

type statement struct {
	Type          string `json:"type"`
	ObjectType    string `json:"object_type"`
	Operation     string `json:"operation"`
	StartingAfter string `json:"starting_after"`
	Limit         *int   `json:"limit"`
}

func parseStatement(raw json.RawMessage) (statement, resource, error) {
	var s statement
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	invalid := xerror.InvalidInput(xerror.Query).Detail("invalid Stripe list statement, object, cursor or limit")
	if dec.Decode(&s) != nil || dec.Decode(&struct{}{}) != io.EOF ||
		(s.Type != "" && s.Type != "stripe") || (s.Operation != "" && s.Operation != "list") ||
		(s.Limit != nil && (*s.Limit < 1 || *s.Limit > 100)) {
		return statement{}, resource{}, invalid
	}
	_, binding := param.ExactParamName(s.StartingAfter)
	for _, r := range resources {
		if r.name == s.ObjectType && (validCursor(s.StartingAfter, r) || binding) {
			return s, r, nil
		}
	}
	return statement{}, resource{}, invalid
}

func validCursor(s string, r resource) bool {
	return s == "" || (strings.HasPrefix(s, r.prefix) && len(s) > len(r.prefix) && cursorBody.MatchString(s))
}

func IsReadStatement(raw json.RawMessage) bool {
	_, _, err := parseStatement(raw)
	return err == nil
}

func (c *Connector) TestConnection(ctx context.Context) error {
	_, err := c.list(ctx, resources[0], "", 1, maxResponseBytes)
	return err
}

func (c *Connector) Execute(ctx context.Context, raw json.RawMessage, params map[string]types.TypedParam, limits ...types.ExecutionLimits) (*result.ExecutionResult, error) {
	start := time.Now()
	exec := types.ResolveExecutionLimits(limits...)
	for _, timeout := range []time.Duration{exec.ConnectorTimeout, exec.HTTPTimeout} {
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	s, r, err := parseStatement(raw)
	if err == nil {
		var rows *result.ObjectRowsResult
		rows, err = c.executeList(ctx, s, r, params, exec)
		if err == nil {
			out := result.NewObjectRowsResult("stripe", rows, time.Since(start).Milliseconds(), nil)
			if err = out.CheckResponseSize(exec.MaxResponseBytes); err == nil {
				return out, nil
			}
		}
	}
	empty := &result.ObjectRowsResult{Rows: []map[string]any{}, ObjectType: r.name, Operation: "list"}
	return result.NewObjectRowsResult("stripe", empty, time.Since(start).Milliseconds(), err), nil
}

func (c *Connector) executeList(ctx context.Context, s statement, r resource, params map[string]types.TypedParam, exec types.ExecutionLimits) (*result.ObjectRowsResult, error) {
	limit := 100
	if s.Limit != nil {
		limit = *s.Limit
	}
	if exec.MaxResultRows > 0 {
		limit = min(limit, exec.MaxResultRows)
	}
	budget := int64(maxResponseBytes)
	for _, b := range []int64{exec.HTTPMaxResponseBytes, exec.MaxResponseBytes} {
		if b > 0 {
			budget = min(budget, b)
		}
	}
	cursor := s.StartingAfter
	if name, binding := param.ExactParamName(cursor); binding {
		var ok bool
		cursor, ok = params[name].Value.(string)
		if !ok || !validCursor(cursor, r) {
			return nil, xerror.InvalidInput(xerror.Query).Detail("Stripe cursor binding requires an object ID or empty string")
		}
	}
	return c.list(ctx, r, cursor, limit, budget)
}

type boundedTransport struct {
	base     http.RoundTripper
	maxBytes int64
}

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.maxBytes)
	return resp, nil
}

func statusError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials()
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).Detail("Stripe restricted key lacks read permission")
	case http.StatusTooManyRequests:
		return xerror.Unavailable(xerror.DataSource).Detail("Stripe rate limit reached (429); retry later")
	default:
		return xerror.ConnectionFailed(xerror.DataSource).Detailf("Stripe returned HTTP %d", status)
	}
}

type listPage struct {
	sdk.APIResource
	Object  string                       `json:"object"`
	Data    []map[string]json.RawMessage `json:"data"`
	HasMore *bool                        `json:"has_more"`
}

func (c *Connector) list(ctx context.Context, r resource, cursor string, limit int, budget int64) (*result.ObjectRowsResult, error) {
	client := &http.Client{
		Transport: boundedTransport{c.transport, budget}, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	backend := sdk.GetBackendWithConfig(sdk.APIBackend, &sdk.BackendConfig{
		HTTPClient:        client,
		MaxNetworkRetries: sdk.Int64(0),
		EnableTelemetry:   sdk.Bool(false), // Workers serve multiple organizations.
		LeveledLogger:     &sdk.LeveledLogger{Level: sdk.LevelNull},
	})
	p := &sdk.ListParams{Context: ctx, Limit: sdk.Int64(int64(limit))}
	if cursor != "" {
		p.StartingAfter = sdk.String(cursor)
	}
	var args sdk.ParamsContainer = p
	if r.name == "subscriptions" {
		args = &sdk.SubscriptionListParams{ListParams: *p, Status: sdk.String("all")}
	}
	var page listPage
	if err := backend.Call(http.MethodGet, r.path, c.key, args, &page); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var oversized *http.MaxBytesError
		if xerror.As(err, &oversized) {
			return nil, xerror.ExecutionResponseSizeExceeded()
		}
		var safe *xerror.Error
		if xerror.As(err, &safe) {
			return nil, safe
		}
		return nil, xerror.ConnectionFailed(xerror.DataSource).Detail("Stripe list request failed")
	}
	invalidPage := xerror.ConnectionFailed(xerror.DataSource).Detail("Stripe returned an invalid list page")
	if page.Object != "list" || page.Data == nil || page.HasMore == nil || (*page.HasMore && len(page.Data) == 0) {
		return nil, invalidPage
	}
	if len(page.Data) > limit {
		return nil, xerror.ExecutionRowLimitExceeded()
	}
	rows := &result.ObjectRowsResult{ObjectType: r.name, Operation: "list", Rows: make([]map[string]any, 0, len(page.Data)), HasMore: *page.HasMore}
	fields := r.properties()
	for _, f := range fields {
		rows.ColumnNames = append(rows.ColumnNames, f.Name)
	}
	for _, rec := range page.Data {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(fields))
		for _, f := range fields {
			raw := rec[f.Name]
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				row[f.Name] = nil
				continue
			}
			value, err := fieldValue(raw, f.Type)
			if err != nil {
				return nil, invalidPage
			}
			row[f.Name] = value
		}
		id, ok := row["id"].(string)
		if !ok || id == "" || !validCursor(id, r) || row["livemode"] != c.live {
			return nil, invalidPage
		}
		rows.Rows = append(rows.Rows, row)
		if rows.HasMore && len(rows.Rows) == len(page.Data) {
			rows.NextCursor = &id
		}
	}
	rows.RowCount = len(rows.Rows)
	return rows, nil
}

func fieldValue(raw json.RawMessage, kind string) (any, error) {
	switch kind {
	case "integer":
		var v int64
		err := json.Unmarshal(raw, &v)
		return v, err
	case "boolean":
		var v bool
		err := json.Unmarshal(raw, &v)
		return v, err
	default:
		var v string
		err := json.Unmarshal(raw, &v)
		return v, err
	}
}
