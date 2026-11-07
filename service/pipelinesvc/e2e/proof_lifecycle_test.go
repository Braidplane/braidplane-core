package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	proofevents "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestService_ProofLifecycleRequiresExactHeadAndTenant_Integration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof lifecycle integration test in short mode")
	}
	ctx := t.Context()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	svc := newPipelineDeploymentIntegrationService(t, pool, txm, store)
	orgA := must.Slug(fmt.Sprintf("proof-lifecycle-a-%d", time.Now().UnixNano()))
	orgB := must.Slug(fmt.Sprintf("proof-lifecycle-b-%d", time.Now().UnixNano()))
	environment := must.Slug("staging")
	pipelineSlug := must.Slug("proof-lifecycle")
	for _, org := range []slug.Slug{orgA, orgB} {
		seedPipelineDeploymentOrg(t, pool, org)
		seedPipelineDeploymentEnvironment(t, pool, org, defaultProjectSlug, environment)
	}
	_, err = pool.Exec(ctx, `
 WITH credential AS (
  INSERT INTO credentials (organization_id, slug, kind, status, encrypted_payload)
  SELECT id, 'github-credential', 'static_secret', 'active', decode('01', 'hex') FROM organizations WHERE slug=$1
  RETURNING id, organization_id
 ) INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active)
 SELECT organization_id, id, 'GitHub fixture', 'github', 'github', '{}', true FROM credential`, orgA)
	require.NoError(t, err)
	installation := e2edb.EnableGitHubProof(t, pool, orgA, "github", "42")
	t.Cleanup(func() {
		for _, table := range []string{"proof_observation_jobs", "proof_observation_policies", "proof_lifecycle_links", "proof_receipts", "proof_evidence", "proof_change_candidates", "proof_change_candidate_mappings"} {
			_, cleanupErr := pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)", orgA)
			require.NoError(t, cleanupErr)
		}
	})
	_, err = pool.Exec(ctx, `
 UPDATE projects SET pipeline_backend_data_source_id=ds.id, pipeline_backend_repo='https://github.com/fixture/pipelines.git',
 pipeline_backend_tracked_branch='main', pipeline_backend_pipeline_dir='pipelines'
 FROM data_sources ds JOIN organizations o ON o.id=ds.organization_id
 WHERE projects.organization_id=o.id AND projects.slug=$2 AND o.slug=$1 AND ds.slug='github'`, orgA, defaultProjectSlug)
	require.NoError(t, err)
	candidates := proofcandidates.New()
	sourceRef := "refs/heads/main"
	sourcePath := "pipelines/proof-lifecycle.pipeline.json"
	mapping, err := candidates.CreateMapping(ctx, pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: orgA, ProjectSlug: defaultProjectSlug, EnvironmentSlug: environment,
		ProviderConnectionSlug: "github", Provider: "github", ProviderConnectionID: installation,
		RepositoryID: "42", TargetRef: sourceRef, PipelinePath: sourcePath,
	})
	require.NoError(t, err)
	receipts := proofreceipts.New()
	head := strings.Repeat("a", 40)
	mergedHead := strings.Repeat("b", 40)
	actor := model.UserEventActor(must.Slug("proof-fixture-member"))

	// Each case is freshly materialized under the current migrated contract.
	// Distinct specs avoid the immutable version's canonical-hash deduplication.
	makeVersion := func(org slug.Slug, commit, name string) (*pipelinesrepo.PipelineSpec, []byte) {
		pipeline := pipelineDeploymentIntegrationPipeline(pipelineSlug, name)
		canonical, marshalErr := json.Marshal(pipeline)
		require.NoError(t, marshalErr)
		git := &lifecycleRepoStore{
			listFilesFn: func(context.Context, string) ([]repostore.FileEntry, error) {
				return []repostore.FileEntry{{Path: sourcePath}}, nil
			},
			readFileFn: func(context.Context, string) ([]byte, error) { return canonical, nil },
		}
		_, reconcileErr := store.ReconcileFromGit(ctx, org, defaultProjectSlug, git, pipelinesvc.ReconcileFromGitParams{
			PipelineDir: "pipelines", SourceRef: &sourceRef, LastAppliedCommit: &commit,
		})
		require.NoError(t, reconcileErr)
		spec, findErr := store.FindSpec(ctx, org, defaultProjectSlug, pipelineSlug)
		require.NoError(t, findErr)
		return spec, canonical
	}
	makeReceipt := func(id slug.Slug, spec *pipelinesrepo.PipelineSpec, canonical []byte) model.ProofReceipt {
		_, createErr := candidates.Create(ctx, pool, orgA, proofcandidates.CreateCandidateParams{
			MappingID: mapping, Slug: id, CandidateKey: id.String(), ChangeRequestID: "7",
			SourceRef: "refs/heads/proposal", HeadSHA: head, SourceSHA256: "sha256:" + strings.Repeat("c", 64),
			SpecHash: "sha256:" + *spec.CurrentVersionSpecHash, CanonicalSpec: canonical,
		})
		require.NoError(t, createErr)
		target, targetErr := receipts.FindCandidateTarget(ctx, pool, orgA, id)
		require.NoError(t, targetErr)
		binding, bindingErr := proofidentity.BindingFingerprint(nil)
		require.NoError(t, bindingErr)
		pipeline := pipelineDeploymentIntegrationPipeline(pipelineSlug, spec.Name)
		capability, capabilityErr := proofidentity.CapabilityFingerprint(pipeline, nil)
		require.NoError(t, capabilityErr)
		now := time.Now().UTC()
		level := model.ProofEvidenceLevelStatic
		receipt, receiptErr := model.NewProofReceipt(model.ProofReceipt{
			SchemaVersion: model.ProofReceiptSchemaVersion, ID: id,
			Candidate: model.ProofCandidateIdentity{
				OrganizationSlug: orgA, ProjectSlug: defaultProjectSlug, EnvironmentSlug: environment,
				ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: head,
				TargetRef: sourceRef, PipelinePath: sourcePath, SpecHash: target.SpecHash,
				BindingFingerprint: binding, CapabilityFingerprint: capability, CompilerVersion: "v1", PolicyVersion: "v1",
			},
			Claims: []model.ProofClaim{{
				Identity: model.ProofClaimIdentity{
					Family:              model.ProofClaimFamilySourceIdentity,
					PipelineVersionHash: target.SpecHash, SemanticPath: "/source",
				},
				Description: "synthetic source identity", RequiredEvidenceLevel: level, ActualEvidenceLevel: &level,
				ObservedAt: &now, Verdict: model.ProofVerdictPassed,
				Evidence: []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindValidation, Digest: "sha256:" + strings.Repeat("d", 64)}},
			}}, CreatedAt: now, RetainUntil: now.Add(time.Hour),
		}, strings.Clone)
		require.NoError(t, receiptErr)
		tx, txErr := pool.Begin(ctx)
		require.NoError(t, txErr)
		defer func() { _ = tx.Rollback(ctx) }()
		require.NoError(t, receipts.LockTarget(ctx, tx, id.String()))
		evidence, evidenceErr := receipts.CreateEvidence(ctx, tx, target, proofreceipts.EvidenceParams{
			Kind: "validation", Digest: "sha256:" + strings.Repeat("d", 64), SourceIdentifier: id.String(),
			BindingFingerprint: binding, CapabilityFingerprint: capability, ObservedAt: now,
		})
		require.NoError(t, evidenceErr)
		_, createErr = receipts.Create(ctx, tx, target, proofreceipts.CreateReceiptParams{
			EvidenceID: evidence.ID, ReceiptKey: id.String(), Receipt: receipt,
		})
		require.NoError(t, createErr)
		require.NoError(t, tx.Commit(ctx))
		return receipt
	}

	wrongSpec, canonical := makeVersion(orgA, mergedHead, "Merged version")
	wrongReceipt := makeReceipt("proof-wrong-head", wrongSpec, canonical)
	otherSpec, _ := makeVersion(orgB, head, "Other tenant version")
	for _, tc := range []struct {
		name string
		org  slug.Slug
		hash string
	}{
		{"MergedCommitCannotInheritSourceHeadReceipt", orgA, *wrongSpec.CurrentVersionSpecHash},
		{"OtherTenantCannotUseReceipt", orgB, *otherSpec.CurrentVersionSpecHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, deployErr := svc.Deploy(ctx, tc.org, defaultProjectSlug, environment, pipelineSlug, actor,
				pipelinesvc.DeployPipelineRequest{SpecHash: tc.hash, ProofReceiptSlug: &wrongReceipt.ID})
			require.ErrorIs(t, deployErr, pipelinesvc.ErrProofReceiptLinkMismatch)
			_, deploymentErr := svc.GetDeployment(ctx, tc.org, defaultProjectSlug, environment, pipelineSlug)
			require.ErrorIs(t, deploymentErr, pipelinesvc.ErrPipelineDeploymentNotFound)
			links, linkErr := receipts.ListLifecycleLinksByReceipt(ctx, pool, orgA, defaultProjectSlug, environment, wrongReceipt.ID)
			require.NoError(t, linkErr)
			require.Empty(t, links)
		})
	}
	exactSpec, canonical := makeVersion(orgA, head, "Exact source version")
	exact := makeReceipt("proof-exact-head", exactSpec, canonical)
	deployment, err := svc.Deploy(ctx, orgA, defaultProjectSlug, environment, pipelineSlug, actor,
		pipelinesvc.DeployPipelineRequest{SpecHash: *exactSpec.CurrentVersionSpecHash, ProofReceiptSlug: &exact.ID})
	require.NoError(t, err)
	require.NotNil(t, deployment.ProofLink)
	require.Equal(t, exact.ID, deployment.ProofLink.ReceiptSlug)
	runSlug := must.Slug("proof-linked-run")
	_, err = svc.StartRun(ctx, orgA, defaultProjectSlug, environment, pipelineSlug, &runSlug, nil, actor,
		pipelinerun.RunSourceSummary{
			Type: model.PipelineRunSourceTypeManual, Summary: "Synthetic lifecycle linkage",
			Entrypoint: model.PipelineRunSourceEntrypointUI, ActorType: actor.Type, ActorSlug: actor.Slug, ParameterKeys: []string{},
		}, nil)
	require.NoError(t, err)
	links, err := receipts.ListLifecycleLinksByReceipt(ctx, pool, orgA, defaultProjectSlug, environment, exact.ID)
	require.NoError(t, err)
	require.Len(t, links, 2)
	for _, link := range links {
		require.Equal(t, exact.ID, link.ReceiptSlug)
		if link.Kind == model.ProofLifecycleLinkKindRun {
			require.Equal(t, &runSlug, link.RunSlug)
		}
	}
	stored, err := receipts.FindBySlug(ctx, pool, orgA, defaultProjectSlug, environment, exact.ID)
	require.NoError(t, err)
	require.Equal(t, exact, stored.Value, "later links must not upgrade or rewrite the immutable receipt")

	// Synchronous execution (including verified-event dispatch) bypasses StartRun.
	syncRunSlug := must.Slug("proof-linked-sync-run")
	_, executeErr := svc.Execute(ctx, orgA, defaultProjectSlug, environment, pipelineSlug, &syncRunSlug, nil, actor,
		pipelinerun.RunSourceSummary{
			Type: model.PipelineRunSourceTypeManual, Summary: "Synthetic synchronous linkage",
			Entrypoint: model.PipelineRunSourceEntrypointUI, ActorType: actor.Type, ActorSlug: actor.Slug, ParameterKeys: []string{},
		}, nil)
	if executeErr != nil {
		require.ErrorIs(t, executeErr, pipelinesvc.ErrPipelineExecutionFailed)
	}
	syncRun, err := svc.GetRun(ctx, orgA, defaultProjectSlug, environment, syncRunSlug.String())
	require.NoError(t, err)
	require.Contains(t, []string{"succeeded", "failed"}, syncRun.Metadata.Status)
	require.NotNil(t, syncRun.ProofLink)
	require.Equal(t, exact.ID, syncRun.ProofLink.ReceiptSlug)

	pinnedRunSlug := must.Slug("proof-unmatched-pinned-run")
	_, executeErr = svc.ExecuteVersion(ctx, orgA, defaultProjectSlug, environment, pipelineSlug,
		*wrongSpec.CurrentVersionSpecHash, &pinnedRunSlug, nil, actor,
		pipelinerun.RunSourceSummary{
			Type: model.PipelineRunSourceTypeManual, Summary: "Synthetic unmatched pinned version",
			Entrypoint: model.PipelineRunSourceEntrypointUI, ActorType: actor.Type, ActorSlug: actor.Slug, ParameterKeys: []string{},
		}, nil)
	if executeErr != nil {
		require.ErrorIs(t, executeErr, pipelinesvc.ErrPipelineExecutionFailed)
	}
	pinnedRun, err := svc.GetRun(ctx, orgA, defaultProjectSlug, environment, pinnedRunSlug.String())
	require.NoError(t, err)
	require.Nil(t, pinnedRun.ProofLink, "a pinned run cannot inherit the current deployment receipt")
	links, err = receipts.ListLifecycleLinksByReceipt(ctx, pool, orgA, defaultProjectSlug, environment, exact.ID)
	require.NoError(t, err)
	require.Len(t, links, 3)
	for i := range links {
		link := &links[i]
		require.Equal(t, "matched", pipelinesvc.ProofLifecycleLinkStatus(link, link.CurrentSpecHash, link.CurrentIdentifier, time.Now()), "receipt view must compare each link with the same kind of runtime record")
	}
	stored, err = receipts.FindBySlug(ctx, pool, orgA, defaultProjectSlug, environment, exact.ID)
	require.NoError(t, err)
	require.Equal(t, exact, stored.Value)

	eventsService := eventssvc.New(txm, proofevents.New(), testutil.FakeClock{NowFn: time.Now})
	deletion := projectssvc.New(txm, projects.NewRepository(), nil, nil, nil, nil, nil, nil, eventtest.NewProducer(eventsService))
	system := model.SystemEventActor()
	_, err = pool.Exec(ctx, `INSERT INTO proof_observation_policies (organization_id,project_id,project_environment_id,version,enabled,worker_identity,policy)
 SELECT p.organization_id,p.id,pe.id,'fixture',true,'fixture','{}' FROM projects p JOIN organizations o ON o.id=p.organization_id
 JOIN project_environments pe ON pe.project_id=p.id JOIN environments e ON e.id=pe.environment_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3`, orgA, defaultProjectSlug, environment)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO proof_observation_jobs (organization_id,project_id,project_environment_id,candidate_id,data_source_id,slug,request_digest,request,authorization_decision,policy_version,worker_identity,connector_type,operation,state,reserved_requests,reserved_bytes,reserved_rows,reserved_cost,budget_expires_at,deadline_at,evidence_retain_until,metadata_retain_until)
 SELECT policy.organization_id,policy.project_id,policy.project_environment_id,c.id,m.data_source_id,'delete-'||state,'fixture','{}','{}','fixture','fixture','github','fixture',state,1,1,0,0,now()+interval '1 hour',now()+interval '1 hour',now()+interval '1 hour',now()+interval '2 hours'
 FROM proof_observation_policies policy JOIN organizations o ON o.id=policy.organization_id JOIN proof_change_candidates c ON c.organization_id=o.id AND c.slug=$2
 JOIN proof_change_candidate_mappings m ON m.organization_id=o.id AND m.id=c.mapping_id CROSS JOIN (VALUES ('pending'),('running')) states(state) WHERE o.slug=$1`, orgA, exact.ID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO execution_requests(id,organization_id,kind,state,request_ciphertext,deadline_at,expires_at)
 SELECT gen_random_uuid(),id,'proof_metadata','ambiguous',decode('01','hex'),now()+interval '1 hour',now()+interval '1 hour' FROM organizations WHERE slug=$1`, orgA)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA)
		require.NoError(t, err)
	})

	failedAudit := projectssvc.New(txm, projects.NewRepository(), nil, nil, nil, nil, nil, nil, failProjectDeletionProducer{})
	require.Error(t, failedAudit.DeleteProject(ctx, orgA, defaultProjectSlug, model.DeleteProjectOptions{}, system))
	_, err = receipts.FindBySlug(ctx, pool, orgA, defaultProjectSlug, environment, exact.ID)
	require.NoError(t, err, "required project audit failure rolls back cascading deletion")
	require.NoError(t, deletion.DeleteProject(ctx, orgA, defaultProjectSlug, model.DeleteProjectOptions{}, system))
	for _, table := range []string{"proof_observation_jobs", "proof_observation_policies", "proof_lifecycle_links", "proof_receipts", "proof_evidence", "proof_change_candidates", "proof_change_candidate_mappings"} {
		var count int
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)", orgA).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err = deletion.GetProject(ctx, orgB, defaultProjectSlug)
	require.NoError(t, err, "other tenant's project is unaffected")
	var active bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT is_active FROM data_sources WHERE slug='github' AND organization_id=(SELECT id FROM organizations WHERE slug=$1)`, orgA).Scan(&active))
	require.True(t, active, "source deletion remains separately owned")
	_, err = candidates.Create(ctx, pool, orgA, proofcandidates.CreateCandidateParams{MappingID: mapping, Slug: "late-candidate", CandidateKey: "late-candidate", ChangeRequestID: "7", SourceRef: sourceRef, HeadSHA: head, SourceSHA256: "sha256:" + strings.Repeat("c", 64), SpecHash: exact.Candidate.SpecHash, CanonicalSpec: canonical})
	require.ErrorIs(t, err, proofcandidates.ErrMappingTargetNotFound, "deleted mapping cannot accept late intake")
	var audits int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND entity_slug=$2 AND action='delete'`, orgA, defaultProjectSlug).Scan(&audits))
	require.Equal(t, 1, audits)
}

type failProjectDeletionProducer struct{ workerproducer.EventProducer }

func (failProjectDeletionProducer) EnqueueRequired(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) (workerproducer.EventRecordJob, error) {
	return workerproducer.EventRecordJob{}, xerror.Unavailable(xerror.Event)
}
