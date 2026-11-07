package spec

import (
	"fmt"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/model"
)

const (
	OperationTypeSQL             = catalog.OperationTypeSQL
	OperationTypeBigQuery        = catalog.OperationTypeBigQuery
	OperationTypeGoogleWorkspace = catalog.OperationTypeGoogleWorkspace
	OperationTypeSnowflake       = catalog.OperationTypeSnowflake
	OperationTypeHTTP            = catalog.OperationTypeHTTP
	OperationTypeFTP             = catalog.OperationTypeFTP
	OperationTypeGit             = catalog.OperationTypeGit
	OperationTypeMongoDB         = catalog.OperationTypeMongoDB
	OperationTypeHubSpot         = catalog.OperationTypeHubSpot
	OperationTypeS3              = catalog.OperationTypeS3
)

// ConnectorCapability is the pipeline-facing subset of connector catalog facts.
// It lets same-repository frontend and builder packages consume connector
// behavior without importing the connector implementation boundary directly.
// If the frontend becomes a separately versioned artifact or repository, this
// descriptor contract belongs in OpenAPI or generated API metadata instead.
type ConnectorCapability struct {
	Capabilities                     model.ConnectorCapabilityDetails  `json:"capabilities"`
	SupportLevel                     string                            `json:"support_level"`
	DataSourceType                   model.DataSourceType              `json:"type"`
	SchemaFamily                     string                            `json:"schema_family"`
	QueryOperationType               string                            `json:"query_operation_type"`
	SinkBackend                      model.SinkBackend                 `json:"sink_backend"`
	SupportsPipelineSource           bool                              `json:"supports_pipeline_source"`
	SupportsPipelineSink             bool                              `json:"supports_pipeline_sink"`
	SupportedSinkReplayStrategies    []SinkIdempotencyClass            `json:"supported_sink_replay_strategies"`
	IncrementalSourceCheckpointKinds []model.IncrementalCheckpointKind `json:"incremental_source_checkpoint_kinds"`
	ForcedReadOnly                   bool                              `json:"forced_read_only"`
}

// CapabilityDetails formats catalog facts for both terminal and visual inspectors.
func (c ConnectorCapability) CapabilityDetails() []string {
	d := c.Capabilities
	sourcePayloads := make([]string, 0, len(d.SourcePayloads))
	for _, representation := range d.SourcePayloads {
		sourcePayloads = append(sourcePayloads, representation.String())
	}
	lines := []string{
		"Read operations: " + capabilityList(d.ReadOperations),
		"Write operations: " + capabilityList(d.WriteOperations),
		"Load modes: " + capabilityList(d.LoadModes),
		"Source payloads: " + capabilityList(sourcePayloads),
	}
	for _, support := range d.SinkPayloads {
		lines = append(lines, fmt.Sprintf("Sink %s: %s. %s", support.Representation, support.Mode, support.Limitation))
	}
	lines = append(lines,
		fmt.Sprintf("Pagination: %s; %s. %s", capabilityList(d.Pagination.Methods), d.Pagination.Scope, d.Pagination.Limitation),
		"Throttling: "+d.Pagination.Throttling,
		"Authentication: "+capabilityList(d.Authentication.Modes),
		"Renewal: "+d.Authentication.Renewal,
		"Recovery: "+d.Authentication.Recovery,
	)
	for _, limitation := range d.Limitations {
		lines = append(lines, "Limit: "+limitation)
	}
	return lines
}

func capabilityList(values []string) string {
	if len(values) == 0 {
		return "unsupported"
	}
	return strings.Join(values, ", ")
}

// SupportSummary uses the same facts for CLI output and connector selectors.
func (c ConnectorCapability) SupportSummary() string {
	kinds := []string{}
	if c.SupportsPipelineSource {
		kinds = append(kinds, "full read")
		for _, kind := range c.IncrementalSourceCheckpointKinds {
			kinds = append(kinds, kind.String())
		}
	}
	strategies := make([]string, 0, len(c.SupportedSinkReplayStrategies))
	for _, strategy := range c.SupportedSinkReplayStrategies {
		if c.SupportsPipelineSink && !c.ForcedReadOnly {
			strategies = append(strategies, strategy.String())
		}
	}
	if len(kinds) == 0 {
		kinds = append(kinds, "unsupported")
	}
	if len(strategies) == 0 {
		strategies = append(strategies, "unsupported")
	}
	return fmt.Sprintf("%s; source/read: %t; sink/write: %t; modes: %s; replay: %s",
		c.SupportLevel, c.SupportsPipelineSource, c.SupportsPipelineSink && !c.ForcedReadOnly,
		strings.Join(kinds, ", "), strings.Join(strategies, ", "))
}

