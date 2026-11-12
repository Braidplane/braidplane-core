package catalog

import "github.com/Braidplane/braidplane-core/pkg/model"

type SchemaFamily string

type SinkPayloadBinding string

const S3ResourceScopePolicyID = "s3/relative-key/v1"

const (
	SinkPayloadBindingEncoded   SinkPayloadBinding = "encoded"
	SinkPayloadBindingRowObject SinkPayloadBinding = "row_object"
)

type SinkResultKind string

const (
	SinkResultKindHTTP       SinkResultKind = "http"
	SinkResultKindObjectRows SinkResultKind = "object_rows"
)

type SinkResultCapability struct {
	Kind      SinkResultKind
	Operation string
}

// SinkReplayStrategy is the connector-level replay mechanism a sink operation
// may declare. Individual saved operations still select one strategy in the
// pipeline spec; this catalog list prevents UI and documentation from implying
// provider support that the adapter does not have.
//
//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=SinkReplayStrategy -json -text -linecomment -output=sink_replay_strategy_enumer.go
type SinkReplayStrategy int

const (
	SinkReplayStrategyUnknown              SinkReplayStrategy = iota // unknown
	SinkReplayStrategyNaturallyIdempotent                            // naturally_idempotent
	SinkReplayStrategyIdempotentWithKey                              // idempotent_with_key
	SinkReplayStrategyDeduplicatedByLedger                           // deduplicated_by_ledger
	SinkReplayStrategyUnsafe                                         // unsafe
)

const (
	SchemaFamilyUnknown     SchemaFamily = "unknown"
	SchemaFamilySQL         SchemaFamily = "sql"
	SchemaFamilySpreadsheet SchemaFamily = "spreadsheet"
	SchemaFamilyHTTP        SchemaFamily = "http"
	SchemaFamilyDocument    SchemaFamily = "document"
	SchemaFamilyFile        SchemaFamily = "file"
	SchemaFamilyObjectAPI   SchemaFamily = "object_api"
	SchemaFamilyObjectStore SchemaFamily = "object_store"
)

const (
	OperationTypeSQL             = "sql"
	OperationTypeBigQuery        = "bigquery"
	OperationTypeGoogleWorkspace = "google_workspace"
	OperationTypeHTTP            = "http"
	OperationTypeFTP             = "ftp"
	OperationTypeGit             = "git"
	OperationTypeMongoDB         = "mongodb"
	OperationTypeHubSpot         = "hubspot"
	OperationTypeS3              = "s3"
	OperationTypeSnowflake       = "snowflake"
)

// ProofMetadataCapability declares fresh selected-resource support separately
// from pipeline execution and cached schema assistance.
type ProofMetadataCapability struct {
	SourceClaimSuffix string
	SinkClaimSuffix   string
	Operations        []string
	UnsupportedReason string
}

type Descriptor struct {
	Capabilities                     model.ConnectorCapabilityDetails
	SupportLevel                     SupportLevel
	ProofMetadata                    ProofMetadataCapability
	Type                             model.DataSourceType
	SchemaFamily                     SchemaFamily
	QueryOperationType               string
	SinkBackend                      model.SinkBackend
	SupportsPipelineSource           bool
	SupportsPipelineSink             bool
	SupportedSinkReplayStrategies    []SinkReplayStrategy
	SinkPayloadBinding               SinkPayloadBinding
	SinkResult                       SinkResultCapability
	IncrementalSourceCheckpointKinds []model.IncrementalCheckpointKind
	ForcedReadOnly                   bool
}

// ResourceScopeCapability advertises confinement separately from ordinary
// execution. An unsupported mapping must never be treated as a fixed target.
type ResourceScopeCapability struct {
	PolicyID          string
	Representations   []model.ContractRepresentation
	FixedFields       []string
	DynamicSelectors  []string
	ExecutionValues   []string
	UnsupportedReason string
}

func (d Descriptor) ResourceScopeCapability() ResourceScopeCapability {
	if d.Type != model.DataSourceTypeS3 {
		return ResourceScopeCapability{UnsupportedReason: "connector has no validated resource scope policy"}
	}
	return ResourceScopeCapability{
		PolicyID:         S3ResourceScopePolicyID,
		Representations:  []model.ContractRepresentation{model.ContractRepresentationRows},
		FixedFields:      []string{"endpoint", "region", "bucket", "prefix", "force_path_style", "operation", "format", "delimiter", "has_header", "overwrite"},
		DynamicSelectors: []string{"key"},
		ExecutionValues:  []string{"content", "content_type", "metadata"},
	}
}

