package model

import pipelinemodel "github.com/Braidplane/braidplane-core/pkg/model"

// Pipeline contracts have one implementation shared with the standalone CLI.
type (
	RuntimeVariableValueType  = pipelinemodel.RuntimeVariableValueType
	RuntimeVariable           = pipelinemodel.RuntimeVariable
	ContractRootShape         = pipelinemodel.ContractRootShape
	SinkBackend               = pipelinemodel.SinkBackend
	IncrementalCheckpointKind = pipelinemodel.IncrementalCheckpointKind
	ParameterDataType         = pipelinemodel.ParameterDataType
	DataSourceType            = pipelinemodel.DataSourceType
	ContractFieldType         = pipelinemodel.ContractFieldType
	RuntimeSecretValue        = pipelinemodel.RuntimeSecretValue
	PipelineFormat            = pipelinemodel.PipelineFormat
	TransformOutputKind       = pipelinemodel.TransformOutputKind
	ContractRepresentation    = pipelinemodel.ContractRepresentation
)

const (
	RuntimeVariableValueTypeUnknown     = pipelinemodel.RuntimeVariableValueTypeUnknown
	RuntimeVariableValueTypeText        = pipelinemodel.RuntimeVariableValueTypeText
	RuntimeVariableValueTypeInteger     = pipelinemodel.RuntimeVariableValueTypeInteger
	RuntimeVariableValueTypeBoolean     = pipelinemodel.RuntimeVariableValueTypeBoolean
	RuntimeVariableValueTypeDate        = pipelinemodel.RuntimeVariableValueTypeDate
	RuntimeVariableValueTypeUUID        = pipelinemodel.RuntimeVariableValueTypeUUID
	ContractRootShapeUnknown            = pipelinemodel.ContractRootShapeUnknown
	ContractRootShapeObject             = pipelinemodel.ContractRootShapeObject
	ContractRootShapeArray              = pipelinemodel.ContractRootShapeArray
	SinkBackendUnknown                  = pipelinemodel.SinkBackendUnknown
	SinkBackendFTP                      = pipelinemodel.SinkBackendFTP
	SinkBackendDB                       = pipelinemodel.SinkBackendDB
	SinkBackendHTTP                     = pipelinemodel.SinkBackendHTTP
	SinkBackendMongo                    = pipelinemodel.SinkBackendMongo
	SinkBackendGit                      = pipelinemodel.SinkBackendGit
	SinkBackendGithub                   = pipelinemodel.SinkBackendGithub
	SinkBackendGitlab                   = pipelinemodel.SinkBackendGitlab
	SinkBackendS3                       = pipelinemodel.SinkBackendS3
	IncrementalCheckpointKindUnknown    = pipelinemodel.IncrementalCheckpointKindUnknown
	IncrementalCheckpointKindWatermark  = pipelinemodel.IncrementalCheckpointKindWatermark
	IncrementalCheckpointKindCursor     = pipelinemodel.IncrementalCheckpointKindCursor
	ParameterDataTypeUnknown            = pipelinemodel.ParameterDataTypeUnknown
	ParameterDataTypeText               = pipelinemodel.ParameterDataTypeText
	ParameterDataTypeInteger            = pipelinemodel.ParameterDataTypeInteger
	ParameterDataTypeBoolean            = pipelinemodel.ParameterDataTypeBoolean
	ParameterDataTypeDate               = pipelinemodel.ParameterDataTypeDate
	ParameterDataTypeUUID               = pipelinemodel.ParameterDataTypeUUID
	DataSourceTypeUnknown               = pipelinemodel.DataSourceTypeUnknown
	DataSourceTypePostgres              = pipelinemodel.DataSourceTypePostgres
	DataSourceTypeMySQL                 = pipelinemodel.DataSourceTypeMySQL
	DataSourceTypeBigQuery              = pipelinemodel.DataSourceTypeBigQuery
	DataSourceTypeGoogleWorkspace       = pipelinemodel.DataSourceTypeGoogleWorkspace
	DataSourceTypeHTTP                  = pipelinemodel.DataSourceTypeHTTP
	DataSourceTypeMongoDB               = pipelinemodel.DataSourceTypeMongoDB
	DataSourceTypeHubSpot               = pipelinemodel.DataSourceTypeHubSpot
	DataSourceTypeFTP                   = pipelinemodel.DataSourceTypeFTP
	DataSourceTypeGit                   = pipelinemodel.DataSourceTypeGit
	DataSourceTypeGithub                = pipelinemodel.DataSourceTypeGithub
	DataSourceTypeGitlab                = pipelinemodel.DataSourceTypeGitlab
	DataSourceTypeS3                    = pipelinemodel.DataSourceTypeS3
	DataSourceTypeSnowflake             = pipelinemodel.DataSourceTypeSnowflake
	ContractFieldTypeUnknown            = pipelinemodel.ContractFieldTypeUnknown
	ContractFieldTypeValueString        = pipelinemodel.ContractFieldTypeValueString
	ContractFieldTypeValueInteger       = pipelinemodel.ContractFieldTypeValueInteger
	ContractFieldTypeValueNumber        = pipelinemodel.ContractFieldTypeValueNumber
	ContractFieldTypeValueDecimal       = pipelinemodel.ContractFieldTypeValueDecimal
	ContractFieldTypeValueBoolean       = pipelinemodel.ContractFieldTypeValueBoolean
	ContractFieldTypeValueObject        = pipelinemodel.ContractFieldTypeValueObject
	ContractFieldTypeValueArray         = pipelinemodel.ContractFieldTypeValueArray
	ContractFieldTypeValueDatetime      = pipelinemodel.ContractFieldTypeValueDatetime
	PipelineFormatUnknown               = pipelinemodel.PipelineFormatUnknown
	PipelineFormatYAML                  = pipelinemodel.PipelineFormatYAML
	PipelineFormatJSON                  = pipelinemodel.PipelineFormatJSON
	TransformOutputKindUnknown          = pipelinemodel.TransformOutputKindUnknown
	TransformOutputKindRows             = pipelinemodel.TransformOutputKindRows
	TransformOutputKindJSON             = pipelinemodel.TransformOutputKindJSON
	TransformOutputKindCSV              = pipelinemodel.TransformOutputKindCSV
	ContractRepresentationUnknown       = pipelinemodel.ContractRepresentationUnknown
	ContractRepresentationRows          = pipelinemodel.ContractRepresentationRows
	ContractRepresentationJSON          = pipelinemodel.ContractRepresentationJSON
	ContractRepresentationCSV           = pipelinemodel.ContractRepresentationCSV
	ContractRepresentationFile          = pipelinemodel.ContractRepresentationFile
	ContractRepresentationMultipartForm = pipelinemodel.ContractRepresentationMultipartForm
)

