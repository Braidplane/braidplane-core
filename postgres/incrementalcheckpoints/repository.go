package incrementalcheckpoints

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	internalpg "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Repository interface {
	EnsureCommitLock(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, lookup pipelinerun.IncrementalCheckpointLookup) error
	LockCommit(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, lookup pipelinerun.IncrementalCheckpointLookup) error

	BootstrapInitial(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, transition pipelinerun.IncrementalCheckpointTransition) error

	GetCurrentCommitted(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		lookup pipelinerun.IncrementalCheckpointLookup,
	) (*pipelinerun.IncrementalCheckpoint, error)
	CommitObservation(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		transition pipelinerun.IncrementalCheckpointTransition,
	) error
	RecordBlockedObservation(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		transition pipelinerun.IncrementalCheckpointTransition,
	) error
}

type repository struct{}

func New() Repository {
	return &repository{}
}

type checkpointQueryScope struct {
	orgSlug             slug.Slug
	projectSlug         slug.Slug
	environmentSlug     slug.Slug
	pipelineSlug        string
	sourceStepSlug      string
	checkpointNamespace string
	dataSourceSlug      string
	operationType       string
	operationRef        pgtype.Text
	operationInlineHash pgtype.Text
	operationIdentity   string
	pipelineVersionID   int
}

// newCheckpointQueryScope normalizes lookup identity fields for SQLC checkpoint queries.
func newCheckpointQueryScope(
	orgSlug, projectSlug, environmentSlug slug.Slug,
	lookup pipelinerun.IncrementalCheckpointLookup,
) checkpointQueryScope {
	return checkpointQueryScope{
		orgSlug:             orgSlug,
		projectSlug:         projectSlug,
		environmentSlug:     environmentSlug,
		pipelineSlug:        lookup.PipelineSlug.String(),
		sourceStepSlug:      lookup.SourceStepSlug.String(),
		checkpointNamespace: checkpointNamespace(lookup.CheckpointNamespace),
		dataSourceSlug:      lookup.DataSourceSlug.String(),
		operationType:       lookup.OperationType,
		operationRef:        postgres.ToText(lookup.OperationRef.String()),
		operationInlineHash: postgres.ToText(lookup.OperationInlineHash),
		operationIdentity:   lookup.OperationIdentity,
		pipelineVersionID:   int(lookup.PipelineVersionID),
	}
}

// currentParams maps the normalized checkpoint scope into the current-read SQLC shape.
func (s checkpointQueryScope) currentParams() internalpg.GetCurrentIncrementalCheckpointParams {
	return internalpg.GetCurrentIncrementalCheckpointParams{
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		OperationIdentity:   s.operationIdentity,
	}
}

// ensureLockParams maps the normalized checkpoint scope into the idempotent lock insert shape.
func (s checkpointQueryScope) ensureLockParams() internalpg.EnsureIncrementalCheckpointCommitLockParams {
	return internalpg.EnsureIncrementalCheckpointCommitLockParams{
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		OperationIdentity:   s.operationIdentity,
	}
}

// lockParams maps the normalized checkpoint scope into the row-lock SQLC shape.
func (s checkpointQueryScope) lockParams() internalpg.LockIncrementalCheckpointCommitParams {
	return internalpg.LockIncrementalCheckpointCommitParams{
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		OperationIdentity:   s.operationIdentity,
	}
}

// supersedeParams maps the normalized checkpoint scope into the committed-row supersede shape.
func (s checkpointQueryScope) supersedeParams(supersededAt pgtype.Timestamptz) internalpg.SupersedeCurrentIncrementalCheckpointParams {
	return internalpg.SupersedeCurrentIncrementalCheckpointParams{
		SupersededAt:        supersededAt,
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		OperationIdentity:   s.operationIdentity,
	}
}

// committedParams combines stable lookup identity with observed commit data for insertion.
func (s checkpointQueryScope) committedParams(
	transition pipelinerun.IncrementalCheckpointTransition,
	observedAt, committedAt time.Time,
) internalpg.InsertCommittedIncrementalCheckpointParams {
	return internalpg.InsertCommittedIncrementalCheckpointParams{
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		CheckpointKind:      transition.CheckpointKind,
		DataSourceSlug:      s.dataSourceSlug,
		OperationType:       s.operationType,
		OperationRef:        s.operationRef,
		OperationInlineHash: s.operationInlineHash,
		OperationIdentity:   s.operationIdentity,
		ObservedLowerBound:  jsonOrNull(transition.ObservedLowerBound),
		ObservedUpperBound:  jsonOrNull(transition.ObservedUpperBound),
		DisplayMetadata:     jsonOrEmptyObject(transition.DisplayMetadata),
		ObservedRunSlug:     transition.ObservedRunSlug,
		CommittingRunSlug:   postgres.ToText(transition.CommittingRunSlug),
		ObservedAt:          postgres.ToTimestamptz(observedAt),
		CommittedAt:         postgres.ToTimestamptz(committedAt),
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineVersionID:   s.pipelineVersionID,
	}
}

