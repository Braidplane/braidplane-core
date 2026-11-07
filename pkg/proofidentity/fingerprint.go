package proofidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func BindingFingerprint(resolutions []model.ResolvedPipelineDataSourceRef) (string, error) {
	return digest(resolutions)
}

func CapabilityFingerprint(pipeline *pipelinespec.Pipeline, resolutions []model.ResolvedPipelineDataSourceRef) (string, error) {
	types := make(map[slug.Slug]model.DataSourceType, len(resolutions))
	for _, resolution := range resolutions {
		types[resolution.RefSlug] = resolution.DataSourceType
	}
	type capability struct{ Step, Role, Ref, Operation, DataSourceType, Idempotency string }
	items := make([]capability, 0, len(pipeline.Steps))
	for _, step := range pipeline.Steps {
		if step.Query != nil {
			items = append(items, capability{step.Slug.String(), "source", step.Query.DataSourceSlug.String(), step.Query.OperationType, types[step.Query.DataSourceSlug].String(), ""})
		}
		if step.Sink != nil {
			items = append(items, capability{step.Slug.String(), "sink", step.Sink.DataSourceSlug.String(), step.Sink.OperationType, types[step.Sink.DataSourceSlug].String(), step.Sink.IdempotencyClass.String()})
		}
	}
	// Preserve the fingerprint format already emitted by proof receipts.
	sort.Slice(items, func(i, j int) bool { return items[i].Step+items[i].Role < items[j].Step+items[j].Role })
	return digest(items)
}

func digest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
