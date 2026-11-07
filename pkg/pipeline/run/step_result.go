package run

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/redaction"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	runInputResultType  = "Input"
	runInputVendorType  = "runtime"
	transformVendorType = "transform"
	transformResultType = "Transform"
)

// StepResult is a pipeline-level wrapper around step outputs.
// Query steps store connector ExecutionResult while transform steps store
// payload.Output directly to keep connector results free of transform payloads.
type StepResult struct {
	execution *result.ExecutionResult
	transform *pipepayload.Output
	payload   *pipepayload.Payload
	runInput  bool

	durationMs int64
	err        error
}

func newRunInputStepResult(input *pipepayload.Payload) *StepResult {
	if input == nil {
		panic("run input payload is required")
	}

	return &StepResult{payload: input.Clone(), runInput: true}
}

func newStepResult(execution *result.ExecutionResult) *StepResult {
	if execution == nil {
		panic("step result execution is required")
	}

	return &StepResult{
		execution:  execution,
		payload:    runtimePayloadFromExecutionResult(execution),
		durationMs: execution.DurationMs(),
		err:        execution.Error(),
	}
}

func newTransformStepResult(
	transform *pipepayload.Output,
	durationMs int64,
	err error,
) *StepResult {
	if transform == nil {
		panic("step result transform is required")
	}

	return &StepResult{
		transform:  transform,
		payload:    runtimePayloadFromTransformOutput(transform),
		durationMs: durationMs,
		err:        err,
	}
}

func (sr *StepResult) Execution() *result.ExecutionResult {
	return sr.requireExecution()
}

func (sr *StepResult) Clone() *StepResult {
	if sr == nil {
		return nil
	}

	cloned := &StepResult{durationMs: sr.DurationMs()}
	if err := sr.Error(); err != nil {
		cloned.err = err
	}

	if sr.execution != nil {
		cloned.execution = sr.execution.Clone()
	}
	if sr.transform != nil {
		cloned.transform = cloneTransformOutput(sr.transform)
	}
	if sr.payload != nil {
		cloned.payload = sr.payload.Clone()
	}
	cloned.runInput = sr.runInput

	return cloned
}

// RedactedClone returns an independent copy with storage-visible strings redacted.
func (sr *StepResult) RedactedClone(redact func(string) string) *StepResult {
	cloned := sr.Clone()
	if cloned == nil || redact == nil {
		return cloned
	}
	redactStepResult(cloned, redact)
	return cloned
}

