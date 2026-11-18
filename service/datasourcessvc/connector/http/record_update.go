package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func (c *Connector) executeRecordUpdate(ctx context.Context, statement json.RawMessage, update *sinkbinding.HTTPRecordUpdate, limits types.ExecutionLimits) *result.ExecutionResult {
	invalid := func() *result.ExecutionResult {
		return result.NewHTTPResult(nil, 0, xerror.InvalidInput(xerror.Query).WithPublicDetail("Invalid HTTP company update."))
	}
	uncertain := func() *result.ExecutionResult {
		return result.NewHTTPResult(nil, 0, xerror.HTTPAcknowledgementUncertain().WithPublicDetail("The destination company update could not be confirmed."))
	}
	var operation map[string]any
	if !sinkbinding.IsSinkExecution(ctx) || limits.ReadOnly || json.Unmarshal(statement, &operation) != nil || !sinkbinding.ValidRecordUpdateOperation(operation) ||
		strings.TrimSpace(update.RecordID) == "" || update.RecordID == "." || update.RecordID == ".." || len(update.Properties) == 0 {
		return invalid()
	}
	target, err := url.Parse(c.config.BaseURL)
	if err != nil || target.ForceQuery || target.RawQuery != "" || target.Fragment != "" {
		return invalid()
	}
	path, err := substitutePath(operation["path"].(string), map[string]any{"record_id": update.RecordID})
	if err != nil {
		return invalid()
	}
	targetURL := strings.TrimRight(target.String(), "/") + path
	method := http.MethodPatch
	var body []byte
	if update.Reconcile {
		method = http.MethodGet
		query := url.Values{"properties": {strings.Join(slices.Sorted(maps.Keys(update.Properties)), ",")}}
		targetURL += "?" + query.Encode()
	} else {
		body, err = json.Marshal(map[string]any{"properties": update.Properties})
		if err != nil || len(body) > 1<<20 || (limits.HTTPMaxRequestBytes > 0 && int64(len(body)) > limits.HTTPMaxRequestBytes) {
			return invalid()
		}
	}
	if c.oauthDestination != "" && httpoauth.ValidateDestination(c.oauthDestination, targetURL) != nil {
		return invalid()
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		return invalid()
	}
	req.Header.Set("Accept", httpContentTypeJSON)
	if !update.Reconcile {
		// A credential header must not make the transport replay this PATCH.
		req.GetBody = nil
		req.Header.Set("Content-Type", httpContentTypeJSON)
	}
	c.authenticate(req)
	client := *c.client
	for _, timeout := range []time.Duration{limits.HTTPTimeout, limits.ConnectorTimeout, 30 * time.Second} {
		if timeout > 0 && (client.Timeout == 0 || timeout < client.Timeout) {
			client.Timeout = timeout
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return uncertain()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return uncertain()
	}

	maxBytes := min(effectiveHTTPMaxResponseBytes(limits), 1<<20)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	var company struct {
		ID         string             `json:"id"`
		Properties map[string]*string `json:"properties"`
	}
	if err != nil || int64(len(raw)) > maxBytes || c.responseExposesSecret(resp.Header, raw) || json.Unmarshal(raw, &company) != nil || company.ID != update.RecordID {
		return uncertain()
	}
	status := http.StatusOK
	for name, expected := range update.Properties {
		actual, present := company.Properties[name]
		if !present {
			return uncertain()
		}
		// HubSpot represents cleared properties as null or an empty string.
		if (actual == nil && expected != "") || (actual != nil && *actual != expected) {
			if !update.Reconcile {
				return uncertain()
			}
			status = http.StatusConflict
		}
	}
	return result.NewHTTPResult(&result.HTTPResult{StatusCode: status}, 0, nil)
}
