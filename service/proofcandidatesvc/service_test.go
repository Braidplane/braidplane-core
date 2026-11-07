package proofcandidatesvc

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

const validPipeline = `metadata:
  slug: acme-users-sync
  name: ACME Users Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
      output_contract:
        representation: rows
        fields:
          - name: email
            type: string
            required: true
  - kind: sink
    slug: acme-users-sync
    sink:
      idempotency_class: idempotent_with_key
      input: fetch-users
      input_contract:
        representation: rows
        fields:
          - name: email
            type: string
            required: true
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: upsert-users
`

func TestIngestIsIdempotentAndSupersedesChangedHead(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(mapping())
	events := &recordingEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(events))
	require.NoError(t, err)

	first, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	replay, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	updated, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("b"))
	require.NoError(t, err)
	updatedReplay, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("b"))
	require.NoError(t, err)

	require.Equal(t, first.CandidateSlug, replay.CandidateSlug)
	require.True(t, replay.Replay)
	require.NotEqual(t, first.CandidateSlug, updated.CandidateSlug)
	require.Equal(t, &first.CandidateSlug, updated.SupersedesCandidateSlug)
	require.Equal(t, updated.SupersedesCandidateSlug, updatedReplay.SupersedesCandidateSlug)
	require.NotEqual(t, first.HeadSHA, updated.HeadSHA)
	require.Equal(t, first.SpecHash, updated.SpecHash)
	require.Equal(t, "sha256:", first.SpecHash[:7])
	require.Len(t, events.params, 2, "a duplicate event must not emit a second audit record")
	require.Equal(t, first.SourceSHA256, events.params[0].Metadata["source_sha256"])
	require.Equal(t, map[string]any{
		"provider": "github", "provider_connection_id": "9001", "repository_id": "42",
		"change_request_id": "7", "source_ref": "refs/heads/feature", "target_ref": "refs/heads/main",
	}, selectedMetadata(events.params[0].Metadata, "provider", "provider_connection_id", "repository_id", "change_request_id", "source_ref", "target_ref"))
	rerequest := change("b")
	rerequest.Rerequest = true
	rerequested, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), rerequest)
	require.NoError(t, err)
	require.Equal(t, updated.CandidateSlug, rerequested.CandidateSlug)
	require.True(t, rerequested.Replay)
	require.Len(t, events.params, 3, "an explicit re-request must emit an audit record")
}

func TestIngestSpecHashMatchesPipelineVersionEncoding(t *testing.T) {
	t.Parallel()
	pipeline, err := pipelinevalidate.Load("pipeline.yaml", []byte(validPipeline))
	require.NoError(t, err)
	for _, reverse := range []bool{false, true} {
		if reverse {
			pipeline.Steps[0], pipeline.Steps[1] = pipeline.Steps[1], pipeline.Steps[0]
		}
		versionJSON, err := json.Marshal(pipeline)
		require.NoError(t, err)
		source := []byte(validPipeline)
		if reverse {
			source = versionJSON
		}
		repo := newFakeRepo(mapping())
		svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: source}, eventtest.NewProducer(&recordingEvents{}))
		require.NoError(t, err)
		result, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
		require.NoError(t, err)
		require.Equal(t, digest(versionJSON), result.SpecHash, "Proof must use the same compact typed JSON as pipeline versions, preserving authored step order")
	}
}

func selectedMetadata(metadata map[string]any, keys ...string) map[string]any {
	selected := make(map[string]any, len(keys))
	for _, key := range keys {
		selected[key] = metadata[key]
	}
	return selected
}

func TestIngestReturnsRerequestAuditFailure(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(mapping())
	events := &recordingEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(events))
	require.NoError(t, err)
	_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	events.err = xerror.Unexpected().Detail("audit failed")
	rerequest := change("a")
	rerequest.Rerequest = true
	_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), rerequest)
	require.ErrorIs(t, err, events.err)
}

func TestCloseRejectsStaleHeadAndReopenRestoresExactCandidate(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(mapping())
	events := &recordingEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(events))
	require.NoError(t, err)
	require.NoError(t, svc.Close(t.Context(), "acme", model.ExternalEventActor(), change("a")))

	created, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	require.ErrorIs(t, svc.Close(t.Context(), "acme", model.ExternalEventActor(), change("b")), gitprovider.ErrChangeStale)
	require.NoError(t, svc.Close(t.Context(), "acme", model.ExternalEventActor(), change("a")))
	require.NoError(t, svc.Close(t.Context(), "acme", model.ExternalEventActor(), change("a")))
	require.NotNil(t, repo.byKey[created.CandidateKey].ClosedAt)

	reopened, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	require.True(t, reopened.Replay)
	require.Nil(t, repo.byKey[created.CandidateKey].ClosedAt)
	require.Equal(t, []model.EventAction{
		model.EventActionReconcile, model.EventActionDelete, model.EventActionRestore,
	}, []model.EventAction{events.params[0].Action, events.params[1].Action, events.params[2].Action})
}