// blockedParams combines stable lookup identity with observed blocked data for insertion.
func (s checkpointQueryScope) blockedParams(
	transition pipelinerun.IncrementalCheckpointTransition,
	observedAt, blockedAt time.Time,
) internalpg.InsertBlockedIncrementalCheckpointParams {
	return internalpg.InsertBlockedIncrementalCheckpointParams{
		PipelineSlug:        s.pipelineSlug,
		SourceStepSlug:      s.sourceStepSlug,
		CheckpointNamespace: s.checkpointNamespace,
		CheckpointKind:      transition.CheckpointKind,
		DataSourceSlug:      s.dataSourceSlug,
		OperationType:       s.operationType,
		OperationRef:        s.operationRef,
		OperationInlineHash: s.operationInlineHash,
		OperationIdentity:   s.operationIdentity,
		ObservedLowerBound:  jsonOrNull(transition.ObservedLowerBound),
		ObservedUpperBound:  jsonOrNull(transition.ObservedUpperBound),
		DisplayMetadata:     jsonOrEmptyObject(transition.DisplayMetadata),
		ObservedRunSlug:     transition.ObservedRunSlug,
		BlockedReason:       postgres.ToText(transition.BlockedReason),
		ObservedAt:          postgres.ToTimestamptz(observedAt),
		BlockedAt:           postgres.ToTimestamptz(blockedAt),
		OrgSlug:             s.orgSlug,
		ProjectSlug:         s.projectSlug,
		EnvironmentSlug:     s.environmentSlug,
		PipelineVersionID:   s.pipelineVersionID,
	}
}

func (r *repository) GetCurrentCommitted(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	lookup pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, lookup)
	row, err := internalpg.New(tx).GetCurrentIncrementalCheckpoint(
		ctx,
		scope.currentParams(),
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, pipelinerun.ErrIncrementalCheckpointNotFound.With(err)
		}
		return nil, err
	}

	return toIncrementalCheckpoint(row), nil
}

func (r *repository) CommitObservation(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	transition pipelinerun.IncrementalCheckpointTransition,
) error {
	observedAt := transition.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	now := observedAt
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, lookupForTransition(transition))

	q := internalpg.New(tx)
	if err := q.EnsureIncrementalCheckpointCommitLock(
		ctx,
		scope.ensureLockParams(),
	); err != nil {
		return err
	}

	if _, err := q.LockIncrementalCheckpointCommit(
		ctx,
		scope.lockParams(),
	); err != nil {
		return err
	}

	if err := q.SupersedeCurrentIncrementalCheckpoint(
		ctx,
		scope.supersedeParams(postgres.ToTimestamptz(now)),
	); err != nil {
		return err
	}

	_, err := q.InsertCommittedIncrementalCheckpoint(
		ctx,
		scope.committedParams(transition, observedAt, now),
	)
	return err
}

func (r *repository) RecordBlockedObservation(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	transition pipelinerun.IncrementalCheckpointTransition,
) error {
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, lookupForTransition(transition))
	observedAt := transition.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	blockedAt := observedAt

	_, err := internalpg.New(tx).InsertBlockedIncrementalCheckpoint(
		ctx,
		scope.blockedParams(transition, observedAt, blockedAt),
	)
	return err
}

