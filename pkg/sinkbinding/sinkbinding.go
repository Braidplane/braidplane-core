package sinkbinding

const (
	// Input is the public reserved sink payload binding exposed to saved sink operations.
	Input = "input"

	// InternalInputRows carries runtime row payloads for SQL connectors that can
	// materialize a native input relation.
	InternalInputRows = "__braidplane_input_rows"
	// InternalInputColumns carries the ordered column list paired with
	// InternalInputRows.
	InternalInputColumns = "__braidplane_input_columns"
	// InternalInputRepresentation carries the runtime payload representation for
	// connectors that need representation-sensitive handling behind the single
	// public Input binding.
	InternalInputRepresentation = "__braidplane_input_representation"
	// InternalInputFileMetadata carries safe metadata for native file payloads.
	InternalInputFileMetadata = "__braidplane_input_file"
	// InternalInputFileFilename carries the file payload filename for template substitution.
	InternalInputFileFilename = "__braidplane_input_file.filename"
	// InternalInputFileMIMEType carries the file payload media type for template substitution.
	InternalInputFileMIMEType = "__braidplane_input_file.mime_type"
	// InternalInputFileSizeBytes carries the file payload size for template substitution.
	InternalInputFileSizeBytes = "__braidplane_input_file.size_bytes"
	// InternalInputFileDigestAlgorithm carries the file payload digest algorithm.
	InternalInputFileDigestAlgorithm = "__braidplane_input_file.digest_algorithm"
	// InternalInputFileDigest carries the file payload digest value for template substitution.
	InternalInputFileDigest = "__braidplane_input_file.digest"
	// InternalIdempotencyKey carries the stable runtime sink idempotency key.
	InternalIdempotencyKey = "__braidplane_sink_idempotency_key"
	// InternalIdempotencyClass carries the declared sink idempotency class.
	InternalIdempotencyClass = "__braidplane_sink_idempotency_class"
	// InternalEffectStartedAt retains the original durable claim time across recovery.
	InternalEffectStartedAt = "__braidplane_sink_effect_started_at"
	// InternalReplayIdentity carries the original effect namespace for operator
	// replays that must target the same deterministic external object.
	InternalReplayIdentity = "__braidplane_sink_replay_identity"
)
