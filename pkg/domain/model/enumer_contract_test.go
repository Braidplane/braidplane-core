package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPipelineSpecEnumJSONContracts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value interface {
			String() string
			MarshalJSON() ([]byte, error)
		}
		parse     func(string) (any, error)
		unmarshal func([]byte) (any, error)
		values    []string
		valid     func() bool
		invalid   func() bool
	}{
		{
			name:  "contract representation",
			value: ContractRepresentationJSON,
			parse: func(raw string) (any, error) {
				return ContractRepresentationString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value ContractRepresentation
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  ContractRepresentationStrings(),
			valid:   ContractRepresentationJSON.IsAContractRepresentation,
			invalid: ContractRepresentation(99).IsAContractRepresentation,
		},
		{
			name:  "contract root shape",
			value: ContractRootShapeArray,
			parse: func(raw string) (any, error) {
				return ContractRootShapeString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value ContractRootShape
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  ContractRootShapeStrings(),
			valid:   ContractRootShapeArray.IsAContractRootShape,
			invalid: ContractRootShape(99).IsAContractRootShape,
		},
		{
			name:  "contract field type",
			value: ContractFieldTypeValueString,
			parse: func(raw string) (any, error) {
				return ContractFieldTypeString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value ContractFieldType
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  ContractFieldTypeStrings(),
			valid:   ContractFieldTypeValueString.IsAContractFieldType,
			invalid: ContractFieldType(99).IsAContractFieldType,
		},
		{
			name:  "transform output kind",
			value: TransformOutputKindRows,
			parse: func(raw string) (any, error) {
				return TransformOutputKindString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value TransformOutputKind
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  TransformOutputKindStrings(),
			valid:   TransformOutputKindRows.IsATransformOutputKind,
			invalid: TransformOutputKind(99).IsATransformOutputKind,
		},
		{
			name:  "sink backend",
			value: SinkBackendDB,
			parse: func(raw string) (any, error) {
				return SinkBackendString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value SinkBackend
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  SinkBackendStrings(),
			valid:   SinkBackendDB.IsASinkBackend,
			invalid: SinkBackend(99).IsASinkBackend,
		},
		{
			name:  "pipeline format",
			value: PipelineFormatYAML,
			parse: func(raw string) (any, error) {
				return PipelineFormatString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value PipelineFormat
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  PipelineFormatStrings(),
			valid:   PipelineFormatYAML.IsAPipelineFormat,
			invalid: PipelineFormat(99).IsAPipelineFormat,
		},
		{
			name:  "project pipeline backend mode",
			value: SetProjectPipelineBackendModeAdoptExisting,
			parse: func(raw string) (any, error) {
				return SetProjectPipelineBackendModeString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value SetProjectPipelineBackendMode
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  SetProjectPipelineBackendModeStrings(),
			valid:   SetProjectPipelineBackendModeAdoptExisting.IsASetProjectPipelineBackendMode,
			invalid: SetProjectPipelineBackendMode(99).IsASetProjectPipelineBackendMode,
		},
		{
			name:  "incremental checkpoint kind",
			value: IncrementalCheckpointKindWatermark,
			parse: func(raw string) (any, error) {
				return IncrementalCheckpointKindString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value IncrementalCheckpointKind
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  IncrementalCheckpointKindStrings(),
			valid:   IncrementalCheckpointKindWatermark.IsAIncrementalCheckpointKind,
			invalid: IncrementalCheckpointKind(99).IsAIncrementalCheckpointKind,
		},
		{
			name:  "runtime variable value type",
			value: RuntimeVariableValueTypeText,
			parse: func(raw string) (any, error) {
				return RuntimeVariableValueTypeString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value RuntimeVariableValueType
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  RuntimeVariableValueTypeStrings(),
			valid:   RuntimeVariableValueTypeText.IsARuntimeVariableValueType,
			invalid: RuntimeVariableValueType(99).IsARuntimeVariableValueType,
		},
		{
			name:  "query parameter data type",
			value: ParameterDataTypeText,
			parse: func(raw string) (any, error) {
				return ParameterDataTypeString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value ParameterDataType
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  ParameterDataTypeStrings(),
			valid:   ParameterDataTypeText.IsAParameterDataType,
			invalid: ParameterDataType(99).IsAParameterDataType,
		},
		{
			name:  "environment color",
			value: EnvironmentColorBlue,
			parse: func(raw string) (any, error) {
				return EnvironmentColorString(raw)
			},
			unmarshal: func(raw []byte) (any, error) {
				var value EnvironmentColor
				err := json.Unmarshal(raw, &value)
				return value, err
			},
			values:  EnvironmentColorStrings(),
			valid:   EnvironmentColorBlue.IsAEnvironmentColor,
			invalid: EnvironmentColor(99).IsAEnvironmentColor,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text := tc.value.String()
			require.Contains(t, tc.values, text)
			require.True(t, tc.valid())
			require.False(t, tc.invalid())

			parsed, err := tc.parse(text)
			require.NoError(t, err)
			require.Equal(t, tc.value, parsed)
			parsed, err = tc.parse(upperASCII(text))
			require.NoError(t, err)
			require.Equal(t, tc.value, parsed)
			_, err = tc.parse("definitely-not-valid")
			require.Error(t, err)

			encoded, err := tc.value.MarshalJSON()
			require.NoError(t, err)
			require.JSONEq(t, `"`+text+`"`, string(encoded))
			decoded, err := tc.unmarshal(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.value, decoded)
			_, err = tc.unmarshal([]byte(`123`))
			require.Error(t, err)
			_, err = tc.unmarshal([]byte(`"definitely-not-valid"`))
			require.Error(t, err)
		})
	}
}

func TestTextEnumContracts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		text      string
		parse     func(string) (any, error)
		marshal   func() ([]byte, error)
		unmarshal func([]byte) (any, error)
		values    []string
		valid     func() bool
		invalid   func() bool
		want      any
	}{
		{
			name: "data source type",
			text: DataSourceTypePostgres.String(),
			parse: func(raw string) (any, error) {
				return DataSourceTypeString(raw)
			},
			marshal: DataSourceTypePostgres.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value DataSourceType
				err := value.UnmarshalText(raw)
				return value, err
			},
			values:  DataSourceTypeStrings(),
			valid:   DataSourceTypePostgres.IsADataSourceType,
			invalid: DataSourceType(99).IsADataSourceType,
			want:    DataSourceTypePostgres,
		},
		{
			name: "role",
			text: RoleSuperAdmin.String(),
			parse: func(raw string) (any, error) {
				return RoleString(raw)
			},
			marshal: RoleSuperAdmin.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value Role
				err := value.UnmarshalText(raw)
				return value, err
			},
			values:  RoleStrings(),
			valid:   RoleSuperAdmin.IsARole,
			invalid: Role(99).IsARole,
			want:    RoleSuperAdmin,
		},
		{
			name: "data source ref capability",
			text: DataSourceRefCapabilityPipelineSource.String(),
			parse: func(raw string) (any, error) {
				return DataSourceRefCapabilityString(raw)
			},
			marshal: DataSourceRefCapabilityPipelineSource.MarshalText,
			unmarshal: func(raw []byte) (any, error) {
				var value DataSourceRefCapability
				err := value.UnmarshalText(raw)
				return value, err
			},
			values:  DataSourceRefCapabilityStrings(),
			valid:   DataSourceRefCapabilityPipelineSource.IsADataSourceRefCapability,
			invalid: DataSourceRefCapability(99).IsADataSourceRefCapability,
			want:    DataSourceRefCapabilityPipelineSource,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Contains(t, tc.values, tc.text)
			require.True(t, tc.valid())
			require.False(t, tc.invalid())
			require.Equal(t, tc.text, string(mustMarshalText(t, tc.marshal)))

			parsed, err := tc.parse(tc.text)
			require.NoError(t, err)
			require.Equal(t, tc.want, parsed)
			parsed, err = tc.parse(upperASCII(tc.text))
			require.NoError(t, err)
			require.Equal(t, tc.want, parsed)
			_, err = tc.parse("definitely-not-valid")
			require.Error(t, err)

			decoded, err := tc.unmarshal([]byte(tc.text))
			require.NoError(t, err)
			require.Equal(t, tc.want, decoded)
			_, err = tc.unmarshal([]byte("definitely-not-valid"))
			require.Error(t, err)
		})
	}
}

func mustMarshalText(t *testing.T, marshal func() ([]byte, error)) []byte {
	t.Helper()

	value, err := marshal()
	require.NoError(t, err)
	return value
}

func upperASCII(value string) string {
	bytes := []byte(value)
	for i, b := range bytes {
		if b >= 'a' && b <= 'z' {
			bytes[i] = b - ('a' - 'A')
		}
	}
	return string(bytes)
}
