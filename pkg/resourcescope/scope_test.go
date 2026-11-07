package resourcescope

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/stretchr/testify/require"
)

func TestStatementFieldRolesRejectUnsupportedMeaning(t *testing.T) {
	t.Parallel()
	scope := Scope{PolicyID: S3PolicyID, DataSource: "store", Operation: "read", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &S3{Endpoint: "https://storage.example", Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true}}
	for name, statement := range map[string]string{
		"DynamicBucket":     `{"type":"s3","operation":"read","bucket":"{{bucket}}","key":"orders.json","format":"json"}`,
		"OtherBucket":       `{"type":"s3","operation":"read","bucket":"other","key":"orders.json","format":"json"}`,
		"DynamicFormat":     `{"type":"s3","operation":"read","key":"orders.json","format":"{{format}}"}`,
		"DynamicOverwrite":  `{"type":"s3","operation":"write","key":"orders.json","format":"json","overwrite":"{{overwrite}}"}`,
		"UnsupportedFormat": `{"type":"s3","operation":"read","key":"orders.json","format":"file"}`,
		"UnknownField":      `{"type":"s3","operation":"read","key":"orders.json","format":"json","url":"https://other.example"}`,
		"SQLIdentifiers":    `{"type":"postgres","sql":"SELECT * FROM {{table}}"}`,
		"Malformed":         `{"type":`,
		"NonObject":         `null`,
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, scope.ValidateStatement(json.RawMessage(statement)), ErrInvalid)
		})
	}
	require.NoError(t, scope.ValidateStatement(json.RawMessage(`{"type":"s3","operation":"read","key":"{{partition}}/orders.json","format":"json"}`)))
	scope.S3.DynamicKey = false
	require.ErrorIs(t, scope.ValidateStatement(json.RawMessage(`{"type":"s3","operation":"read","key":"{{partition}}/orders.json","format":"json"}`)), ErrInvalid)
	require.NoError(t, scope.ValidateStatement(json.RawMessage(`{"type":"s3","operation":"read","key":"orders.json","format":"json"}`)))
}