func (sr *StepResult) MarshalJSON() ([]byte, error) {
	if sr == nil {
		return nil, xerror.StepResultExecutionRequired()
	}

	payload := payloadForStorage(sr.payload)
	var errMsg *string
	if sr.err != nil {
		msg := sr.err.Error()
		errMsg = &msg
	}

	if sr.execution != nil {
		sqlResult, _ := sr.execution.SQL()
		mongoResult, _ := sr.execution.MongoDB()
		httpResult, _ := sr.execution.HTTP()
		objectRowsResult, _ := sr.execution.ObjectRows()
		ftpResult, _ := sr.execution.FTP()
		gitResult, _ := sr.execution.Git()
		s3Result, _ := sr.execution.S3()
		storageResultType := executionStorageResultType(sr.execution)

		encoded := struct {
			ResultType string                   `json:"result_type"`
			VendorType string                   `json:"vendor_type"`
			DurationMs int64                    `json:"duration_ms"`
			Error      *string                  `json:"error,omitempty"`
			SQL        *result.SQLResult        `json:"sql,omitempty"`
			MongoDB    *result.MongoDBResult    `json:"mongodb,omitempty"`
			HTTP       *result.HTTPResult       `json:"http,omitempty"`
			ObjectRows *result.ObjectRowsResult `json:"object_rows,omitempty"`
			FTP        *result.FileResult       `json:"ftp,omitempty"`
			Git        *result.FileResult       `json:"git,omitempty"`
			S3         *result.FileResult       `json:"s3,omitempty"`
			Payload    *pipepayload.Payload     `json:"payload,omitempty"`
		}{
			ResultType: storageResultType,
			VendorType: sr.execution.VendorType(),
			DurationMs: sr.execution.DurationMs(),
			Error:      errMsg,
			SQL:        sqlResult,
			MongoDB:    mongoResult,
			HTTP:       httpResult,
			ObjectRows: objectRowsResult,
			FTP:        ftpResult,
			Git:        gitResult,
			S3:         s3Result,
			Payload:    payload,
		}

		return json.Marshal(encoded)
	}
	if sr.runInput {
		return json.Marshal(struct {
			ResultType string               `json:"result_type"`
			VendorType string               `json:"vendor_type"`
			DurationMs int64                `json:"duration_ms"`
			Error      *string              `json:"error,omitempty"`
			Payload    *pipepayload.Payload `json:"payload"`
		}{
			ResultType: runInputResultType,
			VendorType: runInputVendorType,
			Error:      errMsg,
			Payload:    payload,
		})
	}
	if sr.transform == nil {
		return nil, xerror.StepResultExecutionRequired().Detail(
			"step result execution is required: step result transform is nil",
		)
	}

	encoded := struct {
		ResultType string               `json:"result_type"`
		VendorType string               `json:"vendor_type"`
		DurationMs int64                `json:"duration_ms"`
		Error      *string              `json:"error,omitempty"`
		Transform  *pipepayload.Output  `json:"transform,omitempty"`
		Payload    *pipepayload.Payload `json:"payload,omitempty"`
	}{
		ResultType: transformResultType,
		VendorType: transformVendorType,
		DurationMs: sr.durationMs,
		Error:      errMsg,
		Transform:  sr.transform,
		Payload:    payload,
	}

	return json.Marshal(encoded)
}

func (sr *StepResult) UnmarshalJSON(data []byte) error {
	var stored struct {
		ResultType string               `json:"result_type"`
		DurationMs int64                `json:"duration_ms"`
		Error      *string              `json:"error"`
		Transform  *pipepayload.Output  `json:"transform"`
		Payload    *pipepayload.Payload `json:"payload"`
	}
	if err := jsonvalue.Unmarshal(data, &stored); err != nil {
		return err
	}
	var restored *StepResult
	switch stored.ResultType {
	case transformResultType:
		if stored.Transform == nil {
			return xerror.StepResultExecutionRequired().Detail("stored transform is required")
		}
		jsonvalue.Normalize(stored.Transform.Rows)
		var transformErr error
		if stored.Error != nil {
			transformErr = xerror.Unexpected().Detail(*stored.Error)
		}
		restored = newTransformStepResult(stored.Transform, stored.DurationMs, transformErr)
	case runInputResultType:
		if stored.Payload == nil {
			return xerror.StepResultExecutionRequired().Detail("stored run input payload is required")
		}
		restored = newRunInputStepResult(stored.Payload)
		if stored.Error != nil {
			restored.err = xerror.Unexpected().Detail(*stored.Error)
		}
	default:
		var execution result.ExecutionResult
		if err := json.Unmarshal(data, &execution); err != nil {
			return err
		}
		if execution.Type() == result.ExecutionResultTypeUnknown {
			return xerror.StepResultExecutionRequired().Detail("stored execution result type is required")
		}
		restored = newStepResult(&execution)
	}
	if stored.Payload != nil {
		// Payload storage retains diagnostics; content comes from the typed result.
		if content := restored.payload; content != nil && !restored.runInput {
			stored.Payload.Rows = content.Rows
			stored.Payload.Document = content.Document
			stored.Payload.CSV = content.CSV
			stored.Payload.FileBytes = content.FileBytes
			stored.Payload.Form = content.Form
		}
		restored.payload = stored.Payload
	}
	*sr = *restored
	return nil
}

func payloadForStorage(payload *pipepayload.Payload) *pipepayload.Payload {
	if payload == nil {
		return nil
	}

	stored := payload.Clone()
	stored.Rows = nil
	stored.Document = nil
	stored.CSV = ""
	stored.FileBytes = nil

	return stored
}

