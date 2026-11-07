package pipelines

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func TestPipelineRowTransformsPreserveVersionHashes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	currentVersionID := int64(42)
	specHash := "spec-hash-42"

	spec, err := toSpecFromFindRow(dbgen.FindPipelineSpecBySlugRow{
		Slug:                   slug.Slug("customer-sync"),
		Name:                   "Customer Sync",
		CurrentVersionID:       pgtype.Int8{Int64: currentVersionID, Valid: true},
		CurrentVersionSpecHash: pgtype.Text{String: specHash, Valid: true},
		SpecJsonb:              []byte(`{"metadata":{"name":"Customer Sync"}}`),
		SpecHash:               "draft-spec-hash",
		SyncStatus:             SyncStatusSynced.String(),
		SourcePath:             "pipelines/customer-sync.pipeline.yaml",
		SourceRef:              "main",
		LastAppliedCommit:      "abc123",
		CreatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:              pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.Equal(t, slug.Slug("customer-sync"), spec.Slug)
	require.Equal(t, currentVersionID, *spec.CurrentVersionID)
	require.Equal(t, specHash, *spec.CurrentVersionSpecHash)
	require.Equal(t, "pipelines/customer-sync.pipeline.yaml", *spec.SourcePath)
	require.Equal(t, now, spec.CreatedAt)
	require.Equal(t, now.Add(time.Minute), spec.UpdatedAt)

	listSpec, err := toSpecFromListRow(dbgen.ListPipelineSpecsByProjectRow{
		Slug:                   slug.Slug("customer-sync"),
		Name:                   "Customer Sync",
		CurrentVersionID:       pgtype.Int8{Int64: currentVersionID, Valid: true},
		CurrentVersionSpecHash: pgtype.Text{String: specHash, Valid: true},
		SpecJsonb:              []byte(`{"metadata":{"name":"Customer Sync"}}`),
		SpecHash:               "draft-spec-hash",
		SyncStatus:             SyncStatusDrifted.String(),
		SourcePath:             "pipelines/customer-sync.pipeline.yaml",
		SourceRef:              "main",
		LastAppliedCommit:      "abc123",
		CreatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:              pgtype.Timestamptz{Time: now, Valid: true},
	})
	require.NoError(t, err)
	require.Equal(t, SyncStatusDrifted, listSpec.SyncStatus)
	require.Equal(t, specHash, *listSpec.CurrentVersionSpecHash)
}

func TestPipelineDraftTransformsPreserveBaseAndCurrentVersionContext(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	basedOnVersionID := int64(11)
	currentVersionID := int64(12)

	draft := toDraftFromFindRow(dbgen.FindPipelineDraftBySlugRow{
		PipelineSlug:     "customer-sync",
		BasedOnVersionID: pgtype.Int8{Int64: basedOnVersionID, Valid: true},
		BasedOnSpecHash:  pgtype.Text{String: "base-spec-hash", Valid: true},
		CurrentVersionID: pgtype.Int8{Int64: currentVersionID, Valid: true},
		SpecJsonb:        []byte(`{"metadata":{"name":"Customer Sync Draft"}}`),
		CreatedAt:        pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	})
	require.Equal(t, slug.Slug("customer-sync"), draft.PipelineSlug)
	require.Equal(t, basedOnVersionID, *draft.BasedOnVersionID)
	require.Equal(t, "base-spec-hash", *draft.BasedOnSpecHash)
	require.Equal(t, currentVersionID, *draft.CurrentVersionID)

	anyDraft := toDraftFromAnyRow(dbgen.FindAnyPipelineDraftBySlugRow{
		PipelineSlug:     "customer-sync",
		BasedOnVersionID: pgtype.Int8{Int64: basedOnVersionID, Valid: true},
		BasedOnSpecHash:  pgtype.Text{String: "base-spec-hash", Valid: true},
		CurrentVersionID: pgtype.Int8{Int64: currentVersionID, Valid: true},
		SpecJsonb:        []byte(`{"metadata":{"name":"Customer Sync Draft"}}`),
		CreatedAt:        pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	})
	require.Equal(t, draft, anyDraft)
}

func TestPipelineTransformsHandleNullableFieldsAndInvalidEnums(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	versionID := int64(7)
	text := "value"

	require.Equal(t, pgtype.Text{String: text, Valid: true}, textOrNull(&text))
	require.False(t, textOrNull(nil).Valid)
	require.Equal(t, &text, nullableText(pgtype.Text{String: text, Valid: true}))
	require.Nil(t, nullableText(pgtype.Text{}))
	require.Equal(t, &versionID, nullableInt64(pgtype.Int8{Int64: versionID, Valid: true}))
	require.Nil(t, nullableInt64(pgtype.Int8{}))
	require.Equal(t, pgtype.Int8{Int64: versionID, Valid: true}, nullableInt8(&versionID))
	require.False(t, nullableInt8(nil).Valid)

	ref := toReference(dbgen.PipelineReference{
		Slug:             slug.Slug("customer-sync"),
		Name:             "Customer Sync",
		CurrentVersionID: pgtype.Int8{Int64: versionID, Valid: true},
		HasDraft:         true,
		CreatedAt:        pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:        pgtype.Timestamptz{Time: now, Valid: true},
	})
	require.Equal(t, versionID, *ref.CurrentVersionID)
	require.True(t, ref.HasDraft)

	version := toVersion(dbgen.PipelineVersion{
		ID:           9,
		PipelineSlug: "customer-sync",
		GitCommitSha: pgtype.Text{String: "abc123", Valid: true},
		SourcePath:   pgtype.Text{String: "pipelines/customer-sync.json", Valid: true},
		SpecJsonb:    []byte(`{"metadata":{"name":"Customer Sync"}}`),
		SpecHash:     "spec-hash-9",
		CreatedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	})
	require.Equal(t, int64(9), version.ID)
	require.Equal(t, "abc123", *version.GitCommitSHA)
	require.Equal(t, "pipelines/customer-sync.json", *version.SourcePath)

	_, err := toSpec(dbgen.PipelineSpec{SyncStatus: "bogus"})
	require.Error(t, err)
}
