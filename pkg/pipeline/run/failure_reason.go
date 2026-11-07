package run

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=FailureReasonCode -json -text -linecomment -output=failure_reason_code_enumer.go

// FailureReasonCode identifies the bounded machine-readable cause of a failed run.
type FailureReasonCode int

const (
	// FailureReasonCodeUnknown means the run failure does not have a stable reason code.
	FailureReasonCodeUnknown FailureReasonCode = iota // unknown
	// FailureReasonCodeDataSourceCredentialsFailed means connector authentication failed.
	FailureReasonCodeDataSourceCredentialsFailed // data_source_credentials_failed
	// FailureReasonCodeSourceQueryFailed means a source query failed during execution.
	FailureReasonCodeSourceQueryFailed // source_query_failed
	// FailureReasonCodeSourceResultShapeMismatch means source output violated its runtime contract.
	FailureReasonCodeSourceResultShapeMismatch // source_result_shape_mismatch
	// FailureReasonCodeTransformRuntimeFailed means sql_v1 transform execution failed.
	FailureReasonCodeTransformRuntimeFailed // transform_runtime_failed
	// FailureReasonCodeTransformContractMismatch means transform output violated its runtime contract.
	FailureReasonCodeTransformContractMismatch // transform_contract_mismatch
	// FailureReasonCodeSinkContractMismatch means sink input violated its runtime contract before writing.
	FailureReasonCodeSinkContractMismatch // sink_contract_mismatch
	// FailureReasonCodeS3ObjectWriteFailed means an S3/object sink write failed.
	FailureReasonCodeS3ObjectWriteFailed // s3_object_write_failed
	// FailureReasonCodeResourceScopeRejected means a selector or target violated the pinned resource scope.
	FailureReasonCodeResourceScopeRejected // resource_scope_rejected
	// FailureReasonCodeDataSourcePermissionDenied means the selected operation was denied.
	FailureReasonCodeDataSourcePermissionDenied // data_source_permission_denied
	// FailureReasonCodeDataSourceConnectionFailed means the endpoint could not be reached.
	FailureReasonCodeDataSourceConnectionFailed // data_source_connection_failed
	// FailureReasonCodeDataSourceTimeout means source execution exceeded its time limit.
	FailureReasonCodeDataSourceTimeout // data_source_timeout
	// FailureReasonCodeSourceQueryInvalid means the provider rejected the query.
	FailureReasonCodeSourceQueryInvalid // source_query_invalid
	// FailureReasonCodeSourceSchemaReferenceInvalid means a query schema reference could not resolve.
	FailureReasonCodeSourceSchemaReferenceInvalid // source_schema_reference_invalid
	// FailureReasonCodeWorkerOwnershipLost means an admitted worker attempt ended without completion.
	FailureReasonCodeWorkerOwnershipLost // worker_ownership_lost
	// FailureReasonCodeEventInputRejected means event input failed admission before execution.
	FailureReasonCodeEventInputRejected // event_input_rejected
	// FailureReasonCodeDestinationConstraintViolation means the destination rejected write data.
	FailureReasonCodeDestinationConstraintViolation // destination_constraint_violation
)