func TestCloseRejectsMalformedOrganizationSlug(t *testing.T) {
	t.Parallel()
	svc, err := New(testutil.FakeTransactionManager{}, newFakeRepo(mapping()), staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(&recordingEvents{}))
	require.NoError(t, err)
	require.ErrorIs(t, svc.Close(t.Context(), "Invalid Organization", model.ExternalEventActor(), change("a")), xerror.InvalidInput(xerror.OrganizationSlug))
}

func TestIngestFailsClosedForMissingOrAmbiguousMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mappings []proofrepo.Mapping
		want     error
	}{
		{name: "missing", want: ErrMappingNotFound},
		{name: "ambiguous", mappings: []proofrepo.Mapping{mapping(), mapping()}, want: ErrMappingAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sources := &recordingSource{data: []byte(validPipeline)}
			svc, err := New(testutil.FakeTransactionManager{}, newFakeRepo(tc.mappings...), sources, eventtest.NewProducer(nil))
			require.NoError(t, err)
			_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
			require.ErrorIs(t, err, tc.want)
			require.Zero(t, sources.calls)
		})
	}
}

func TestIngestRejectsProviderTargetSubstitution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*gitprovider.VerifiedChange)
	}{
		{name: "wrong installation", mutate: func(change *gitprovider.VerifiedChange) { change.ProviderConnectionID = "other-installation" }},
		{name: "wrong repository", mutate: func(change *gitprovider.VerifiedChange) { change.RepositoryID = "other-repository" }},
		{name: "wrong target ref", mutate: func(change *gitprovider.VerifiedChange) { change.TargetRef = "refs/heads/release" }},
		{name: "wrong pipeline path", mutate: func(change *gitprovider.VerifiedChange) { change.PipelinePath = "pipelines/other.pipeline.yaml" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate := change("a")
			tc.mutate(&candidate)
			svc, err := New(testutil.FakeTransactionManager{}, newFakeRepo(mapping()), staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(nil))
			require.NoError(t, err)
			_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), candidate)
			require.ErrorIs(t, err, ErrMappingNotFound)
		})
	}
}

func TestIngestPreservesExplicitProviderFailureStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "authentication", err: gitprovider.ErrChangeAuthentication},
		{name: "closed", err: gitprovider.ErrChangeClosed},
		{name: "stale", err: gitprovider.ErrChangeStale},
		{name: "permission lost", err: gitprovider.ErrChangePermissionLost},
		{name: "ref deleted", err: gitprovider.ErrChangeRefDeleted},
		{name: "path missing", err: gitprovider.ErrChangePathMissing},
		{name: "provider unavailable", err: gitprovider.ErrChangeUnavailable},
		{name: "commit unreachable", err: gitprovider.ErrChangeUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, err := New(testutil.FakeTransactionManager{}, newFakeRepo(mapping()), staticSource{err: tc.err}, eventtest.NewProducer(nil))
			require.NoError(t, err)
			_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
			require.ErrorIs(t, err, tc.err)
		})
	}
}

func TestIngestRejectsParseFailureWithoutPersistence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{name: "invalid", data: []byte("not: [valid"), want: ErrParseFailed},
		{name: "empty", data: nil, want: ErrParseFailed},
		{name: "oversized", data: make([]byte, maxSourceBytes+1), want: ErrSourceTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo(mapping())
			svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: tc.data}, eventtest.NewProducer(nil))
			require.NoError(t, err)
			_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, repo.byKey)
		})
	}
}

func TestIngestRejectsMappingChangedDuringTransaction(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(mapping())
	changed := mapping()
	changed.EnvironmentSlug = "production"
	repo.mappingsAfterFirst = []proofrepo.Mapping{changed}
	svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(nil))
	require.NoError(t, err)

	_, err = svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))

	require.ErrorIs(t, err, ErrMappingAmbiguous)
	require.ErrorContains(t, err, "mapping changed during ingestion")
	require.Empty(t, repo.byKey)
}

