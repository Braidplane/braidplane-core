package contractplansvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/supplierfixture"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/stretchr/testify/require"
)

type versionRepository struct {
	pipelines.Repository
	find func(slug.Slug, slug.Slug, slug.Slug, string) (*pipelines.PipelineVersion, error)
}

type bindingResolver struct {
	resolve func(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error)
}

func (r bindingResolver) ResolvePipelineDataSourceRefs(ctx context.Context, org, project, environment slug.Slug, p *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
	return r.resolve(ctx, org, project, environment, p)
}

func TestOperationSourcesRequireExplicitScopedBindings(t *testing.T) {
	t.Parallel()
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Query: &pipelinespec.QueryRef{DataSourceSlug: "logical-source", OperationRef: "read"}},
		{Sink: &pipelinespec.SinkSpec{DataSourceSlug: "logical-sink", OperationRef: "write"}},
	}}
	for _, scenario := range []string{"missing environment", "binding error", "missing result", "resolved"} {
		t.Run(scenario, func(t *testing.T) {
			called := false
			s := &Service{dataSourceRefs: bindingResolver{resolve: func(_ context.Context, org, project, environment slug.Slug, authored *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
				called = true
				require.Equal(t, slug.Slug("org"), org)
				require.Equal(t, slug.Slug("project"), project)
				require.Equal(t, slug.Slug("dev"), environment)
				require.Same(t, pipeline, authored)
				if scenario == "binding error" {
					return nil, pipelines.ErrPipelineVersionNotFound
				}
				if scenario == "missing result" {
					return &datasourcerefssvc.ResolvedPipeline{}, nil
				}
				return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline, Resolutions: []model.ResolvedPipelineDataSourceRef{
					{RefSlug: "logical-source", DataSourceSlug: "physical-source"},
					{RefSlug: "logical-sink", DataSourceSlug: "physical-sink"},
				}}, nil
			}}}
			environment := slug.Slug("dev")
			if scenario == "missing environment" {
				environment = ""
			}
			sources, err := s.operationSources(t.Context(), "org", "project", environment, pipeline)
			switch scenario {
			case "resolved":
				require.NoError(t, err)
				require.Equal(t, map[slug.Slug]slug.Slug{"logical-source": "physical-source", "logical-sink": "physical-sink"}, sources)
			case "binding error":
				require.ErrorIs(t, err, pipelines.ErrPipelineVersionNotFound)
			default:
				require.ErrorIs(t, err, ErrOperationSource)
			}
			require.Equal(t, scenario != "missing environment", called)
		})
	}
	sources, err := (&Service{}).operationSources(t.Context(), "org", "project", "", &pipelinespec.Pipeline{})
	require.NoError(t, err)
	require.Empty(t, sources)
}

func (r versionRepository) FindVersionBySpecHash(_ context.Context, _ postgres.DBTX, org, project, pipeline slug.Slug, hash string) (*pipelines.PipelineVersion, error) {
	return r.find(org, project, pipeline, hash)
}

func TestCompileVersionRequiresFullIdentity(t *testing.T) {
	t.Parallel()
	s := &Service{}
	for _, count := range []int{0, odcs.MaxProjectionInputs + 1} {
		_, err := s.Compile(t.Context(), "org", "project", Request{Pipeline: &pipelinespec.Pipeline{}, Projection: &odcs.ProjectionRequest{Inputs: make([]odcs.ProjectionInput, count)}})
		require.ErrorIs(t, err, contractplan.ErrInvalid, "reject selection size before resolving project documents")
	}
	for _, identity := range []string{"", "latest", "abcdef", strings.Repeat("X", 64)} {
		_, err := s.CompileVersion(t.Context(), "org", "project", "sync", identity, "", nil, nil)
		require.Error(t, err)
	}
}

