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
	out := c.recordUpdateRequest(ctx, statement, update, limits)
	reply, ok := out.HTTP()
	if out.Error() != nil || !ok || reply == nil || reply.StatusCode != http.StatusNoContent {
		return out
	}
	readback := *update
	readback.Reconcile = true
	out = c.recordUpdateRequest(ctx, statement, &readback, limits)
	reply, ok = out.HTTP()
	if out.Error() == nil && ok && reply != nil && reply.StatusCode == http.StatusConflict {
		return uncertainRecordUpdate()
	}
	return out
}

func uncertainRecordUpdate() *result.ExecutionResult {
	return result.NewHTTPResult(nil, 0, xerror.HTTPAcknowledgementUncertain().WithPublicDetail("The destination record update could not be confirmed."))
}

func (c *Connector) recordUpdateRequest(ctx context.Context, statement json.RawMessage, update *sinkbinding.HTTPRecordUpdate, limits types.ExecutionLimits) *result.ExecutionResult {
	invalid := func() *result.ExecutionResult {
		return result.NewHTTPResult(nil, 0, xerror.InvalidInput(xerror.Query).WithPublicDetail("Invalid HTTP record update."))
	}
	var operation map[string]any
	if !update.Readback.Valid() || !sinkbinding.IsSinkExecution(ctx) || limits.ReadOnly || json.Unmarshal(statement, &operation) != nil || !sinkbinding.ValidRecordUpdateOperation(operation) ||
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
	targetURL := strings.TrimRight(target.String(), "/") + "/" + strings.TrimLeft(path, "/")
	method := operation["method"].(string)
	var body []byte
	if update.Reconcile {
		method = http.MethodGet
		if update.Readback.SelectParameter != "" {
			query := url.Values{update.Readback.SelectParameter: {strings.Join(slices.Sorted(maps.Keys(update.Properties)), ",")}}
			targetURL += "?" + query.Encode()
		}
	} else {
		var payload any = update.Properties
		if wrapper, ok := operation["body"].(map[string]any); ok {
			for name := range wrapper {
				payload = map[string]any{name: update.Properties}
			}
		}
		body, err = json.Marshal(payload)
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
		// A credential header must not make the transport replay this write.
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
		return uncertainRecordUpdate()
	}
	defer func() { _ = resp.Body.Close() }()
	if !update.Reconcile && resp.StatusCode == http.StatusNoContent {
		return result.NewHTTPResult(&result.HTTPResult{StatusCode: http.StatusNoContent}, 0, nil)
	}
	if resp.StatusCode != http.StatusOK && (update.Reconcile || resp.StatusCode != http.StatusCreated) {
		return uncertainRecordUpdate()
	}

	maxBytes := min(effectiveHTTPMaxResponseBytes(limits), 1<<20)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	var record map[string]json.RawMessage
	if err != nil || int64(len(raw)) > maxBytes || c.responseExposesSecret(resp.Header, raw) || json.Unmarshal(raw, &record) != nil {
		return uncertainRecordUpdate()
	}
	var id *string
	if json.Unmarshal(record[update.Readback.IdentityField], &id) != nil || id == nil || *id != update.RecordID {
		return uncertainRecordUpdate()
	}
	fields := record
	if update.Readback.FieldsObject != "" {
		fields = nil
		if json.Unmarshal(record[update.Readback.FieldsObject], &fields) != nil || fields == nil {
			return uncertainRecordUpdate()
		}
	}
	status := http.StatusOK
	for name, expected := range update.Properties {
		var actual *string
		if json.Unmarshal(fields[name], &actual) != nil || actual == nil {
			return uncertainRecordUpdate()
		}
		if *actual != expected {
			if !update.Reconcile {
				return uncertainRecordUpdate()
			}
			status = http.StatusConflict
		}
	}
	return result.NewHTTPResult(&result.HTTPResult{StatusCode: status}, 0, nil)
}