func mapping() proofrepo.Mapping {
	return proofrepo.Mapping{
		ID: 1, OrganizationSlug: must.Slug("acme"), ProjectSlug: must.Slug("data"),
		EnvironmentSlug: must.Slug("staging"), ProviderConnectionSlug: must.Slug("github"),
		Provider: "github", ProviderConnectionID: "9001", RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/acme.pipeline.yaml",
	}
}

func change(fill string) gitprovider.VerifiedChange {
	return gitprovider.VerifiedChange{
		Provider: repostore.ProviderGitHub, ProviderConnectionID: "9001", RepositoryID: "42",
		ChangeRequestID: "7", HeadSHA: fill + "123456789012345678901234567890123456789",
		SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
		PipelinePath: "pipelines/acme.pipeline.yaml",
	}
}

type staticSource struct {
	data []byte
	err  error
}

func (s staticSource) VerifySource(context.Context, proofrepo.Mapping, gitprovider.VerifiedChange) ([]byte, error) {
	return append([]byte(nil), s.data...), s.err
}

type recordingSource struct {
	data  []byte
	calls int
}

func (s *recordingSource) VerifySource(context.Context, proofrepo.Mapping, gitprovider.VerifiedChange) ([]byte, error) {
	s.calls++
	return append([]byte(nil), s.data...), nil
}

type fakeRepo struct {
	mappings           []proofrepo.Mapping
	mappingsAfterFirst []proofrepo.Mapping
	resolveCalls       int
	byKey              map[string]*proofrepo.Candidate
	latest             map[string]*proofrepo.Candidate
	nextID             int64
}

func newFakeRepo(mappings ...proofrepo.Mapping) *fakeRepo {
	return &fakeRepo{mappings: mappings, byKey: map[string]*proofrepo.Candidate{}, latest: map[string]*proofrepo.Candidate{}, nextID: 1}
}

func (*fakeRepo) CreateMapping(context.Context, postgres.DBTX, proofrepo.CreateMappingParams) (int64, error) {
	return 0, nil
}

