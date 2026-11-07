package contractplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/stretchr/testify/require"
)

func scopedInput(t *testing.T) Input {
	t.Helper()
	in := testInput(t)
	in.Operations[0].DataSourceType = model.DataSourceTypeS3
	var pipeline pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(in.Authored, &pipeline))
	pipeline.Steps[0].Query.OperationType = "s3"
	pipeline.Steps[0].Query.OutputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row", Representation: model.ContractRepresentationRows}
	var err error
	in.Authored, err = json.Marshal(pipeline)
	require.NoError(t, err)
	in.Pins = []model.PinnedODCSPort{{StepSlug: "fetch", Port: "output", ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row", Digest: "sha256:" + strings.Repeat("a", 64), CompilerID: odcs.CompilerID, Shape: &payload.Contract{Name: "orders", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger}}}}}
	content := OperationContent{Data: json.RawMessage(`{"statement":"{\"type\":\"s3\",\"operation\":\"read\",\"key\":\"orders.json\",\"format\":\"json\"}"}`), ResourceScope: &resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "db", Operation: "orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test-region", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true}}}
	in.Operations[0].Content, err = json.Marshal(content)
	require.NoError(t, err)
	return in
}

func TestScopeIdentityReloadAndHistoricalMeaning(t *testing.T) {
	t.Parallel()
	in := scopedInput(t)
	raw, id, err := Compile(in)
	require.NoError(t, err)
	original, err := Read(raw, id)
	require.NoError(t, err)
	var content OperationContent
	require.NoError(t, json.Unmarshal(in.Operations[0].Content, &content))
	content.ResourceScope.S3.DynamicKey = false
	in.Operations[0].Content, err = json.Marshal(content)
	require.NoError(t, err)
	changed, changedID, err := Compile(in)
	require.NoError(t, err)
	require.NotEqual(t, id, changedID)
	newPlan, err := Read(changed, changedID)
	require.NoError(t, err)
	require.Equal(t, original.AuthoredSpecHash, newPlan.AuthoredSpecHash)
	_, err = Read(raw, id)
	require.NoError(t, err)
	for _, version := range []int{1, 2} {
		original.FormatVersion = version
		original.CompilerID = map[int]string{1: "braidplane/contract-plan/v1", 2: "braidplane/contract-plan/v2"}[version]
		legacyRaw, err := json.Marshal(original)
		require.NoError(t, err)
		legacyRaw, err = canonical(legacyRaw)
		require.NoError(t, err)
		_, err = Read(legacyRaw, digest(legacyRaw))
		require.ErrorIs(t, err, ErrInvalid, "historical plans cannot acquire new scope claims")
	}
	legacyInput := testInput(t)
	legacyRaw, legacyID, err := Compile(legacyInput)
	require.NoError(t, err)
	legacy, err := Read(legacyRaw, legacyID)
	require.NoError(t, err)
	legacy.FormatVersion, legacy.CompilerID = 2, "braidplane/contract-plan/v2"
	legacyRaw, err = json.Marshal(legacy)
	require.NoError(t, err)
	legacyRaw, err = canonical(legacyRaw)
	require.NoError(t, err)
	legacy, err = Read(legacyRaw, digest(legacyRaw))
	require.NoError(t, err)
	_, err = legacy.RuntimePipeline()
	require.NoError(t, err)
}

func TestScopeRequiresExactObjectOperationAndSupportedPolicy(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*resourcescope.Scope){
		"OtherSource":               func(s *resourcescope.Scope) { s.DataSource = "other" },
		"OtherOperation":            func(s *resourcescope.Scope) { s.Operation = "other" },
		"OtherContract":             func(s *resourcescope.Scope) { s.Contract = "other" },
		"OtherRevision":             func(s *resourcescope.Scope) { s.Revision = "v2" },
		"OtherObject":               func(s *resourcescope.Scope) { s.ObjectID = "other" },
		"Latest":                    func(s *resourcescope.Scope) { s.Revision = "latest" },
		"UnknownPolicy":             func(s *resourcescope.Scope) { s.PolicyID = "sql/identifier/v1" },
		"UnsupportedRepresentation": func(s *resourcescope.Scope) { s.Representation = model.ContractRepresentationJSON },
		"CredentialURL":             func(s *resourcescope.Scope) { s.S3.Endpoint = "https://user:password@example.com" },
		"ImplicitEndpoint":          func(s *resourcescope.Scope) { s.S3.Endpoint = "" },
		"SecretScope":               func(s *resourcescope.Scope) { s.S3.Bucket = "{{runtime.secrets.bucket}}" },
		"EncodedPrefix":             func(s *resourcescope.Scope) { s.S3.Prefix = "%2e%2e/private" },
	} {
		t.Run(name, func(t *testing.T) {
			in := scopedInput(t)
			var content OperationContent
			require.NoError(t, json.Unmarshal(in.Operations[0].Content, &content))
			change(content.ResourceScope)
			var err error
			in.Operations[0].Content, err = json.Marshal(content)
			require.NoError(t, err)
			_, _, err = Compile(in)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}
