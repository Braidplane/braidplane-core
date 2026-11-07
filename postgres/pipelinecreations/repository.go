// Package pipelinecreations persists proposals for pipelines not yet imported from Git.
package pipelinecreations

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = xerror.NotFound(xerror.Pipeline).Detail("pipeline creation proposal was not found")

type Row struct {
	Slug             slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	Request          json.RawMessage
	Result           json.RawMessage
	Status           string
	FailureCode      string
	PublishAttemptID string
}

type Repository struct{}

func New() *Repository { return &Repository{} }

func (*Repository) List(ctx context.Context, db postgres.DBTX, org, project, after slug.Slug, limit int) ([]*Row, error) {
	rows, err := dbgen.New(db).ListPipelineCreationProposals(ctx, dbgen.ListPipelineCreationProposalsParams{OrgSlug: org, ProjectSlug: project, AfterSlug: after, PageLimit: int64(limit)})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]*Row, len(rows))
	for index, row := range rows {
		result[index] = fromRow(dbgen.GetPipelineCreationProposalRow(row))
	}
	return result, nil
}

func (*Repository) Create(ctx context.Context, db postgres.DBTX, org, project slug.Slug, row *Row) (bool, error) {
	n, err := dbgen.New(db).CreatePipelineCreationProposal(ctx, dbgen.CreatePipelineCreationProposalParams{OrgSlug: org, ProjectSlug: project, MemberSlug: row.MemberSlug, CreationSlug: row.Slug, PipelineSlug: row.PipelineSlug.String(), Request: row.Request, PublishAttemptID: row.PublishAttemptID})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return n > 0, nil
}

func (*Repository) Get(ctx context.Context, db postgres.DBTX, org, project, creation slug.Slug) (*Row, error) {
	r, err := dbgen.New(db).GetPipelineCreationProposal(ctx, dbgen.GetPipelineCreationProposalParams{OrgSlug: org, ProjectSlug: project, CreationSlug: creation})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return fromRow(r), nil
}

func (*Repository) Find(ctx context.Context, db postgres.DBTX, org, project, pipeline slug.Slug) (*Row, error) {
	r, err := dbgen.New(db).FindPipelineCreationProposal(ctx, dbgen.FindPipelineCreationProposalParams{OrgSlug: org, ProjectSlug: project, PipelineSlug: pipeline.String()})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return fromRow(dbgen.GetPipelineCreationProposalRow(r)), nil
}

func (*Repository) Update(ctx context.Context, db postgres.DBTX, org, project slug.Slug, row *Row) (bool, error) {
	n, err := dbgen.New(db).UpdatePipelineCreationProposalResult(ctx, dbgen.UpdatePipelineCreationProposalResultParams{OrgSlug: org, ProjectSlug: project, CreationSlug: row.Slug, PublishAttemptID: row.PublishAttemptID, Status: row.Status, Result: row.Result, FailureCode: row.FailureCode})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return n > 0, nil
}

func fromRow(r dbgen.GetPipelineCreationProposalRow) *Row {
	return &Row{Slug: r.Slug, PipelineSlug: slug.Slug(r.PipelineSlug), MemberSlug: r.MemberSlug, Request: append(json.RawMessage(nil), r.Request...), Result: append(json.RawMessage(nil), r.Result...), Status: r.Status, FailureCode: r.FailureCode, PublishAttemptID: r.PublishAttemptID}
}
