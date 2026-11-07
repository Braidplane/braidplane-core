// Package contractplansvc persists portable compilation templates.
package contractplansvc

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
)

type ContractResolver interface {
	ResolvePipelineContracts(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error)
}

type DataSourceRefResolver interface {
	ResolvePipelineDataSourceRefs(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error)
}

type Service struct {
	versions       pipelines.Repository
	txm            postgres.TransactionManager
	plans          *compiledplans.Repository
	queries        queries.Repository
	contracts      odcscontracts.Repository
	resolver       ContractResolver
	dataSourceRefs DataSourceRefResolver
}

type Request struct {
	Pipeline         *pipelinespec.Pipeline
	Source           contractplan.Source
	Projection       *odcs.ProjectionRequest
	ResourceScopes   []OperationScope
	OperationSources map[slug.Slug]slug.Slug
}

type OperationScope struct {
	Step  slug.Slug
	Role  string
	Scope resourcescope.Scope
}

func New(versions pipelines.Repository, txm postgres.TransactionManager, plans *compiledplans.Repository, operations queries.Repository, contracts odcscontracts.Repository, resolver ContractResolver, dataSourceRefs DataSourceRefResolver) (*Service, error) {
	if versions == nil || txm == nil || plans == nil || operations == nil || contracts == nil || resolver == nil || dataSourceRefs == nil {
		return nil, xerror.Unexpected().Detail("contractplansvc requires version, transaction, plan, operation, catalog, contract and data-source resolver dependencies")
	}
	return &Service{versions: versions, txm: txm, plans: plans, queries: operations, contracts: contracts, resolver: resolver, dataSourceRefs: dataSourceRefs}, nil
}

var (
	ErrSourceUnavailable = xerror.New(xerror.KindInvalidInput, "PIPELINE_VERSION_SOURCE_UNAVAILABLE", "pipeline version has no retained source commit and path")
	ErrOperationSource   = xerror.New(xerror.KindInvalidInput, "PIPELINE_OPERATION_SOURCE_REQUIRED", "select an environment with bindings for saved-operation compilation")
)

// CompileVersion accepts only a complete retained version identity. Provenance
// comes from that row, never the mutable current pipeline or a caller's source.
func (s *Service) CompileVersion(ctx context.Context, org, project, pipeline slug.Slug, specHash string, environment slug.Slug, projection *odcs.ProjectionRequest, scopes []OperationScope) (*Summary, error) {
	if len(specHash) != 64 || strings.Trim(specHash, "0123456789abcdef") != "" {
		return nil, xerror.InvalidInput(xerror.PipelineVersion).Detail("a full immutable version hash is required")
	}
	var snapshot *compiledplans.Snapshot
	err := s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		version, err := s.versions.FindVersionBySpecHash(ctx, db, org, project, pipeline, specHash)
		if err != nil {
			return err
		}
		if version.GitCommitSHA == nil || version.SourcePath == nil {
			return ErrSourceUnavailable
		}
		authored, err := pipelinespec.Load("retained.pipeline.json", version.SpecJSONB)
		if err != nil {
			return contractplan.ErrInvalid.With(err)
		}
		if authored.Metadata.Slug != pipeline || version.SpecHash != specHash {
			return contractplan.ErrInvalid
		}
		sources, err := s.operationSources(ctx, org, project, environment, authored)
		if err != nil {
			return err
		}
		snapshot, err = s.Compile(ctx, org, project, Request{Pipeline: authored, Source: contractplan.Source{Commit: *version.GitCommitSHA, Path: *version.SourcePath}, OperationSources: sources, Projection: projection, ResourceScopes: scopes})
		if err == nil && projection != nil {
			plan, readErr := contractplan.Read(snapshot.Materialization, snapshot.Digest)
			if readErr != nil {
				return readErr
			}
			if err := plan.ValidateProjectionBindings(); err != nil {
				return err
			}
		}
		if err == nil && snapshot.AuthoredSpecHash != specHash {
			return contractplan.ErrInvalid
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return summarize(snapshot), nil
}

func (s *Service) operationSources(ctx context.Context, org, project, environment slug.Slug, pipeline *pipelinespec.Pipeline) (map[slug.Slug]slug.Slug, error) {
	needsOperations := false
	for _, step := range pipeline.Steps {
		needsOperations = needsOperations || step.Query != nil && step.Query.OperationRef != "" || step.Sink != nil && step.Sink.OperationRef != ""
	}
	if !needsOperations {
		return map[slug.Slug]slug.Slug{}, nil
	}
	if environment == "" {
		return nil, ErrOperationSource
	}
	resolved, err := s.dataSourceRefs.ResolvePipelineDataSourceRefs(ctx, org, project, environment, pipeline)
	if err != nil {
		return nil, err
	}
	if resolved == nil || resolved.Pipeline == nil {
		return nil, ErrOperationSource
	}
	sources := make(map[slug.Slug]slug.Slug, len(resolved.Resolutions))
	for _, resolution := range resolved.Resolutions {
		sources[resolution.RefSlug] = resolution.DataSourceSlug
	}
	return sources, nil
}

// Get reads only the selected retained identity.
func (s *Service) Get(ctx context.Context, org, project, pipeline slug.Slug, digest string) (*Summary, error) {
	var snapshot *compiledplans.Snapshot
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		snapshot, err = s.plans.Find(ctx, db, org, project, pipeline, digest)
		return err
	})
	if err != nil {
		return nil, err
	}
	return summarize(snapshot), nil
}