func (r *fakeRepo) ResolveMappings(_ context.Context, _ postgres.DBTX, lookup proofrepo.MappingLookup) ([]proofrepo.Mapping, error) {
	r.resolveCalls++
	mappings := r.mappings
	if r.resolveCalls > 1 && r.mappingsAfterFirst != nil {
		mappings = r.mappingsAfterFirst
	}
	var out []proofrepo.Mapping
	for _, item := range mappings {
		if item.OrganizationSlug == lookup.OrganizationSlug && item.Provider == lookup.Provider && item.ProviderConnectionID == lookup.ProviderConnectionID &&
			(lookup.MappingID == 0 || lookup.MappingID == item.ID) && item.RepositoryID == lookup.RepositoryID && item.TargetRef == lookup.TargetRef && item.PipelinePath == lookup.PipelinePath {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *fakeRepo) ResolveWebhookMappings(_ context.Context, _ postgres.DBTX, lookup proofrepo.WebhookMappingLookup) ([]proofrepo.Mapping, error) {
	var out []proofrepo.Mapping
	for _, item := range r.mappings {
		if item.Provider == lookup.Provider && item.ProviderConnectionID == lookup.ProviderConnectionID &&
			item.RepositoryID == lookup.RepositoryID && item.TargetRef == lookup.TargetRef {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *fakeRepo) FindByKey(_ context.Context, _ postgres.DBTX, _ slug.Slug, key string) (*proofrepo.Candidate, error) {
	candidate, ok := r.byKey[key]
	if !ok {
		return nil, proofrepo.ErrCandidateNotFound
	}
	return candidate, nil
}

func (r *fakeRepo) FindByID(_ context.Context, _ postgres.DBTX, _ slug.Slug, id int64) (*proofrepo.Candidate, error) {
	for _, candidate := range r.byKey {
		if candidate.ID == id {
			return candidate, nil
		}
	}
	return nil, proofrepo.ErrCandidateNotFound
}

func (r *fakeRepo) FindLatest(_ context.Context, _ postgres.DBTX, _ slug.Slug, mappingID int64, changeID string) (*proofrepo.Candidate, error) {
	candidate, ok := r.latest[latestKey(mappingID, changeID)]
	if !ok {
		return nil, proofrepo.ErrCandidateNotFound
	}
	return candidate, nil
}

func (r *fakeRepo) CloseLatest(_ context.Context, _ postgres.DBTX, _ slug.Slug, mappingID int64, changeID, headSHA string) (*proofrepo.Candidate, error) {
	candidate, ok := r.latest[latestKey(mappingID, changeID)]
	if !ok || candidate.HeadSHA != headSHA {
		return nil, proofrepo.ErrCandidateNotFound
	}
	now := time.Now()
	candidate.ClosedAt = &now
	return candidate, nil
}

func (r *fakeRepo) ReopenByKey(_ context.Context, _ postgres.DBTX, _ slug.Slug, candidateKey string) (*proofrepo.Candidate, error) {
	candidate, ok := r.byKey[candidateKey]
	if !ok {
		return nil, proofrepo.ErrCandidateNotFound
	}
	candidate.ClosedAt = nil
	return candidate, nil
}

func (r *fakeRepo) Create(_ context.Context, _ postgres.DBTX, _ slug.Slug, params proofrepo.CreateCandidateParams) (*proofrepo.Candidate, error) {
	candidate := &proofrepo.Candidate{
		ID: r.nextID, MappingID: params.MappingID, Slug: params.Slug, CandidateKey: params.CandidateKey,
		ChangeRequestID: params.ChangeRequestID, SourceRef: params.SourceRef, HeadSHA: params.HeadSHA,
		SourceSHA256: params.SourceSHA256, SpecHash: params.SpecHash, CanonicalSpec: params.CanonicalSpec,
		SupersedesCandidateID: params.SupersedesCandidateID, CreatedAt: time.Now(),
	}
	r.nextID++
	r.byKey[params.CandidateKey] = candidate
	r.latest[latestKey(params.MappingID, params.ChangeRequestID)] = candidate
	return candidate, nil
}

func latestKey(mappingID int64, changeID string) string {
	return strconv.FormatInt(mappingID, 10) + ":" + changeID
}

type recordingEvents struct {
	params []eventssvc.CreateEventParams
	err    error
}

func (r *recordingEvents) CreateEvent(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	r.params = append(r.params, params)
	if r.err != nil {
		return nil, r.err
	}
	return &model.Event{Slug: params.Slug}, nil
}

func TestExplicitTargetsSupportSeveralEnvironmentsWithoutGuessing(t *testing.T) {
	first := mapping()
	second := first
	second.ID = 2
	second.EnvironmentSlug = "production"
	repo := newFakeRepo(first, second)
	svc, err := New(testutil.FakeTransactionManager{}, repo, staticSource{data: []byte(validPipeline)}, eventtest.NewProducer(&recordingEvents{}))
	require.NoError(t, err)
	_, err = svc.Ingest(t.Context(), first.OrganizationSlug, model.ExternalEventActor(), change("a"))
	require.ErrorIs(t, err, ErrMappingAmbiguous)
	a, err := svc.IngestTarget(t.Context(), first, model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	b, err := svc.IngestTarget(t.Context(), second, model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	require.Equal(t, first.EnvironmentSlug, a.EnvironmentSlug)
	require.Equal(t, second.EnvironmentSlug, b.EnvironmentSlug)
	require.NotEqual(t, a.CandidateSlug, b.CandidateSlug)
}

func (*fakeRepo) LockSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error { return nil }

func TestChangedODCSRevisionSupersedesExactCandidate(t *testing.T) {
	t.Parallel()
	pipeline, err := pipelinevalidate.Load("pipelines/orders.yaml", []byte(validPipeline))
	require.NoError(t, err)
	pipeline.Steps[0].Query.OutputContract = nil
	pipeline.Steps[0].Query.OutputContractRef = &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "order"}
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	source := &recordingSource{data: raw}
	repo := newFakeRepo(mapping())
	svc, err := New(testutil.FakeTransactionManager{}, repo, source, eventtest.NewProducer(&recordingEvents{}))
	require.NoError(t, err)
	first, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("a"))
	require.NoError(t, err)
	var previous []byte
	for _, candidate := range repo.byKey {
		if candidate.Slug == first.CandidateSlug {
			previous = append([]byte(nil), candidate.CanonicalSpec...)
		}
	}
	pipeline.Steps[0].Query.OutputContractRef.RevisionSlug = "v2"
	source.data, err = json.Marshal(pipeline)
	require.NoError(t, err)
	second, err := svc.Ingest(t.Context(), "acme", model.ExternalEventActor(), change("b"))
	require.NoError(t, err)
	require.NotEqual(t, first.CandidateSlug, second.CandidateSlug)
	require.NotEqual(t, first.SpecHash, second.SpecHash)
	require.Equal(t, &first.CandidateSlug, second.SupersedesCandidateSlug)
	for _, candidate := range repo.byKey {
		if candidate.Slug == first.CandidateSlug {
			require.Equal(t, string(previous), string(candidate.CanonicalSpec))
		}
	}
}