// redactStepResult mutates a cloned step result so persisted output cannot expose secret values.
func redactStepResult(sr *StepResult, redact func(string) string) {
	if sr == nil {
		return
	}
	if sr.err != nil {
		sr.err = xerror.Unexpected().Detail(redact(sr.err.Error()))
	}
	if sr.execution != nil {
		redactExecutionResult(sr.execution, redact)
	}
	redactTransformOutput(sr.transform, redact)
	redactPayload(sr.payload, redact)
}

// redactExecutionResult mutates connector execution payloads and rebuilds typed errors with redacted text.
func redactExecutionResult(execution *result.ExecutionResult, redact func(string) string) {
	if execution == nil {
		return
	}
	if execution.Error() != nil {
		executionErr := xerror.Unexpected().Detail(redact(execution.Error().Error()))
		switch execution.Type() {
		case result.ExecutionResultTypeSQL:
			sqlResult, _ := execution.SQL()
			*execution = *result.NewSQLResult(execution.VendorType(), sqlResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeMongoDB:
			mongoResult, _ := execution.MongoDB()
			*execution = *result.NewMongoDBResult(mongoResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeHTTP:
			httpResult, _ := execution.HTTP()
			*execution = *result.NewHTTPResult(httpResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeObjectRows:
			objectRowsResult, _ := execution.ObjectRows()
			*execution = *result.NewObjectRowsResult(
				execution.VendorType(),
				objectRowsResult,
				execution.DurationMs(),
				executionErr,
			)
		case result.ExecutionResultTypeFTP:
			fileResult, _ := execution.FTP()
			*execution = *result.NewFTPResult(fileResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeGit:
			fileResult, _ := execution.Git()
			*execution = *result.NewGitResult(fileResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeWorkspaceFile:
			fileResult, _ := execution.File()
			*execution = *result.NewWorkspaceFileResult(fileResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeS3:
			fileResult, _ := execution.S3()
			*execution = *result.NewS3Result(fileResult, execution.DurationMs(), executionErr)
		case result.ExecutionResultTypeUnknown:
			// Unknown results do not have a typed payload constructor to rebuild.
		}
	}

	if sqlResult, ok := execution.SQL(); ok {
		redactSQLResult(sqlResult, redact)
	}
	if mongoResult, ok := execution.MongoDB(); ok {
		redactMongoDBResult(mongoResult, redact)
	}
	if httpResult, ok := execution.HTTP(); ok {
		redactHTTPResult(httpResult, redact)
	}
	if objectRowsResult, ok := execution.ObjectRows(); ok {
		redactObjectRowsResult(objectRowsResult, redact)
	}
	if fileResult, ok := execution.File(); ok {
		redactFileResult(fileResult, redact)
	}
}

// redactSQLResult mutates SQL rows and column names that may contain resolved secret values.
func redactSQLResult(sqlResult *result.SQLResult, redact func(string) string) {
	if sqlResult == nil {
		return
	}
	redactRows(sqlResult.Rows, redact)
	redaction.StringSlice(sqlResult.ColumnNames, redact)
}

// redactMongoDBResult mutates MongoDB result metadata and documents that may contain secrets.
func redactMongoDBResult(mongoResult *result.MongoDBResult, redact func(string) string) {
	if mongoResult == nil {
		return
	}
	mongoResult.OperationType = redact(mongoResult.OperationType)
	redactRows(mongoResult.Documents, redact)
	mongoResult.InsertedID = redaction.Any(mongoResult.InsertedID, redact)
	redaction.AnySlice(mongoResult.InsertedIDs, redact)
	mongoResult.UpsertedID = redaction.Any(mongoResult.UpsertedID, redact)
}

// redactHTTPResult mutates HTTP headers, body data, and file metadata before storage.
func redactHTTPResult(httpResult *result.HTTPResult, redact func(string) string) {
	if httpResult == nil {
		return
	}
	for key, value := range httpResult.Headers {
		httpResult.Headers[key] = redact(value)
	}
	httpResult.Body = redactRawJSON(httpResult.Body, redact)
	redactFileMetadata(httpResult.File, redact)
	httpResult.RawBody = []byte(redact(string(httpResult.RawBody)))
}

// redactObjectRowsResult mutates object-row result metadata, rows, and cursors.
func redactObjectRowsResult(objectRowsResult *result.ObjectRowsResult, redact func(string) string) {
	if objectRowsResult == nil {
		return
	}
	objectRowsResult.ObjectType = redact(objectRowsResult.ObjectType)
	objectRowsResult.Operation = redact(objectRowsResult.Operation)
	redactRows(objectRowsResult.Rows, redact)
	redaction.StringSlice(objectRowsResult.ColumnNames, redact)
	if objectRowsResult.NextCursor != nil {
		*objectRowsResult.NextCursor = redact(*objectRowsResult.NextCursor)
	}
	redaction.AnyMap(objectRowsResult.ProviderMetadata, redact)
}

// redactFileResult mutates file connector rows, metadata, paths, and content bytes.
func redactFileResult(fileResult *result.FileResult, redact func(string) string) {
	if fileResult == nil {
		return
	}
	redactRows(fileResult.Rows, redact)
	redaction.StringSlice(fileResult.ColumnNames, redact)
	fileResult.Format = redact(fileResult.Format)
	fileResult.Sheet = redact(fileResult.Sheet)
	fileResult.Range = redact(fileResult.Range)
	fileResult.Operation = redact(fileResult.Operation)
	fileResult.TargetPath = redact(fileResult.TargetPath)
	redactFileMetadata(fileResult.File, redact)
	fileResult.Content = []byte(redact(string(fileResult.Content)))
}

// redactTransformOutput mutates transform output fields that are persisted with the run result.
func redactTransformOutput(output *pipepayload.Output, redact func(string) string) {
	if output == nil {
		return
	}
	redaction.StringSlice(output.ColumnNames, redact)
	redactRows(output.Rows, redact)
	output.Document = redactRawJSON(output.Document, redact)
	output.CSV = redact(output.CSV)
}

// redactPayload mutates generic runtime payload fields before they are stored with a step result.
func redactPayload(payload *pipepayload.Payload, redact func(string) string) {
	if payload == nil {
		return
	}
	redaction.StringSlice(payload.ColumnNames, redact)
	redactRows(payload.Rows, redact)
	payload.Document = redactRawJSON(payload.Document, redact)
	payload.CSV = redact(payload.CSV)
	redactFileMetadata(payload.File, redact)
	payload.FileBytes = []byte(redact(string(payload.FileBytes)))
	redactFormData(payload.Form, redact)
}

// redactFormData mutates multipart form field and file metadata plus inline file content.
func redactFormData(formData *formpayload.FormData, redact func(string) string) {
	if formData == nil {
		return
	}
	formData.Encoding = redact(formData.Encoding)
	for i := range formData.Fields {
		formData.Fields[i].Name = redact(formData.Fields[i].Name)
		formData.Fields[i].ContentType = redact(formData.Fields[i].ContentType)
		redaction.StringSlice(formData.Fields[i].Values, redact)
	}
	for i := range formData.Files {
		formData.Files[i].Name = redact(formData.Files[i].Name)
		formData.Files[i].Filename = redact(formData.Files[i].Filename)
		formData.Files[i].ContentType = redact(formData.Files[i].ContentType)
		formData.Files[i].Content = []byte(redact(string(formData.Files[i].Content)))
	}
}

// redactFileMetadata mutates file metadata names and media types that can echo secret-derived paths.
func redactFileMetadata(metadata *filepayload.FilePayloadMetadata, redact func(string) string) {
	if metadata == nil {
		return
	}
	metadata.Filename = redact(metadata.Filename)
	metadata.MIMEType = redact(metadata.MIMEType)
}

// redactRows mutates row maps in place through the shared JSON-shaped redaction helper.
func redactRows(rows []map[string]any, redact func(string) string) {
	for _, row := range rows {
		redaction.AnyMap(row, redact)
	}
}

// redactRawJSON redacts decoded JSON values and preserves the previous nil-empty storage behavior.
func redactRawJSON(raw json.RawMessage, redact func(string) string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return redaction.RawJSON(raw, redact)
}

func (sr *StepResult) SQL() (*result.SQLResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.SQL()
}

func (sr *StepResult) MongoDB() (*result.MongoDBResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.MongoDB()
}

func (sr *StepResult) HTTP() (*result.HTTPResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.HTTP()
}

func (sr *StepResult) ObjectRows() (*result.ObjectRowsResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.ObjectRows()
}

func (sr *StepResult) FTP() (*result.FileResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.FTP()
}

func (sr *StepResult) Git() (*result.FileResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.Git()
}

// S3 returns the S3 file result payload when this step came from an S3 execution.
func (sr *StepResult) S3() (*result.FileResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.S3()
}

func (sr *StepResult) File() (*result.FileResult, bool) {
	if sr == nil || sr.execution == nil {
		return nil, false
	}

	return sr.execution.File()
}

func (sr *StepResult) Transform() (*pipepayload.Output, bool) {
	if sr == nil || sr.transform == nil {
		return nil, false
	}

	return sr.transform, true
}

func (sr *StepResult) Payload() (*pipepayload.Payload, bool) {
	if sr == nil || sr.payload == nil {
		return nil, false
	}

	return sr.payload, true
}

func (sr *StepResult) Type() result.ExecutionResultType {
	if sr != nil && sr.runInput {
		return result.ExecutionResultTypeUnknown
	}
	return sr.requireExecution().Type()
}

func (sr *StepResult) ResultTypeString() string {
	if sr != nil && sr.runInput {
		return runInputResultType
	}
	if sr != nil && sr.transform != nil {
		return transformResultType
	}

	return executionStorageResultType(sr.requireExecution())
}

func (sr *StepResult) VendorType() string {
	if sr != nil && sr.runInput {
		return runInputVendorType
	}
	if sr != nil && sr.transform != nil {
		return transformVendorType
	}

	return sr.requireExecution().VendorType()
}

func (sr *StepResult) DurationMs() int64 {
	if sr != nil && sr.runInput {
		return 0
	}
	if sr != nil && sr.transform != nil {
		return sr.durationMs
	}

	return sr.requireExecution().DurationMs()
}

func (sr *StepResult) Error() error {
	if sr != nil && sr.runInput {
		return sr.err
	}
	if sr != nil && sr.transform != nil {
		return sr.err
	}

	return sr.requireExecution().Error()
}

func (sr *StepResult) IsSuccess() bool {
	if sr != nil && sr.runInput {
		return sr.err == nil
	}
	if sr != nil && sr.transform != nil {
		return sr.err == nil
	}

	return sr.requireExecution().IsSuccess()
}

func (sr *StepResult) IsRunInput() bool {
	return sr != nil && sr.runInput
}

func (sr *StepResult) requireExecution() *result.ExecutionResult {
	if sr == nil || sr.execution == nil {
		panic("step result execution is required")
	}

	return sr.execution
}

func executionStorageResultType(execution *result.ExecutionResult) string {
	return execution.Type().String()
}

func cloneTransformOutput(input *pipepayload.Output) *pipepayload.Output {
	if input == nil {
		return nil
	}

	rows := make([]map[string]any, len(input.Rows))
	for i, row := range input.Rows {
		rows[i] = cloneMapAny(row)
	}

	return &pipepayload.Output{
		OutputKind:    input.OutputKind,
		RowCount:      input.RowCount,
		IsHeadlessCSV: input.IsHeadlessCSV,
		ColumnNames:   append([]string(nil), input.ColumnNames...),
		Rows:          rows,
		Document:      append([]byte(nil), input.Document...),
		CSV:           input.CSV,
	}
}

func cloneMapAny(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}

	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}

	return cloned
}
