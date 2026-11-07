package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"golang.org/x/net/http/httpguts"
)

type httpWriteRetry struct {
	Verified            bool   `json:"verified"`
	ProviderContractURL string `json:"provider_contract_url"`
	KeyHeader           string `json:"key_header"`
	RetentionSeconds    int    `json:"retention_seconds"`
	PayloadConflict     string `json:"payload_conflict"`
}

func writeRetryRefused() error {
	return xerror.InvalidInput(xerror.Query).WithPublicDetail("HTTP write retry refused: requires a verified provider contract, durable keyed sink effect, exact runtime key header binding, replayable body and unexpired retention")
}

func validateWriteRetry(ctx context.Context, raw json.RawMessage, definition httpQueryData, params map[string]any, req *http.Request) (time.Time, error) {
	var policy *httpWriteRetry
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil || policy == nil {
		return time.Time{}, writeRetryRefused()
	}
	// Recovery restores input and the effect key; other bindings can change between runs.
	requestDefinition, err := json.Marshal([]any{definition.Path, definition.QueryParams, definition.Headers, definition.Body})
	if err != nil {
		return time.Time{}, writeRetryRefused()
	}
	for _, name := range param.ExtractParamNames(string(requestDefinition)) {
		if name != sinkbinding.Input && name != "runtime.sink.idempotency_key" {
			return time.Time{}, writeRetryRefused()
		}
	}
	contract, err := url.Parse(policy.ProviderContractURL)
	if err != nil || contract.Scheme != "https" || contract.Hostname() == "" || contract.User != nil ||
		!policy.Verified || policy.PayloadConflict != "reject" || policy.RetentionSeconds < 1 || policy.RetentionSeconds > 604800 ||
		!runtimevars.IsSinkExecution(ctx) || fmt.Sprint(params[sinkbinding.InternalIdempotencyClass]) != "idempotent_with_key" ||
		isIdempotentHTTPMethod(req.Method) || (req.Body != nil && req.GetBody == nil) {
		return time.Time{}, writeRetryRefused()
	}
	if !httpguts.ValidHeaderFieldName(policy.KeyHeader) || len(policy.KeyHeader) > 128 ||
		reservedSecretHeader(strings.ToLower(policy.KeyHeader)) || strings.EqualFold(policy.KeyHeader, "Authorization") ||
		req.Header.Get("Connection") != "" {
		return time.Time{}, writeRetryRefused()
	}
	key, ok := params[sinkbinding.InternalIdempotencyKey].(string)
	if !ok || strings.TrimSpace(key) == "" || req.Header.Get(policy.KeyHeader) != key ||
		headerValueIgnoreCase(definition.Headers, policy.KeyHeader) != "{{runtime.sink.idempotency_key}}" {
		return time.Time{}, writeRetryRefused()
	}
	started, err := time.Parse(time.RFC3339Nano, fmt.Sprint(params[sinkbinding.InternalEffectStartedAt]))
	expires := started.Add(time.Duration(policy.RetentionSeconds) * time.Second)
	if err != nil || started.After(time.Now()) || !expires.After(time.Now()) {
		return time.Time{}, writeRetryRefused()
	}
	return expires, nil
}