func toIncrementalCheckpoint(row internalpg.PipelineIncrementalCheckpoint) *pipelinerun.IncrementalCheckpoint {
	status, err := pipelinerun.IncrementalCheckpointStatusString(row.CommitStatus)
	if err != nil {
		status = pipelinerun.IncrementalCheckpointStatusUnknown
	}

	return &pipelinerun.IncrementalCheckpoint{
		PipelineSlug:        slug.Slug(row.PipelineSlug),
		PipelineVersionID:   row.PipelineVersionID,
		SourceStepSlug:      slug.Slug(row.SourceStepSlug),
		CheckpointNamespace: row.CheckpointNamespace,
		CheckpointKind:      row.CheckpointKind,
		DataSourceSlug:      slug.Slug(row.DataSourceSlug),
		OperationType:       row.OperationType,
		OperationRef:        slug.Slug(nullableTextString(row.OperationRef)),
		OperationInlineHash: nullableTextString(row.OperationInlineHash),
		OperationIdentity:   row.OperationIdentity,
		ObservedLowerBound:  rawJSON(row.ObservedLowerBound),
		ObservedUpperBound:  rawJSON(row.ObservedUpperBound),
		DisplayMetadata:     rawJSON(row.DisplayMetadata),
		CommitStatus:        status,
		ObservedRunSlug:     row.ObservedRunSlug,
		CommittingRunSlug:   nullableTextString(row.CommittingRunSlug),
		BlockedReason:       nullableTextString(row.BlockedReason),
		ObservedAt:          row.ObservedAt.Time,
		CommittedAt:         postgres.FromNullableTimestamptz(row.CommittedAt),
		SupersededAt:        postgres.FromNullableTimestamptz(row.SupersededAt),
		BlockedAt:           postgres.FromNullableTimestamptz(row.BlockedAt),
		ExpiredAt:           postgres.FromNullableTimestamptz(row.ExpiredAt),
		CreatedAt:           row.CreatedAt.Time,
	}
}

// lookupForTransition returns the canonical lookup used to address checkpoint storage.
func lookupForTransition(
	transition pipelinerun.IncrementalCheckpointTransition,
) pipelinerun.IncrementalCheckpointLookup {
	if transition.Lookup.OperationIdentity != "" {
		return transition.Lookup
	}
	return lookupFromTransition(transition)
}

func lookupFromTransition(
	transition pipelinerun.IncrementalCheckpointTransition,
) pipelinerun.IncrementalCheckpointLookup {
	return pipelinerun.IncrementalCheckpointLookup{
		PipelineSlug:        transition.Lookup.PipelineSlug,
		PipelineVersionID:   transition.Lookup.PipelineVersionID,
		SourceStepSlug:      transition.SourceStepSlug,
		CheckpointNamespace: transition.CheckpointNamespace,
		CheckpointKind:      transition.CheckpointKind,
		DataSourceSlug:      transition.DataSourceSlug,
		OperationType:       transition.OperationType,
		OperationRef:        transition.OperationRef,
		OperationInlineHash: transition.OperationInlineHash,
		OperationIdentity:   transition.OperationIdentity,
	}
}

func checkpointNamespace(value string) string {
	if value == "" {
		return pipelinerun.DefaultIncrementalCheckpointNamespace
	}
	return value
}

func jsonOrNull(value json.RawMessage) []byte {
	if len(value) == 0 {
		return []byte("null")
	}
	return value
}

func jsonOrEmptyObject(value json.RawMessage) []byte {
	if len(value) == 0 {
		return []byte("{}")
	}
	return value
}

func rawJSON(value []byte) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return json.RawMessage(value)
}

func nullableTextString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// BootstrapInitial never supersedes committed progress. The caller must hold the source commit lock.
func (r *repository) BootstrapInitial(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, transition pipelinerun.IncrementalCheckpointTransition) error {
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, transition.Lookup)
	params := scope.committedParams(transition, transition.ObservedAt, transition.ObservedAt)
	_, err := internalpg.New(tx).BootstrapIncrementalCheckpoint(ctx, internalpg.BootstrapIncrementalCheckpointParams(params))
	if xerror.Is(err, pgx.ErrNoRows) {
		return xerror.AlreadyExists(xerror.PipelineCheckpoint).With(err).Detail("source checkpoint is already initialized or the deployment is no longer available")
	}
	return err
}

func (r *repository) EnsureCommitLock(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, lookup pipelinerun.IncrementalCheckpointLookup) error {
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, lookup)
	return internalpg.New(tx).EnsureIncrementalCheckpointCommitLock(ctx, scope.ensureLockParams())
}

func (r *repository) LockCommit(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, lookup pipelinerun.IncrementalCheckpointLookup) error {
	scope := newCheckpointQueryScope(orgSlug, projectSlug, environmentSlug, lookup)
	_, err := internalpg.New(tx).LockIncrementalCheckpointCommit(ctx, scope.lockParams())
	return err
}