var descriptors = []Descriptor{
	{
		Type: model.DataSourceTypeGoogleWorkspace,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations:  []string{"spreadsheet.read", "file.list", "file.read", "document.read", "document.export", "presentation.read", "presentation.export"},
			WriteOperations: []string{"spreadsheet.update", "file.create", "file.update", "document.create", "document.update", "presentation.create", "presentation.update"},
			LoadModes:       []string{"range_update", "file_create", "file_update", "blank_document_create", "text_replace"},
			SourcePayloads:  []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Exact Sheets rectangle; ordered columns and matching rows. Native text replacement serializes structured input."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "Drive JSON file create/update or native text content; no arbitrary document conversion."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "Drive CSV file create/update or native text content."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Drive file create/update only; native text replacement rejects binary input."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{"page_token"}, Scope: "Drive file listing/discovery only", Limitation: "One bounded page per list operation; schema inventory contains at most 100 files. No incremental or CDC.", Throttling: "Writes have one provider attempt; quota/transport failures after dispatch can be ambiguous."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"oauth2_selected_files"}, Renewal: "Managed backend OAuth refresh; Google Testing-mode grants expire after seven days.", Recovery: "Explicit reconnect/consent and Picker authorization for existing files; folder consent excludes existing children."},
			Limitations:    []string{"No append/resize, template copying, layout edits or sharing administration. Native creates/text replacements require unsafe replay; inspect remote state before repeating."},
		},
		SupportLevel:                  SupportLevelPreview,
		ProofMetadata:                 ProofMetadataCapability{Operations: []string{"google_workspace.worksheet_metadata", "google_workspace.file_metadata"}, SourceClaimSuffix: "/source/selected_resource", SinkClaimSuffix: "/destination/selected_resource"},
		SchemaFamily:                  SchemaFamilyFile,
		QueryOperationType:            OperationTypeGoogleWorkspace,
		SinkBackend:                   model.SinkBackendHTTP,
		SupportsPipelineSource:        true,
		SupportsPipelineSink:          true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{SinkReplayStrategyNaturallyIdempotent, SinkReplayStrategyUnsafe},
		SinkPayloadBinding:            SinkPayloadBindingEncoded,
		SinkResult:                    SinkResultCapability{Kind: SinkResultKindObjectRows, Operation: "update"},
	},
	{
		Type: model.DataSourceTypePostgres,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"authored_sql"}, WriteOperations: []string{"authored_sql"}, LoadModes: []string{"authored_sql"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Authored SQL must reference the input relation or bindings."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Object/array becomes input rows; scalars use explicit input binding."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Header-based rows; values are strings. Malformed CSV uses explicit input binding."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Explicit input binding only; no automatic binary/table load."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "One bounded query result; watermark filtering is authored SQL, not page traversal or CDC.", Throttling: "Execution deadline/concurrency limits; no automatic SQL retry."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"password"}, Renewal: "Static encrypted credentials; no automatic credential renewal.", Recovery: "Customer rotates credentials/grants and retests. Hosted private access requires an operator-pinned endpoint."},
			Limitations:    []string{"Insert/update/upsert semantics come from authored SQL and database permissions; no generic load-mode helper. Deletes, schema changes and late updates require an explicit workflow."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"postgres.table_metadata"}, SourceClaimSuffix: "/source/selected_resource", SinkClaimSuffix: "/destination/selected_resource"},
		SchemaFamily:           SchemaFamilySQL,
		QueryOperationType:     OperationTypeSQL,
		SinkBackend:            model.SinkBackendDB,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeMySQL,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"authored_sql"}, WriteOperations: []string{"authored_sql"}, LoadModes: []string{"authored_sql"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Authored SQL must reference the input relation or bindings."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Object/array becomes input rows; scalars use explicit input binding."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Header-based rows; values are strings. Malformed CSV uses explicit input binding."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Explicit input binding only; no automatic binary/table load."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "One bounded query result; watermark filtering is authored SQL, not page traversal or CDC.", Throttling: "Execution deadline/concurrency limits; no automatic SQL retry."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"password"}, Renewal: "Static encrypted credentials; no automatic credential renewal.", Recovery: "Customer rotates credentials/grants and retests; OAuth/OIDC and RDS IAM are unavailable."},
			Limitations:    []string{"Insert/update/upsert semantics come from authored SQL and database permissions; no generic load-mode helper. Deletes, schema changes and late updates require an explicit workflow."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"mysql.table_metadata"}, SourceClaimSuffix: "/source/selected_resource", SinkClaimSuffix: "/destination/selected_resource"},
		SchemaFamily:           SchemaFamilySQL,
		QueryOperationType:     OperationTypeSQL,
		SinkBackend:            model.SinkBackendDB,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeBigQuery,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"query"}, WriteOperations: []string{"insert_rows"}, LoadModes: []string{"append"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Append through insert_rows; target table must exist."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Only objects/arrays that normalize to rows."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Only CSV that normalizes to header-based rows."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{"page_token"}, Scope: "internal query-result traversal", Limitation: "Bounded by shared result limits; no public continuation/resumable result contract.", Throttling: "Provider requests and job polling are bounded by the execution deadline; no unlimited retry."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"oauth2", "service_account"}, Renewal: "Managed OAuth refresh or service-account access-token exchange; Testing-mode OAuth grants expire after seven days.", Recovery: "Reconnect expired/revoked OAuth grants; customer rotates service-account credentials and repairs IAM/billing."},
			Limitations:    []string{"No merge/upsert, bulk load, DDL helper or WIF. Watermarks require an authored predicate; no CDC."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"bigquery.table_metadata"}, SinkClaimSuffix: "/destination/append_capability"},
		SchemaFamily:           SchemaFamilySQL,
		QueryOperationType:     OperationTypeBigQuery,
		SinkBackend:            model.SinkBackendDB,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyIdempotentWithKey,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		SinkPayloadBinding: SinkPayloadBindingRowObject,
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeSnowflake,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"authored_sql"}, WriteOperations: []string{"INSERT"}, LoadModes: []string{"append"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Authored INSERT SELECT FROM input only."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Objects/arrays normalized into the SQL input relation."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Header-based rows normalized into the SQL input relation."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "No public pagination or resumable partial results; bounded SQL result only.", Throttling: "Execution deadline/concurrency limits; investigate uncertain writes before replay."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"password", "workload_identity"}, Renewal: "Customer WIF tokens renew for each physical session, including sessions opened during long operations; passwords are static.", Recovery: "WIF needs saved-connection binding, operator identity infrastructure and customer-admin grants. Repair/reverify the binding; no password fallback or unsaved-draft WIF test."},
			Limitations:    []string{"No COPY INTO, staged load, merge/upsert, DDL helper, OAuth or key-pair JWT. Watermarks require authored SQL; no CDC."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "SDK session, OCSP and telemetry traffic and warehouse-backed metadata queries lack an admitted bounded no-cost observation."},
		SchemaFamily:           SchemaFamilySQL,
		QueryOperationType:     OperationTypeSnowflake,
		SinkBackend:            model.SinkBackendDB,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeHTTP,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"request"}, WriteOperations: []string{"request"}, LoadModes: []string{"provider_defined_request"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationJSON, model.ContractRepresentationCSV, model.ContractRepresentationFile, model.ContractRepresentationMultipartForm},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Explicit body input binding; provider defines request semantics."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeNative, Limitation: "Explicit body input binding."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw body with the matching content type."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes with the matching content type."},
				{Representation: model.ContractRepresentationMultipartForm, Mode: model.ConnectorPayloadModeNative, Limitation: "Generated multipart boundary; URL-encoded forms accept scalar fields only."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{"cursor", "page_number", "next_link"}, Scope: "explicit GET request traversal", Limitation: "Bounded pages/results; next-link uses JSON, not Link headers. No short-page termination or async polling.", Throttling: "Bounded Retry-After/read retries; write retries require explicit idempotency evidence and configured policy."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"none", "secret_headers", "api_key", "bearer", "basic", "oauth2_authorization_code", "oauth2_client_credentials"}, Renewal: "Supported OAuth grants refresh/exchange tokens; unattended authorization-code use requires a refresh token. Static secrets have no automatic renewal.", Recovery: "Customer owns provider app, endpoints/scopes and static rotation; reconnect revoked grants. Credentialed redirects stay on the same origin."},
			Limitations:    []string{"Method/read-only policy still applies. Customer defines API version, object mappings, append/upsert/delete semantics and recovery; generic HTTP is not a maintained native provider integration."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "Generic requests and whole OpenAPI documents do not establish selected-resource metadata semantics."},
		SchemaFamily:           SchemaFamilyHTTP,
		QueryOperationType:     OperationTypeHTTP,
		SinkBackend:            model.SinkBackendHTTP,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyIdempotentWithKey,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindCursor,
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeMongoDB,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"find", "aggregate", "count_documents"}, WriteOperations: []string{"insert_one", "update_one", "update_many", "delete_one", "delete_many"}, LoadModes: []string{"insert", "update", "delete"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationJSON},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Explicit document/filter/update binding; match the operation's object/array shape."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Explicit structured binding; match the operation's object/array shape."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Contextual input is a string; no implicit CSV-to-document load."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeBindingFallback, Limitation: "Contextual input binding only; no native binary/document load."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "Driver batches are internal; no connector page traversal or change streams.", Throttling: "Execution deadline/row/byte limits; driver batching does not guarantee resumability."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"password"}, Renewal: "Static encrypted database credentials; no automatic credential renewal.", Recovery: "Customer rotates credentials and repairs database roles; retest access."},
			Limitations:    []string{"Find/aggregate pipeline output is a JSON document array; count/write results are metadata. No replace or upsert option is exposed by the current statement. Watermark filtering is authored; no CDC."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"mongodb.collection_metadata"}, SourceClaimSuffix: "/source/selected_resource", SinkClaimSuffix: "/destination/selected_resource"},
		SchemaFamily:           SchemaFamilyDocument,
		QueryOperationType:     OperationTypeMongoDB,
		SinkBackend:            model.SinkBackendMongo,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindWatermark,
		},
	},
	{
		Type: model.DataSourceTypeHubSpot,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"list", "search"}, WriteOperations: []string{"upsert"}, LoadModes: []string{"keyed_upsert"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeNative, Limitation: "Stable unique-key upsert with explicit property mapping; idempotent_with_key required."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Only objects/arrays that normalize to mapped rows."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeRowNormalized, Limitation: "Only CSV that normalizes to mapped rows."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{"cursor"}, Scope: "list/search after cursor", Limitation: "Provider search windows and execution bounds apply; a cursor does not guarantee late-update completeness or CDC.", Throttling: "Bounded read retries and keyed-upsert retry policy; long retry windows fail within the deadline."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"oauth2", "private_app_token"}, Renewal: "Managed OAuth refresh; static private-app tokens require customer rotation. Refresh does not add scopes.", Recovery: "Reconnect/reinstall revoked grants or request new scopes explicitly; no fallback from failed OAuth to a static token."},
			Limitations:    []string{"API 2026-03; contacts, companies, deals and discovered custom objects only. Custom objects need account entitlement/scopes. No generic create/delete sink."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"hubspot.contact_metadata"}, SourceClaimSuffix: "/source/live_identity"},
		SchemaFamily:           SchemaFamilyObjectAPI,
		QueryOperationType:     OperationTypeHubSpot,
		SinkBackend:            model.SinkBackendHTTP,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyIdempotentWithKey,
		},
		SinkPayloadBinding: SinkPayloadBindingRowObject,
		SinkResult: SinkResultCapability{
			Kind:      SinkResultKindObjectRows,
			Operation: "upsert",
		},
		ForcedReadOnly: false,
		IncrementalSourceCheckpointKinds: []model.IncrementalCheckpointKind{
			model.IncrementalCheckpointKindCursor,
		},
	},
	{
		Type: model.DataSourceTypeFTP,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"read"}, WriteOperations: []string{"write"}, LoadModes: []string{"file_write"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV/JSON/XLSX output; explicit worksheet/range for XLSX."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "JSON or row-compatible spreadsheet input."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV output only; no implicit JSON/XLSX conversion."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes require format:file."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "Full-file reads only; no incremental checkpoint.", Throttling: "Execution byte/time limits; investigate ambiguous writes before replay."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"password"}, Renewal: "Static FTP credentials; no automatic renewal.", Recovery: "Customer rotates credentials and verifies file state after ambiguous writes."},
			Limitations:    []string{"Plaintext FTP only: no TLS or SFTP. Use approved isolated networks/data. XLSX creates workbook output; no formula evaluation or preservation of existing workbook artifacts."},
		},
		SupportLevel:           SupportLevelExperimental,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "Current introspection lists and samples files; no admitted selected-file metadata operation exists."},
		SchemaFamily:           SchemaFamilyFile,
		QueryOperationType:     OperationTypeFTP,
		SinkBackend:            model.SinkBackendFTP,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
	},
	{
		Type: model.DataSourceTypeGit,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"read"}, WriteOperations: []string{"write"}, LoadModes: []string{"commit_file"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV/JSON/XLSX file content."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "JSON or row-compatible spreadsheet input."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV output only; no implicit JSON/XLSX conversion."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes require format:file."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "Full repository-file reads; no incremental checkpoint or issues/PR extraction.", Throttling: "Clone/file/byte/time bounds; no provider API pagination or universal retry guarantee."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"https", "ssh"}, Renewal: "Manual HTTPS/SSH credentials; customer owns rotation and SSH host trust.", Recovery: "Replace expired credentials or repair repository/branch permissions; HTTPS redirects are rejected."},
			Limitations:    []string{"Writes create repository commits/pushes, not provider objects. XLSX is file-backed with no formula evaluation or existing-workbook preservation."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "Repository clone/list/sample operations have no bounded selected-resource metadata observation."},
		SchemaFamily:           SchemaFamilyFile,
		QueryOperationType:     OperationTypeGit,
		SinkBackend:            model.SinkBackendGit,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
	},
	{
		Type: model.DataSourceTypeGithub,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"read"}, WriteOperations: []string{"write"}, LoadModes: []string{"commit_file"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV/JSON/XLSX file content."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "JSON or row-compatible spreadsheet input."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV output only; no implicit JSON/XLSX conversion."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes require format:file."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "Shared Git repository-file runtime; no incremental checkpoint or issues/PR extraction.", Throttling: "Clone/file/byte/time bounds; installation/token acquisition is not data pagination."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"github_app"}, Renewal: "Organization GitHub App installation tokens are resolved through the managed connection.", Recovery: "Repair installation/repository access; manual HTTPS/SSH credentials belong to Generic Git."},
			Limitations:    []string{"Write access depends on installation permissions and branch rules. File-backed XLSX has no formula evaluation or existing-workbook preservation."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "Repository clone/list/sample operations have no bounded selected-resource metadata observation."},
		SchemaFamily:           SchemaFamilyFile,
		QueryOperationType:     OperationTypeGit,
		SinkBackend:            model.SinkBackendGit,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
	},
	{
		Type: model.DataSourceTypeGitlab,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"read"}, WriteOperations: []string{"write"}, LoadModes: []string{"commit_file"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV/JSON/XLSX file content."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "JSON or row-compatible spreadsheet input."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV output only; no implicit JSON/XLSX conversion."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes require format:file."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{}, Scope: "unsupported", Limitation: "Shared Git repository-file runtime; no incremental checkpoint or issues/merge-request extraction.", Throttling: "Clone/file/byte/time bounds; repository selection is not data pagination."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"oauth2"}, Renewal: "Organization GitLab OAuth grant uses the managed credential lifecycle.", Recovery: "Reconnect revoked/expired grants and recheck project access. Manual project tokens/SSH belong to Generic Git."},
			Limitations:    []string{"Write access depends on grant permissions and branch rules. File-backed XLSX has no formula evaluation or existing-workbook preservation."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{UnsupportedReason: "Repository clone/list/sample operations have no bounded selected-resource metadata observation."},
		SchemaFamily:           SchemaFamilyFile,
		QueryOperationType:     OperationTypeGit,
		SinkBackend:            model.SinkBackendGit,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
	},
	{
		Type: model.DataSourceTypeS3,
		Capabilities: model.ConnectorCapabilityDetails{
			ReadOperations: []string{"read"}, WriteOperations: []string{"write"}, LoadModes: []string{"object_put"},
			SourcePayloads: []model.ContractRepresentation{model.ContractRepresentationRows, model.ContractRepresentationFile},
			SinkPayloads: []model.ConnectorSinkPayloadSupport{
				{Representation: model.ContractRepresentationRows, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV/JSON/XLSX object content."},
				{Representation: model.ContractRepresentationJSON, Mode: model.ConnectorPayloadModeSerialized, Limitation: "JSON or row-compatible spreadsheet input."},
				{Representation: model.ContractRepresentationCSV, Mode: model.ConnectorPayloadModeSerialized, Limitation: "CSV output only; no implicit JSON/XLSX conversion."},
				{Representation: model.ContractRepresentationFile, Mode: model.ConnectorPayloadModeNative, Limitation: "Raw bytes require format:file."},
			},
			Pagination:     model.ConnectorPaginationSupport{Methods: []string{"continuation_token"}, Scope: "introspection object listing only", Limitation: "Object reads are full-read only; listing pages are not incremental extraction.", Throttling: "Provider/client retry policy remains within runtime byte/time limits; verify uncertain puts before replay."},
			Authentication: model.ConnectorAuthenticationSupport{Modes: []string{"access_key", "access_key_session_token"}, Renewal: "Static access key with optional session token; no automatic AssumeRole, STS/OIDC or default credential chain.", Recovery: "Customer rotates keys/session credentials, repairs bucket policy and verifies object state before replay."},
			Limitations:    []string{"Object put/overwrite only; no record merge or multipart-form input. XLSX has no formula evaluation or existing-workbook preservation. No CDC."},
		},
		SupportLevel:           SupportLevelPreview,
		ProofMetadata:          ProofMetadataCapability{Operations: []string{"s3.object_metadata"}, SourceClaimSuffix: "/source/selected_resource", SinkClaimSuffix: "/destination/selected_resource"},
		SchemaFamily:           SchemaFamilyObjectStore,
		QueryOperationType:     OperationTypeS3,
		SinkBackend:            model.SinkBackendS3,
		SupportsPipelineSource: true,
		SupportsPipelineSink:   true,
		SupportedSinkReplayStrategies: []SinkReplayStrategy{
			SinkReplayStrategyNaturallyIdempotent,
			SinkReplayStrategyDeduplicatedByLedger,
			SinkReplayStrategyUnsafe,
		},
	},
}

func AllDescriptors() []Descriptor {
	return append([]Descriptor(nil), descriptors...)
}

func DescriptorFor(dsType model.DataSourceType) (Descriptor, bool) {
	for _, descriptor := range descriptors {
		if descriptor.Type == dsType {
			return descriptor, true
		}
	}

	return Descriptor{}, false
}

// SupportsSinkReplayStrategy reports whether a sink connector permits the
// declared replay mechanism. Unknown and source-only connectors are rejected
// so callers cannot dispatch a side effect with an unverified contract.
func SupportsSinkReplayStrategy(dsType model.DataSourceType, strategy SinkReplayStrategy) bool {
	descriptor, found := DescriptorFor(dsType)
	if !found || !descriptor.SupportsPipelineSink {
		return false
	}

	for _, supported := range descriptor.SupportedSinkReplayStrategies {
		if supported == strategy {
			return true
		}
	}

	return false
}

func IsSupportedQueryOperationType(operationType string) bool {
	for _, descriptor := range descriptors {
		if descriptor.QueryOperationType == operationType {
			return true
		}
	}

	return false
}

func SinkBackendForOperationType(operationType string) (model.SinkBackend, bool) {
	for _, descriptor := range descriptors {
		if descriptor.QueryOperationType == operationType &&
			descriptor.SupportsPipelineSink &&
			descriptor.SinkBackend != model.SinkBackendUnknown {
			return descriptor.SinkBackend, true
		}
	}

	return model.SinkBackendUnknown, false
}

// SinkPayloadBindingForOperationType returns the public input shape expected by
// a sink connector. Connectors default to the encoded binding unless their
// catalog descriptor explicitly declares row-object input.
func SinkPayloadBindingForOperationType(operationType string) SinkPayloadBinding {
	for _, descriptor := range descriptors {
		if descriptor.QueryOperationType == operationType &&
			descriptor.SinkPayloadBinding != "" {
			return descriptor.SinkPayloadBinding
		}
	}

	return SinkPayloadBindingEncoded
}

// SinkResultForOperationType returns the result contract expected after sink
// dispatch. HTTP is the default for connectors without an explicit result shape.
func SinkResultForOperationType(operationType string) SinkResultCapability {
	for _, descriptor := range descriptors {
		if descriptor.QueryOperationType == operationType && descriptor.SinkResult.Kind != "" {
			return descriptor.SinkResult
		}
	}

	return SinkResultCapability{Kind: SinkResultKindHTTP}
}

func SupportsIncrementalSourceCheckpointKind(
	operationType string,
	kind model.IncrementalCheckpointKind,
) bool {
	for _, descriptor := range descriptors {
		if descriptor.QueryOperationType != operationType || !descriptor.SupportsPipelineSource {
			continue
		}
		for _, supportedKind := range descriptor.IncrementalSourceCheckpointKinds {
			if supportedKind == kind {
				return true
			}
		}
	}

	return false
}
