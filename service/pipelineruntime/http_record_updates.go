package pipelineruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxRecordUpdateBytes = 1 << 20

type httpRecordUpdate struct {
	id         string
	properties map[string]string
}

func prepareHTTPRecordUpdates(ctx context.Context, rows []map[string]any, cfg *pipelinespec.RecordUpdates, wrapperBytes int) ([]httpRecordUpdate, error) {
	if len(rows) > cfg.MaxRecords {
		return nil, errSinkConfigurationInvalid.Detail("record update count exceeds max_records")
	}
	seen := make(map[string]bool, len(rows))
	updates := make([]httpRecordUpdate, 0, len(rows))
	remaining := maxRecordUpdateBytes
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		id, ok := row[cfg.IdentityField].(string)
		if !ok || id == "" || len(id) > 256 || strings.TrimSpace(id) != id || id == "." || id == ".." || !utf8.ValidString(id) || strings.ContainsAny(id, "\r\n\x00") {
			return nil, errSinkConfigurationInvalid.Detail("record update requires a valid string identity")
		}
		if seen[id] {
			return nil, errSinkConfigurationInvalid.Detail("duplicate record update identity")
		}
		seen[id] = true
		props := make(map[string]string, len(cfg.Fields))
		rawBytes := 0
		for source, destination := range cfg.Fields {
			value, ok := row[source].(string)
			if !ok || len(value) > remaining-rawBytes || !utf8.ValidString(value) {
				return nil, errSinkConfigurationInvalid.Detail("record update requires bounded string properties")
			}
			rawBytes += len(value)
			props[destination] = value
		}
		encoded, err := json.Marshal(props)
		if err != nil {
			return nil, errSinkConfigurationInvalid.Detail("invalid record update properties")
		}
		remaining -= len(encoded) + len(id) + wrapperBytes
		if remaining < 0 {
			return nil, errSinkConfigurationInvalid.Detail("record update batch exceeds byte limit")
		}
		updates = append(updates, httpRecordUpdate{id, props})
	}
	return updates, nil
}

func (s *sinkRuntime) executeHTTPRecordUpdates(ctx context.Context, input *SinkRuntimeInput) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cfg, err := pipelinespec.ParseRecordUpdates(input.SinkConfig)
	if err != nil {
		return err
	}
	if input.IdempotencyClass != pipelinespec.SinkIdempotencyClassDeduplicatedByLedger {
		return errSinkConfigurationInvalid.Detail("record updates require deduplicated_by_ledger")
	}
	if input.SinkBackend != model.SinkBackendHTTP || input.SinkOperationRef != "" || !sinkbinding.ValidRecordUpdateOperation(input.SinkOperationInline) || input.SinkDataSourceSlug == "" {
		return errSinkConfigurationInvalid.Detail("record updates require an inline HTTP destination")
	}
	payload, ok := input.SourceResult.Payload()
	if !ok || payload == nil || payload.Representation != model.ContractRepresentationRows {
		return errSinkConfigurationInvalid.Detail("record updates require rows")
	}
	// The validated body contains exactly one input token, optionally wrapped in an object.
	template, err := json.Marshal(input.SinkOperationInline["body"])
	if err != nil {
		return errSinkConfigurationInvalid.Detail("invalid HTTP record update body")
	}
	updates, err := prepareHTTPRecordUpdates(ctx, payload.Rows, cfg, len(template)-len(`"{{input}}"`))
	if err != nil {
		return err
	}
	for _, update := range updates {
		input.DeliveryOutcome = "unknown"
		if err := ctx.Err(); err != nil {
			return xerror.Unexpected().With(err)
		}
		err := s.executeRecordEffect(ctx, input, update.id, func(ctx context.Context) error {
			_, err := s.dispatchRecordUpdate(ctx, input, update, false, cfg.Readback)
			return err
		}, func(ctx context.Context) (bool, error) {
			return s.dispatchRecordUpdate(ctx, input, update, true, cfg.Readback)
		})
		if err != nil {
			return err
		}
	}
	input.DeliveryOutcome = "confirmed"
	if len(updates) == 0 {
		input.DeliveryOutcome = "no_op"
	}
	return nil
}

func (s *sinkRuntime) dispatchRecordUpdate(ctx context.Context, input *SinkRuntimeInput, update httpRecordUpdate, reconcile bool, readback sinkbinding.RecordReadback) (bool, error) {
	executor, ok := s.queryExecutor.(interface {
		ExecuteInline(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error)
	})
	if !ok {
		return false, errSinkNotConfigured.Detail("HTTP inline sink executor is required")
	}
	ctx = sinkbinding.WithHTTPRecordUpdate(ctx, update.id, update.properties, reconcile, readback)
	output, err := executor.ExecuteInline(ctx, input.OrgSlug, input.SinkDataSourceSlug, input.SinkOperationInline, nil)
	if err != nil || output == nil || output.Error() != nil {
		if ctx.Err() != nil {
			return false, xerror.Unexpected().With(ctx.Err())
		}
		return false, xerror.HTTPAcknowledgementUncertain().WithPublicDetail("The destination record update could not be confirmed.")
	}
	response, ok := output.HTTP()
	if ok && response != nil {
		if response.StatusCode == http.StatusOK {
			return true, nil
		}
		if reconcile && response.StatusCode == http.StatusConflict {
			return false, nil
		}
	}
	return false, xerror.HTTPAcknowledgementUncertain().Detail("unexpected HTTP record update response")
}