func RuntimeVariableValueTypeString(s string) (RuntimeVariableValueType, error) {
	return pipelinemodel.RuntimeVariableValueTypeString(s)
}

func RuntimeVariableValueTypeValues() []RuntimeVariableValueType {
	return pipelinemodel.RuntimeVariableValueTypeValues()
}

func RuntimeVariableValueTypeStrings() []string {
	return pipelinemodel.RuntimeVariableValueTypeStrings()
}

func PipelineFormatString(s string) (PipelineFormat, error) {
	return pipelinemodel.PipelineFormatString(s)
}

func PipelineFormatValues() []PipelineFormat { return pipelinemodel.PipelineFormatValues() }

func PipelineFormatStrings() []string { return pipelinemodel.PipelineFormatStrings() }

func ContractFieldTypeString(s string) (ContractFieldType, error) {
	return pipelinemodel.ContractFieldTypeString(s)
}

func ContractFieldTypeValues() []ContractFieldType { return pipelinemodel.ContractFieldTypeValues() }

func ContractFieldTypeStrings() []string { return pipelinemodel.ContractFieldTypeStrings() }

func ContractRootShapeString(s string) (ContractRootShape, error) {
	return pipelinemodel.ContractRootShapeString(s)
}

func ContractRootShapeValues() []ContractRootShape { return pipelinemodel.ContractRootShapeValues() }

func ContractRootShapeStrings() []string { return pipelinemodel.ContractRootShapeStrings() }

func SinkBackendString(s string) (SinkBackend, error) { return pipelinemodel.SinkBackendString(s) }

func SinkBackendValues() []SinkBackend { return pipelinemodel.SinkBackendValues() }

func SinkBackendStrings() []string { return pipelinemodel.SinkBackendStrings() }

func ParameterDataTypeString(s string) (ParameterDataType, error) {
	return pipelinemodel.ParameterDataTypeString(s)
}

func ParameterDataTypeValues() []ParameterDataType { return pipelinemodel.ParameterDataTypeValues() }

func ParameterDataTypeStrings() []string { return pipelinemodel.ParameterDataTypeStrings() }

func TransformOutputKindString(s string) (TransformOutputKind, error) {
	return pipelinemodel.TransformOutputKindString(s)
}

func TransformOutputKindValues() []TransformOutputKind {
	return pipelinemodel.TransformOutputKindValues()
}

func TransformOutputKindStrings() []string { return pipelinemodel.TransformOutputKindStrings() }

func DataSourceTypeString(s string) (DataSourceType, error) {
	return pipelinemodel.DataSourceTypeString(s)
}

func DataSourceTypeValues() []DataSourceType { return pipelinemodel.DataSourceTypeValues() }

func DataSourceTypeStrings() []string { return pipelinemodel.DataSourceTypeStrings() }

func ContractRepresentationString(s string) (ContractRepresentation, error) {
	return pipelinemodel.ContractRepresentationString(s)
}

func ContractRepresentationValues() []ContractRepresentation {
	return pipelinemodel.ContractRepresentationValues()
}

func ContractRepresentationStrings() []string { return pipelinemodel.ContractRepresentationStrings() }

func IncrementalCheckpointKindString(s string) (IncrementalCheckpointKind, error) {
	return pipelinemodel.IncrementalCheckpointKindString(s)
}

func IncrementalCheckpointKindValues() []IncrementalCheckpointKind {
	return pipelinemodel.IncrementalCheckpointKindValues()
}

func IncrementalCheckpointKindStrings() []string {
	return pipelinemodel.IncrementalCheckpointKindStrings()
}