// AllConnectorCapabilities returns catalog connector facts in a pipeline-owned shape.
// Slice fields are copied so callers can sort or filter without mutating catalog state.
func AllConnectorCapabilities() []ConnectorCapability {
	descriptors := catalog.AllDescriptors()
	capabilities := make([]ConnectorCapability, 0, len(descriptors))
	for _, descriptor := range descriptors {
		incrementalKinds := append(
			[]model.IncrementalCheckpointKind(nil),
			descriptor.IncrementalSourceCheckpointKinds...,
		)
		replayStrategies := sinkIdempotencyClasses(descriptor.SupportedSinkReplayStrategies)
		capabilities = append(capabilities, ConnectorCapability{
			Capabilities:                     descriptor.Capabilities.Clone(),
			SupportLevel:                     descriptor.SupportLevel.String(),
			DataSourceType:                   descriptor.Type,
			SchemaFamily:                     string(descriptor.SchemaFamily),
			QueryOperationType:               descriptor.QueryOperationType,
			SinkBackend:                      descriptor.SinkBackend,
			SupportsPipelineSource:           descriptor.SupportsPipelineSource,
			SupportsPipelineSink:             descriptor.SupportsPipelineSink,
			SupportedSinkReplayStrategies:    replayStrategies,
			IncrementalSourceCheckpointKinds: incrementalKinds,
			ForcedReadOnly:                   descriptor.ForcedReadOnly,
		})
	}

	return capabilities
}

// sinkIdempotencyClasses translates catalog-owned mechanisms into the neutral
// pipeline contract exposed to builders and frontend-facing callers.
func sinkIdempotencyClasses(strategies []catalog.SinkReplayStrategy) []SinkIdempotencyClass {
	classes := make([]SinkIdempotencyClass, 0, len(strategies))
	for _, strategy := range strategies {
		class, err := SinkIdempotencyClassString(strategy.String())
		if err == nil && class != SinkIdempotencyClassUnknown {
			classes = append(classes, class)
		}
	}
	return classes
}

// IsSupportedQueryOperationType reports whether the catalog knows the operation type.
func IsSupportedQueryOperationType(operationType string) bool {
	return catalog.IsSupportedQueryOperationType(operationType)
}

// IsSupportedSinkOperationType reports whether the operation type can write to a sink.
func IsSupportedSinkOperationType(operationType string) bool {
	_, supported := SinkBackendForOperationType(operationType)
	return supported
}

// SinkBackendForOperationType returns the sink backend attached to an operation type.
func SinkBackendForOperationType(operationType string) (model.SinkBackend, bool) {
	return catalog.SinkBackendForOperationType(operationType)
}

// SupportsSinkIdempotencyClass reports whether the concrete sink connector
// supports the replay mechanism declared by a pipeline sink step.
func SupportsSinkIdempotencyClass(
	dataSourceType model.DataSourceType,
	idempotencyClass SinkIdempotencyClass,
) bool {
	strategy, err := catalog.SinkReplayStrategyString(idempotencyClass.String())
	if err != nil {
		return false
	}

	return catalog.SupportsSinkReplayStrategy(
		dataSourceType,
		strategy,
	)
}

// SupportsIncrementalSourceCheckpointKind parses a checkpoint kind before capability lookup.
func SupportsIncrementalSourceCheckpointKind(operationType, checkpointKind string) bool {
	kind, err := model.IncrementalCheckpointKindString(checkpointKind)
	if err != nil {
		return false
	}

	return SupportsIncrementalSourceCheckpoint(operationType, kind)
}

// SupportsIncrementalSourceCheckpoint reports whether an operation type accepts the checkpoint kind.
func SupportsIncrementalSourceCheckpoint(
	operationType string,
	checkpointKind model.IncrementalCheckpointKind,
) bool {
	return catalog.SupportsIncrementalSourceCheckpointKind(
		operationType,
		checkpointKind,
	)
}