func TestCompileVersionUsesScopedRetainedProvenance(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	commit, path := strings.Repeat("b", 40), "pipelines/retained.json"
	for _, scenario := range []string{"missing", "missing source", "wrong pipeline", "invalid authored snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			s := &Service{txm: testutil.FakeTransactionManager{}, versions: versionRepository{find: func(org, project, pipeline slug.Slug, selected string) (*pipelines.PipelineVersion, error) {
				require.Equal(t, slug.Slug("org"), org)
				require.Equal(t, slug.Slug("project"), project)
				require.Equal(t, slug.Slug("sync"), pipeline)
				require.Equal(t, hash, selected)
				if scenario == "missing" {
					return nil, pipelines.ErrPipelineVersionNotFound
				}
				version := &pipelines.PipelineVersion{SpecHash: hash}
				if scenario == "wrong pipeline" {
					version.GitCommitSHA, version.SourcePath = &commit, &path
					version.SpecJSONB = []byte(`{"metadata":{"slug":"another","name":"Other"},"steps":[]}`)
				}
				if scenario == "invalid authored snapshot" {
					version.GitCommitSHA, version.SourcePath = &commit, &path
					version.SpecJSONB = []byte(`invalid-sensitive-authored-body`)
				}
				return version, nil
			}}}
			_, err := s.CompileVersion(t.Context(), "org", "project", "sync", hash, "", nil, nil)
			switch scenario {
			case "missing":
				require.ErrorIs(t, err, pipelines.ErrPipelineVersionNotFound)
			case "missing source":
				require.ErrorIs(t, err, ErrSourceUnavailable)
			default:
				require.ErrorIs(t, err, contractplan.ErrInvalid)
			}
		})
	}
}

type decimalOperationRepository struct {
	queries.Repository
	snapshot *queries.OperationSnapshot
}

func (r decimalOperationRepository) FindSnapshot(_ context.Context, _ postgres.DBTX, org, source, reference slug.Slug) (*queries.OperationSnapshot, error) {
	if org != "acme" || source != "physical-db" || reference != "offers" {
		return nil, ErrOperationSource
	}
	return r.snapshot, nil
}

type decimalRevisionRepository struct {
	odcscontracts.Repository
	revision *model.ODCSRevision
}

func (r decimalRevisionRepository) GetRevision(_ context.Context, _ postgres.DBTX, org, project, contract, revision slug.Slug) (*model.ODCSRevision, error) {
	return r.revision, nil
}

func TestSavedOperationDecimalReferencePinsSurviveSnapshot(t *testing.T) {
	t.Parallel()
	_, ref, err := supplierfixture.PinnedPipeline()
	require.NoError(t, err)
	document, err := supplierfixture.Files.ReadFile("data/supplier-offer.odcs.json")
	require.NoError(t, err)
	revision, err := odcs.Record(document, "supplier-offer")
	require.NoError(t, err)
	encoded, err := json.Marshal(ref)
	require.NoError(t, err)
	s := &Service{queries: decimalOperationRepository{snapshot: &queries.OperationSnapshot{DataSourceType: model.DataSourceTypePostgres, Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured offers\"}"}`), OutputContractRef: encoded, Parameters: json.RawMessage(`[]`)}}, contracts: decimalRevisionRepository{revision: revision}}
	op, err := s.operation(t.Context(), nil, "acme", "read-offers", "query", "logical-db", "physical-db", "offers", "etl", nil)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("logical-db"), op.DataSource)
	_, output, err := contractplan.OperationContracts(op)
	require.NoError(t, err)
	require.Equal(t, model.ContractFieldTypeValueDecimal, output.Fields[5].Type)
	require.Equal(t, 2, *output.Fields[5].Scale)
	require.Equal(t, 4, *output.Fields[6].Scale)
	var captured contractplan.OperationContent
	require.NoError(t, json.Unmarshal(op.Content, &captured))
	require.Len(t, captured.ContractPins, 1)
	require.Equal(t, odcs.DecimalCompilerID, captured.ContractPins[0].CompilerID)
	captured.ContractPins[0].CompilerID = "unknown"
	op.Content, err = json.Marshal(captured)
	require.NoError(t, err)
	_, _, err = contractplan.OperationContracts(op)
	require.ErrorIs(t, err, contractplan.ErrInvalid)
}