// Compile explicitly snapshots current authoring inputs once. Recompilation can
// create another plan for identical authored content without changing old pins.
func (s *Service) Compile(ctx context.Context, org, project slug.Slug, request Request) (*compiledplans.Snapshot, error) {
	authored, projection := request.Pipeline, request.Projection
	if projection != nil && (len(projection.Inputs) == 0 || len(projection.Inputs) > odcs.MaxProjectionInputs) {
		return nil, contractplan.ErrInvalid
	}
	if authored == nil || len(request.ResourceScopes) > 128 {
		return nil, contractplan.ErrInvalid
	}
	raw, err := json.Marshal(authored)
	if err != nil {
		return nil, contractplan.ErrInvalid.With(err)
	}
	// A private copy prevents resolution and sorting from mutating caller data.
	pipeline, err := pipelinespec.Load("compiled.pipeline.json", raw)
	if err != nil {
		return nil, contractplan.ErrInvalid.With(err)
	}
	var stored *compiledplans.Snapshot
	err = s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		input := contractplan.Input{Source: request.Source, Authored: raw}
		if projection != nil {
			encoded, err := json.Marshal(projection)
			if err != nil {
				return contractplan.ErrInvalid.With(err)
			}
			input.Projection = new(odcs.ProjectionRequest)
			if err := json.Unmarshal(encoded, input.Projection); err != nil {
				return contractplan.ErrInvalid.With(err)
			}
			projectionBytes := len(encoded)
			if projectionBytes > odcs.MaxProjectionBytes {
				return contractplan.ErrInvalid
			}
			for i := range input.Projection.Inputs {
				selected := &input.Projection.Inputs[i]
				contract, err := slug.New(selected.Contract)
				if err != nil {
					return contractplan.ErrInvalid.With(err)
				}
				revision, err := slug.New(selected.Revision)
				if err != nil {
					return contractplan.ErrInvalid.With(err)
				}
				stored, err := s.contracts.GetRevision(ctx, db, org, project, contract, revision)
				if err != nil {
					return err
				}
				if selected.Digest != stored.Digest {
					return contractplan.ErrInvalid
				}
				projectionBytes += len(stored.Document) - len(selected.Document)
				if projectionBytes > odcs.MaxProjectionBytes {
					return contractplan.ErrInvalid
				}
				selected.Document = stored.Document
			}
		}
		var pins []model.PinnedODCSPort
		var selected *odcs.ProjectionPlan
		if input.Projection != nil {
			result, projectionErr := odcs.CompileProjection(*input.Projection)
			if projectionErr != nil {
				return projectionErr
			}
			if result.Plan == nil || len(result.Diagnostics) != 0 {
				return contractplan.ErrInvalid
			}
			selected = result.Plan
			pins, err = contractplan.CapturePipelinePins(pipeline, selected, func(ref *pipelinespec.ODCSContractRef) (*model.ODCSRevision, error) {
				return s.contracts.GetRevision(ctx, db, org, project, ref.ContractSlug, ref.RevisionSlug)
			})
		} else {
			_, pins, err = s.resolver.ResolvePipelineContracts(ctx, org, project, pipeline)
		}
		if err != nil {
			return err
		}
		input.Pins = pins
		for _, step := range pipeline.Steps {
			if q := step.Query; q != nil && q.OperationRef != "" {
				op, err := s.operation(ctx, db, org, step.Slug, "query", q.DataSourceSlug, request.OperationSources[q.DataSourceSlug], q.OperationRef, project, selected)
				if err != nil {
					return err
				}
				input.Operations = append(input.Operations, op)
			}
			if sink := step.Sink; sink != nil && sink.OperationRef != "" {
				op, err := s.operation(ctx, db, org, step.Slug, "sink", sink.DataSourceSlug, request.OperationSources[sink.DataSourceSlug], sink.OperationRef, project, selected)
				if err != nil {
					return err
				}
				input.Operations = append(input.Operations, op)
			}
		}
		for _, binding := range request.ResourceScopes {
			matched := false
			for i := range input.Operations {
				op := &input.Operations[i]
				if op.Step != binding.Step || op.Role != binding.Role {
					continue
				}
				var content contractplan.OperationContent
				if err := json.Unmarshal(op.Content, &content); err != nil || content.ResourceScope != nil {
					return contractplan.ErrInvalid.With(err)
				}
				content.ResourceScope = &binding.Scope
				op.Content, err = json.Marshal(content)
				if err != nil {
					return contractplan.ErrInvalid.With(err)
				}
				matched = true
			}
			if !matched {
				return contractplan.ErrInvalid
			}
		}
		materialization, id, err := contractplan.Compile(input)
		if err != nil {
			return err
		}
		plan, err := contractplan.Read(materialization, id)
		if err != nil {
			return err
		}
		// Select by full compiled identity, never by authored hash or Git commit.
		stored, err = s.plans.Find(ctx, db, org, project, pipeline.Metadata.Slug, id)
		if err == nil {
			_, err = validateSnapshot(stored, pipeline.Metadata.Slug, id)
			return err
		}
		if !xerror.Is(err, compiledplans.ErrNotFound) {
			return err
		}
		stored, err = s.plans.Create(ctx, db, org, project, compiledplans.CreateParams{PipelineSlug: pipeline.Metadata.Slug, Digest: id, AuthoredSpecHash: plan.AuthoredSpecHash, FormatVersion: contractplan.FormatVersion, Materialization: materialization})
		if xerror.Is(err, compiledplans.ErrNotFound) {
			// Another compiler inserted the same identity; read its immutable row.
			stored, err = s.plans.Find(ctx, db, org, project, pipeline.Metadata.Slug, id)
			if err == nil {
				_, err = validateSnapshot(stored, pipeline.Metadata.Slug, id)
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, err
}

func (s *Service) operation(ctx context.Context, db postgres.DBTX, org, step slug.Slug, role string, source, operationSource, reference, project slug.Slug, projection *odcs.ProjectionPlan) (contractplan.Operation, error) {
	if operationSource == "" {
		return contractplan.Operation{}, ErrOperationSource
	}
	q, err := s.queries.FindSnapshot(ctx, db, org, operationSource, reference)
	if err != nil {
		return contractplan.Operation{}, err
	}
	content := contractplan.OperationContent{Data: q.Data, ExpectedInputContract: q.ExpectedInputContract, OutputContract: q.OutputContract}
	if len(q.ExpectedInputContractRef) > 0 {
		if err := json.Unmarshal(q.ExpectedInputContractRef, &content.ExpectedInputContractRef); err != nil {
			return contractplan.Operation{}, contractplan.ErrInvalid.With(err)
		}
	}
	if len(q.OutputContractRef) > 0 {
		if err := json.Unmarshal(q.OutputContractRef, &content.OutputContractRef); err != nil {
			return contractplan.Operation{}, contractplan.ErrInvalid.With(err)
		}
	}

	// Capture project-scoped ODCS pins inside this operation's own identity.
	for _, attachment := range []struct {
		ref    *pipelinespec.ODCSContractRef
		port   string
		inline json.RawMessage
	}{
		{content.ExpectedInputContractRef, "input", content.ExpectedInputContract},
		{content.OutputContractRef, "output", content.OutputContract},
	} {
		if attachment.ref == nil {
			continue
		}
		if len(attachment.inline) > 0 && string(attachment.inline) != "null" {
			return contractplan.Operation{}, contractplan.ErrInvalid
		}
		ref := attachment.ref
		revision, err := s.contracts.GetRevision(ctx, db, org, project, ref.ContractSlug, ref.RevisionSlug)
		if err != nil {
			return contractplan.Operation{}, err
		}
		pin, err := contractplan.CapturePort(ref, revision, projection)
		if err != nil {
			return contractplan.Operation{}, err
		}
		pin.StepSlug, pin.Port = step, attachment.port
		content.ContractPins = append(content.ContractPins, pin)
	}

	if err := json.Unmarshal(q.Parameters, &content.Parameters); err != nil {
		return contractplan.Operation{}, xerror.Unexpected().With(err).Detail("decode saved operation parameter snapshot")
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return contractplan.Operation{}, xerror.Unexpected().With(err).Detail("encode saved operation snapshot")
	}
	return contractplan.Operation{Step: step, Role: role, DataSource: source, DataSourceType: q.DataSourceType, Slug: reference, Revision: q.UpdatedAt.UTC().Format(time.RFC3339Nano), Content: raw}, nil
}

func (s *Service) Reload(ctx context.Context, org, project, pipeline slug.Slug, digest string) (*contractplan.Plan, error) {
	var plan *contractplan.Plan
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		snapshot, err := s.plans.Find(ctx, db, org, project, pipeline, digest)
		if err != nil {
			return err
		}
		plan, err = validateSnapshot(snapshot, pipeline, digest)
		return err
	})
	if err != nil {
		return nil, err
	}
	return plan, err
}

func validateSnapshot(snapshot *compiledplans.Snapshot, pipeline slug.Slug, digest string) (*contractplan.Plan, error) {
	plan, err := contractplan.Read(snapshot.Materialization, digest)
	if err != nil {
		return nil, err
	}
	var authored struct {
		Metadata struct {
			Slug slug.Slug `json:"slug"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(plan.Authored, &authored); err != nil || authored.Metadata.Slug != pipeline || snapshot.PipelineSlug != pipeline || snapshot.Digest != digest || snapshot.FormatVersion != plan.FormatVersion || snapshot.AuthoredSpecHash != plan.AuthoredSpecHash {
		return nil, contractplan.ErrInvalid.With(err)
	}
	return plan, nil
}
