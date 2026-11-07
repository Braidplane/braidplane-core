package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	bqconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	bigquery "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	workspaceconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	connectorsql "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	pgconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/stretchr/testify/require"
)

type unavailableObservationStorage struct{ err error }

func (s unavailableObservationStorage) RunInTx(context.Context, func(context.Context, postgres.DBTX) error) error {
	return s.err
}

func TestChangedConnectionSettingsInvalidateBoundMetadataPlan(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		observer          proofevidence.MetadataObserver
		original, changed string
	}{
		{pgconnector.TableMetadataObserver{TableMetadataResource: connectorsql.TableMetadataResource{Host: "database.example.com", Port: 5432, Database: "analytics", Schema: "public", Table: "orders"}}, `{}`, `{"ssl_mode":"verify-full"}`},
		{mongo.CollectionMetadataObserver{Host: "database.example.com", Port: 27017, Database: "analytics", Collection: "orders"}, `{}`, `{"auth_database":"changed"}`},
	} {
		plan, err := scenario.observer.Plan()
		require.NoError(t, err)
		source := &datasources.DataSource{Config: json.RawMessage(scenario.original), Credential: &datasources.CredentialMetadata{Kind: "static_secret"}}
		require.True(t, boundTargetMatches(source, plan))
		source.Config = json.RawMessage(scenario.changed)
		require.False(t, boundTargetMatches(source, plan))
	}
}

func (s unavailableObservationStorage) RunRead(context.Context, func(context.Context, postgres.DBTX) error) error {
	return s.err
}

func TestObservationStorageOutageCannotAuthorizeOrRefreshEvidence(t *testing.T) {
	t.Parallel()
	storageErr := xerror.Unexpected().Detail("private-storage-diagnostic")
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	events := eventrecorder.New(observationAuditStore{create: func(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
		require.Equal(t, false, params.Metadata["accepted"])
		return &model.Event{Slug: params.Slug}, nil
	}})
	ledger, err := proofevidencesvc.NewLedger(unavailableObservationStorage{storageErr}, proofobservations.New(), cipher, events)
	require.NoError(t, err)
	target := testCandidateTarget(t)
	repo := &fakeReceiptRepository{target: target}
	base, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{SchemaData: json.RawMessage(`{}`), UpdatedAt: time.Now()}}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{time.Now()})
	require.NoError(t, err)
	compiler := &LiveCompiler{base: base, ledger: ledger, events: events}
	scope := ObservationScope{Organization: target.OrganizationSlug, Project: target.ProjectSlug, Environment: target.EnvironmentSlug}
	settings := ObservationPolicySettings{Enabled: true, TimeoutSeconds: 30, EvidenceTTLSeconds: 300, MaxRequests: 20, MaxRows: 10000, MaxBytes: 10 << 20}
	_, err = compiler.SetObservationPolicy(t.Context(), scope, "reviewer", settings)
	require.ErrorIs(t, err, storageErr)
	_, err = compiler.GetObservationPolicy(t.Context(), scope, "reviewer")
	require.ErrorIs(t, err, storageErr)
	_, err = compiler.GetObservation(t.Context(), scope, "reviewer", "job")
	require.ErrorIs(t, err, storageErr)
	_, err = compiler.CancelObservation(t.Context(), scope, "reviewer", "job")
	require.ErrorIs(t, err, storageErr)
	_, err = compiler.PrepareObservation(t.Context(), scope, "reviewer", target.CandidateSlug, "extract", proofevidence.MetadataPlan{})
	require.ErrorIs(t, err, storageErr)
	_, err = compiler.AuthorizeObservation(t.Context(), scope, "reviewer", "invalid-intent", "invalid-digest")
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	_, err = compiler.CompileObserved(t.Context(), scope, "reviewer", target.CandidateSlug, []slug.Slug{"job"})
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Empty(t, repo.created)

	level := model.ProofEvidenceLevelLiveReadOnly
	repo.stored = &proofreceipts.Receipt{Value: model.ProofReceipt{ID: "receipt", Claims: []model.ProofClaim{{Verdict: model.ProofVerdictPassed, ActualEvidenceLevel: &level, ExpiresAt: new(time.Now().Add(time.Minute)), Evidence: []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindIntrospection, Digest: "digest"}}}}}}
	detail, err := compiler.Get(t.Context(), scope.Organization, scope.Project, scope.Environment, "receipt")
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, detail.Receipt.Claims[0].Verdict)
	require.Equal(t, "proof.live_observation_changed", detail.Receipt.Claims[0].Diagnostics[0].Code)
	require.Equal(t, model.ProofVerdictPassed, repo.stored.Value.Claims[0].Verdict)
	encoded, err := json.Marshal(detail)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-storage-diagnostic")

	static, err := compiler.Compile(t.Context(), scope.Organization, target.CandidateSlug)
	require.NoError(t, err)
	for _, claim := range static.Value.Claims {
		require.NotEqual(t, model.ProofEvidenceLevelLiveReadOnly, *claim.ActualEvidenceLevel)
	}
}

func TestWorkspaceLiveMetadataProducesValidReceiptWithStaticCapabilityClaims(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, role := range []string{"source", "destination"} {
		for _, valid := range []bool{true, false} {
			t.Run(role+"/"+map[bool]string{true: "valid", false: "invalid"}[valid], func(t *testing.T) {
				t.Parallel()
				step := pipelinespec.Step{Slug: "selected"}
				field := "steps[0].query"
				if role == "source" {
					step.Query = &pipelinespec.QueryRef{DataSourceSlug: "workspace-ref", OperationType: "google_workspace"}
				} else {
					step.Sink = &pipelinespec.SinkSpec{DataSourceSlug: "workspace-ref", OperationType: "google_workspace", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}
					field = "steps[0].sink"
				}
				fixture := testutil.ProofEvidenceRequest()
				validation := &pipelinesvc.PipelineValidationResult{Valid: valid}
				if !valid {
					stepSlug := step.Slug.String()
					validation.Diagnostics = []pipelinesvc.PipelineValidationDiagnostic{{Code: "DATA_SOURCE_REF_INCOMPATIBLE", Field: field, StepSlug: &stepSlug}}
				}
				claims := compileClaims(&pipelinespec.Pipeline{Steps: []pipelinespec.Step{step}}, validation,
					[]model.ResolvedPipelineDataSourceRef{{RefSlug: "workspace-ref", DataSourceSlug: "workspace", DataSourceType: model.DataSourceTypeGoogleWorkspace}},
					nil, nil, fixture.Candidate.SpecHash, fixture.Candidate.SpecHash, now)
				_, _, _, _, livePath := selectedStep(step, model.DataSourceTypeGoogleWorkspace)
				level := model.ProofEvidenceLevelLiveReadOnly
				claims = append(claims, model.ProofClaim{
					Identity:    model.ProofClaimIdentity{Family: model.ProofClaimFamilyAccessCapability, PipelineVersionHash: fixture.Candidate.SpecHash, SemanticPath: livePath},
					Description: "selected Workspace resource is reachable", RequiredEvidenceLevel: level, ActualEvidenceLevel: &level,
					Verdict: model.ProofVerdictPassed, ObservedAt: &now, ExpiresAt: new(now.Add(time.Minute)),
					Evidence: []model.ProofEvidenceReference{{Kind: model.ProofEvidenceReferenceKindIntrospection, Digest: fixture.Candidate.SpecHash}},
				})
				receipt, err := model.NewProofReceipt(model.ProofReceipt{
					SchemaVersion: model.ProofReceiptSchemaVersion, ID: "workspace-receipt", Candidate: fixture.Candidate,
					Claims: claims, CreatedAt: now, RetainUntil: now.Add(time.Hour),
				}, secretRedactor(nil))
				require.NoError(t, err)
				var static, live *model.ProofClaim
				for i := range receipt.Claims {
					claim := &receipt.Claims[i]
					if claim.Identity.Family != model.ProofClaimFamilyAccessCapability {
						continue
					}
					if claim.Identity.SemanticPath == "/steps/selected/"+role+"/capability" {
						static = claim
					} else {
						live = claim
					}
				}
				require.NotNil(t, static)
				require.NotNil(t, live)
				require.Equal(t, model.ProofEvidenceLevelStatic, *static.ActualEvidenceLevel)
				require.Equal(t, level, *live.ActualEvidenceLevel)
				require.Equal(t, model.ProofVerdictPassed, live.Verdict)
				if !valid {
					require.Equal(t, model.ProofVerdictFailed, static.Verdict)
					require.Equal(t, model.ProofVerdictFailed, receipt.AggregateVerdict)
				}
			})
		}
	}
}

func TestObservationPreviewBindsReviewedAuthorityAndCapsExecution(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	ledger, err := proofevidencesvc.NewLedger(testutil.FakeTransactionManager{}, proofobservations.New(), cipher, eventrecorder.New(observationAuditStore{}))
	require.NoError(t, err)
	compiler := &LiveCompiler{ledger: ledger}
	fixture := testutil.ProofEvidenceRequest()
	scope := ObservationScope{Organization: fixture.OrganizationSlug, Project: fixture.ProjectSlug, Environment: fixture.EnvironmentSlug}
	for _, observer := range []proofevidence.MetadataObserver{
		hubspot.ContactMetadataObserver{AccountID: 42},
		bqconnector.TableMetadataObserver{ProjectID: "project", DatasetID: "dataset", TableID: "table"},
		bqconnector.TableMetadataObserver{ProjectID: "project", DatasetID: "dataset", TableID: "table", OAuth: true},
		workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"},
		workspaceconnector.FileMetadataObserver{FileID: "file", Operation: "read"},
		workspaceconnector.FileMetadataObserver{FileID: "file", Operation: "update"},
		workspaceconnector.FileMetadataObserver{FileID: "folder", Operation: "create"},
		workspaceconnector.FileMetadataObserver{FileID: "folder", Operation: "list"},
	} {
		target, err := observer.Plan()
		require.NoError(t, err)
		t.Run(target.ConnectorType.String(), func(t *testing.T) {
			for _, scenario := range []string{"Capped", "SmallerPolicy", "Disabled", "InsufficientRequests", "CredentialExpires", "InvalidActor", "InvalidTarget"} {
				t.Run(scenario, func(t *testing.T) {
					settings := ObservationPolicySettings{Enabled: true, TimeoutSeconds: 30, EvidenceTTLSeconds: 300, MaxRequests: 20, MaxRows: 10000, MaxBytes: 10 << 20}
					wantLimits := proofevidence.Limits{Timeout: 30 * time.Second, MaxRows: 1000, MaxBytes: 1 << 20, MaxRequests: int64(len(target.Routes)), MaxConcurrency: 1, RateWindow: time.Hour}
					now := time.Now().UTC()
					source := &datasources.DataSource{Slug: fixture.DataSourceSlug, Type: target.ConnectorType, IsActive: true, Credential: &datasources.CredentialMetadata{Slug: "credential", UpdatedAt: now, Status: "active"}}
					actor := slug.Slug("reviewer")
					plan := target
					switch scenario {
					case "SmallerPolicy":
						settings.TimeoutSeconds, settings.MaxRows, settings.MaxBytes = 5, 10, 2048
						wantLimits.Timeout, wantLimits.MaxRows, wantLimits.MaxBytes = 5*time.Second, 10, 2048
					case "Disabled":
						settings.Enabled = false
					case "InsufficientRequests":
						settings.MaxRequests = int64(len(target.Routes)) - 1
					case "CredentialExpires":
						source.Credential.AccessTokenExpiresAt = new(now.Add(time.Second))
					case "InvalidActor":
						actor = ""
					case "InvalidTarget":
						plan = proofevidence.MetadataPlan{}
					}
					policy := &proofobservations.Policy{Value: observationPolicy(settings, "policy"), WorkerIdentity: proofevidencesvc.ManagedExecutorIdentity}
					preview, err := compiler.sealObservationPreview(policy, scope, actor, fixture.CandidateSlug, fixture.Candidate, fixture.Claim.SemanticPath, source, plan)
					if scenario != "Capped" && scenario != "SmallerPolicy" {
						require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
						require.Nil(t, preview)
						return
					}
					require.NoError(t, err)
					require.Equal(t, wantLimits, preview.Request.Limits)
					require.Equal(t, target.Operation, preview.Request.Operation)
					require.Equal(t, target.Target, preview.Request.Target)
					require.Equal(t, target.Egress, preview.Request.Egress)
					require.Equal(t, fixture.Candidate, preview.Request.Candidate)
					require.Equal(t, fixture.CandidateSlug, preview.Request.CandidateSlug)
					require.Equal(t, fixture.Claim, preview.Request.Claim)
					require.Equal(t, source.Slug, preview.Request.CredentialDataSourceSlug)
					require.Equal(t, "credential/"+now.Format(time.RFC3339Nano), preview.Request.CredentialVersion)
					require.Equal(t, proofevidence.Worker{Identity: proofevidencesvc.ManagedExecutorIdentity, Trust: proofevidence.RunnerTrustManaged}, preview.Request.Worker)
					require.WithinRange(t, preview.ExpiresAt, now.Add(time.Minute), time.Now().Add(time.Minute))
					require.Equal(t, 4*time.Minute, preview.Request.EvidenceRetainUntil.Sub(preview.ExpiresAt))
					require.Equal(t, 24*time.Hour-time.Minute, preview.Request.MetadataRetainUntil.Sub(preview.ExpiresAt))
					intent, err := ledger.OpenIntent(scope, actor, preview.IntentToken, preview.RequestDigest)
					require.NoError(t, err)
					require.Equal(t, preview.Request, intent.Request)
					require.Equal(t, target, intent.Target)
					_, err = ledger.OpenIntent(scope, "other-reviewer", preview.IntentToken, preview.RequestDigest)
					require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				})
			}
		})
	}
}

func TestLiveAppendClauseNeverInfersWritePermissionFromTableMetadata(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, scenario := range []struct {
		kind    bigquery.TableType
		verdict model.ProofVerdict
	}{
		{bigquery.TableTypeTable, model.ProofVerdictUnverified}, {bigquery.TableTypeView, model.ProofVerdictFailed}, {bigquery.TableTypeExternal, model.ProofVerdictFailed}, {bigquery.TableTypeSnapshot, model.ProofVerdictFailed},
	} {
		r := testutil.ProofEvidenceRequest()
		digest, err := r.Digest()
		require.NoError(t, err)
		result := proofevidence.Result{RequestDigest: digest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly, ObservedAt: now, ExpiresAt: now.Add(time.Minute)}
		evidence := &proofevidencesvc.ReceiptObservation{DispatchedAt: now, Request: r, Target: proofObservationTestPlan(t, r), Observation: proofevidencesvc.Observation{Result: result, Snapshot: &proofevidencesvc.Snapshot{Metadata: json.RawMessage(`{"table_type":"` + scenario.kind.String() + `"}`)}}}
		claim, err := observationClaim(evidence, now)
		require.NoError(t, err)
		require.Equal(t, scenario.verdict, claim.Verdict)
		require.Equal(t, digest, claim.Evidence[0].Digest)
		for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
			evidence.Observation.Result.ObservedAt = now.Add(skew)
			claim, err = observationClaim(evidence, now)
			require.NoError(t, err)
			require.Equal(t, now, *claim.ObservedAt, "freshness uses the dispatch lower bound")
		}
		evidence.Observation.Result.ExpiresAt = now
		_, err = observationClaim(evidence, now)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
}

func TestDisabledReaderExpiresLiveClaimsWithoutChangingHistory(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	level := model.ProofEvidenceLevelLiveReadOnly
	for _, scenario := range []struct {
		name       string
		expiry     *time.Time
		superseded bool
	}{
		{name: "Fresh", expiry: new(now.Add(time.Minute))},
		{name: "Expired", expiry: new(now)},
		{name: "MissingExpiry"},
		{name: "Superseded", expiry: new(now.Add(time.Minute)), superseded: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			stored := &proofreceipts.Receipt{Superseded: scenario.superseded, Value: model.ProofReceipt{ID: "receipt", AggregateVerdict: model.ProofVerdictPassed, Claims: []model.ProofClaim{
				{Verdict: model.ProofVerdictPassed, ActualEvidenceLevel: &level, ExpiresAt: scenario.expiry},
				{Verdict: model.ProofVerdictUnverified},
			}}}
			repo := &fakeReceiptRepository{stored: stored}
			compiler, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{now})
			require.NoError(t, err)
			detail, err := compiler.Get(t.Context(), "org", "project", "environment", "receipt")
			require.NoError(t, err)
			require.Equal(t, model.ProofVerdictStale, detail.Receipt.Claims[0].Verdict)
			require.Equal(t, model.ProofVerdictUnverified, detail.Receipt.Claims[1].Verdict)
			if scenario.superseded {
				require.Equal(t, model.ProofVerdictSuperseded, detail.Receipt.AggregateVerdict)
			} else {
				require.Equal(t, model.AggregateProofVerdict(detail.Receipt.Claims), detail.Receipt.AggregateVerdict)
			}
			require.Equal(t, model.ProofVerdictPassed, stored.Value.Claims[0].Verdict)
			require.Empty(t, stored.Value.Claims[0].Diagnostics)
		})
	}
}

func TestObservationPolicyRejectsRelaxedExecutionRestrictions(t *testing.T) {
	t.Parallel()
	settings := ObservationPolicySettings{Enabled: true, TimeoutSeconds: 30, EvidenceTTLSeconds: 300, MaxRequests: 20, MaxRows: 10000, MaxBytes: 10 << 20}
	compiler := &LiveCompiler{}
	for _, scenario := range []string{"Valid", "Disabled", "Retry", "Concurrency", "Cost", "Endpoint", "Assignment", "MissingConnector"} {
		t.Run(scenario, func(t *testing.T) {
			policy := &proofobservations.Policy{Value: observationPolicy(settings, "policy"), WorkerIdentity: proofevidencesvc.ManagedExecutorIdentity}
			switch scenario {
			case "Disabled":
				policy.Value.Enabled = false
			case "Retry":
				policy.Value.Operations[0].Limits.MaxRetries = 1
			case "Concurrency":
				policy.Value.Operations[0].Limits.MaxConcurrency = 2
			case "Cost":
				policy.Value.Operations[0].Limits.MaxCostUnits = 1
			case "Endpoint":
				for i := range policy.Value.Operations {
					if len(policy.Value.Operations[i].ProviderEndpoints) > 0 {
						policy.Value.Operations[i].ProviderEndpoints[0].Host = "other.example"
						break
					}
				}
			case "Assignment":
				policy.WorkerIdentity = "customer-executor"
			case "MissingConnector":
				policy.Value.Operations = policy.Value.Operations[:1]
			}
			view, err := compiler.policyView(policy)
			if scenario == "Valid" || scenario == "Disabled" {
				require.NoError(t, err)
				want := settings
				want.Enabled = scenario == "Valid"
				require.Equal(t, want, view.Settings)
			} else {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			}
		})
	}
}

func TestHubSpotTargetRequiresMetadataOnlyContactOperation(t *testing.T) {
	t.Parallel()
	target, err := (hubspot.ContactMetadataObserver{AccountID: 42}).Plan()
	require.NoError(t, err)
	for _, scenario := range []struct {
		name, config, operation string
		allowed                 bool
	}{
		{"List", `{}`, `{"type":"hubspot","object_type":"contacts","operation":"list"}`, true},
		{"Search", `{}`, `{"object_type":"contacts","operation":"search"}`, true},
		{"Default", `{}`, `{"object_type":"contacts"}`, true},
		{"Write", `{}`, `{"object_type":"contacts","operation":"create"}`, false},
		{"OtherObject", `{}`, `{"object_type":"companies","operation":"list"}`, false},
		{"Private", `{"allow_private_hosts":true}`, `{"object_type":"contacts"}`, false},
		{"CustomHost", `{"base_url":"https://other.example"}`, `{"object_type":"contacts"}`, false},
		{"Malformed", `{`, `{"object_type":"contacts"}`, false},
		{"WrongConnector", `{}`, `{"type":"bigquery","object_type":"contacts"}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := &datasources.DataSource{Type: model.DataSourceTypeHubSpot, Config: json.RawMessage(scenario.config)}
			require.Equal(t, scenario.allowed, targetMatches(source, json.RawMessage(scenario.operation), target))
		})
	}
}

func TestGoogleWorkspaceTargetRequiresSelectedWorksheetAndRange(t *testing.T) {
	t.Parallel()
	target, err := (workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 7, Range: "A1:B2"}).Plan()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, operation string
		allowed         bool
	}{
		{"Read", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"read","worksheet_id":7,"range":"A1:B2"}`, true},
		{"Update", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"update","worksheet_id":7,"range":"A1:B2","columns":["a","b"],"rows":"{{input}}"}`, true},
		{"OtherWorksheet", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"read","worksheet_id":8,"range":"A1:B2"}`, false},
		{"OtherRange", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"read","worksheet_id":7,"range":"A1:C2"}`, false},
		{"Invalid", `{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"read","worksheet_id":7,"range":"A1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &datasources.DataSource{Type: model.DataSourceTypeGoogleWorkspace, Config: json.RawMessage(`{"access_mode":"selected_files"}`)}
			require.Equal(t, tc.allowed, targetMatches(source, json.RawMessage(tc.operation), target))
		})
	}
}

func TestGoogleWorkspaceOperationMatchesClaimRole(t *testing.T) {
	t.Parallel()
	target, err := (workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"}).Plan()
	require.NoError(t, err)
	source := &datasources.DataSource{Type: model.DataSourceTypeGoogleWorkspace, Config: json.RawMessage(`{"access_mode":"selected_files"}`)}
	read := json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"read","worksheet_id":0,"range":"A1:B2"}`)
	update := json.RawMessage(`{"type":"google_workspace","resource":"spreadsheet","spreadsheet_id":"sheet","operation":"update","worksheet_id":0,"range":"A1:B2","columns":["a","b"],"rows":"{{input}}"}`)
	require.True(t, targetMatchesRole(source, read, target, true))
	require.False(t, targetMatchesRole(source, update, target, true))
	require.True(t, targetMatchesRole(source, update, target, false))
	require.False(t, targetMatchesRole(source, read, target, false))
}

func TestFailureEvidenceMustMatchTheSelectedStepAndBinding(t *testing.T) {
	t.Parallel()
	r := testutil.ProofEvidenceRequest()
	r.ConnectorType = model.DataSourceTypeHubSpot
	r.Claim.SemanticPath = "/steps/contact-source/source/live_identity"
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Slug: "contact-source", Query: &pipelinespec.QueryRef{DataSourceSlug: "crm-ref", OperationType: "hubspot"}}}}
	resolutions := []model.ResolvedPipelineDataSourceRef{{RefSlug: "crm-ref", DataSourceSlug: r.DataSourceSlug, DataSourceType: model.DataSourceTypeHubSpot}}
	require.True(t, failedObservationMatchesStep(pipeline, resolutions, r))
	for _, scenario := range []string{"Step", "Connector", "Reference", "DataSource"} {
		t.Run(scenario, func(t *testing.T) {
			request := r
			binding := resolutions[0]
			switch scenario {
			case "Step":
				request.Claim.SemanticPath = "/steps/other/source/live_identity"
			case "Connector":
				request.ConnectorType = model.DataSourceTypeBigQuery
			case "Reference":
				binding.RefSlug = "other-ref"
			case "DataSource":
				binding.DataSourceSlug = "other-source"
			}
			require.False(t, failedObservationMatchesStep(pipeline, []model.ResolvedPipelineDataSourceRef{binding}, request))
		})
	}
}

func TestLiveCompilationRejectsUnboundedOrRepeatedJobsBeforeStorage(t *testing.T) {
	t.Parallel()
	compiler := &LiveCompiler{}
	for _, jobs := range [][]slug.Slug{nil, {"one", "two", "three"}, {"same", "same"}} {
		_, err := compiler.CompileObserved(t.Context(), ObservationScope{}, "actor", "candidate", jobs)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
}

type observationSourceRepository struct {
	datasources.Repository
	find func(slug.Slug, slug.Slug) (*datasources.DataSource, error)
}

func (r observationSourceRepository) FindBySlug(_ context.Context, _ postgres.DBTX, org, source slug.Slug) (*datasources.DataSource, error) {
	return r.find(org, source)
}

type observationOperationRepository struct {
	queries.Repository
	find func(slug.Slug, slug.Slug, slug.Slug) (*queries.Query, error)
}

func (r observationOperationRepository) FindBySlug(_ context.Context, _ postgres.DBTX, org, source, operation slug.Slug) (*queries.Query, []*queries.QueryParameter, error) {
	value, err := r.find(org, source, operation)
	return value, nil, err
}

func TestSelectedTargetRechecksSavedOperationAndTenantBinding(t *testing.T) {
	t.Parallel()
	target, err := (bqconnector.TableMetadataObserver{ProjectID: "project", DatasetID: "dataset", TableID: "table"}).Plan()
	require.NoError(t, err)
	for _, scenario := range []string{"Saved", "Inline", "Mixed", "DeletedOperation", "ChangedTable", "WrongSource", "WrongConnector", "MissingBinding", "UnwrappedStatement", "MissingStatement", "MalformedEnvelope", "MalformedStatement"} {
		t.Run(scenario, func(t *testing.T) {
			pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Slug: "sink", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "warehouse-ref", OperationType: "bigquery", OperationRef: "append"}}}}
			bindings := []model.ResolvedPipelineDataSourceRef{{RefSlug: "warehouse-ref", DataSourceSlug: "warehouse", DataSourceType: model.DataSourceTypeBigQuery}}
			source := &datasources.DataSource{Slug: "warehouse", Type: model.DataSourceTypeBigQuery, Config: json.RawMessage(`{"project_id":"project","default_dataset":"dataset"}`)}
			operation := &queries.Query{DataSourceSlug: source.Slug, Data: json.RawMessage(`{"statement":"{\"operation\":\"insert_rows\",\"table\":\"table\"}","row_limit":1000,"timeout_seconds":30}`)}
			switch scenario {
			case "Inline":
				pipeline.Steps[0].Sink.OperationRef = ""
				pipeline.Steps[0].Sink.OperationInline = map[string]any{"operation": "insert_rows", "table": "table"}
			case "Mixed":
				pipeline.Steps[0].Sink.OperationInline = map[string]any{"operation": "insert_rows", "table": "other"}
			case "DeletedOperation":
				operation.DeletedAt = new(time.Now())
			case "ChangedTable":
				operation.Data = json.RawMessage(`{"statement":"{\"operation\":\"insert_rows\",\"table\":\"other\"}"}`)
			case "UnwrappedStatement":
				operation.Data = json.RawMessage(`{"operation":"insert_rows","table":"table"}`)
			case "MissingStatement":
				operation.Data = json.RawMessage(`{"statement":""}`)
			case "MalformedEnvelope":
				operation.Data = json.RawMessage(`{"statement":{}}`)
			case "MalformedStatement":
				operation.Data = json.RawMessage(`{"statement":"{"}`)
			case "WrongSource":
				operation.DataSourceSlug = "other"
			case "WrongConnector":
				source.Type = model.DataSourceTypeHubSpot
			case "MissingBinding":
				bindings = nil
			}
			compiler := &LiveCompiler{base: &Compiler{txm: testutil.FakeTransactionManager{}}, sources: observationSourceRepository{find: func(org, ds slug.Slug) (*datasources.DataSource, error) {
				require.Equal(t, slug.Slug("tenant"), org)
				require.Equal(t, slug.Slug("warehouse"), ds)
				return source, nil
			}}, operations: observationOperationRepository{find: func(org, ds, query slug.Slug) (*queries.Query, error) {
				require.Equal(t, slug.Slug("tenant"), org)
				require.Equal(t, slug.Slug("warehouse"), ds)
				require.Equal(t, slug.Slug("append"), query)
				require.NotEqual(t, "Inline", scenario, "inline operations must not resolve a saved query")
				return operation, nil
			}}}
			got, err := compiler.selectedSource(t.Context(), model.ProofCandidateIdentity{OrganizationSlug: "tenant"}, pipeline, bindings, "/steps/sink/destination/append_capability", target)
			if scenario == "Saved" || scenario == "Inline" {
				require.NoError(t, err)
				require.Same(t, source, got)
			} else {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				require.Nil(t, got)
			}
		})
	}
}

func TestSelectedWorkspaceSavedOperationMatchesTargetAndRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		statement string
		observer  proofevidence.MetadataObserver
		isSink    bool
	}{
		{"WorksheetRead", `{"type":"google_workspace","resource":"spreadsheet","operation":"read","spreadsheet_id":"sheet","worksheet_id":0,"range":"A1:B2"}`, workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"}, false},
		{"WorksheetUpdate", `{"type":"google_workspace","resource":"spreadsheet","operation":"update","spreadsheet_id":"sheet","worksheet_id":0,"range":"A1:B2","columns":["a","b"],"rows":"{{input}}"}`, workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"}, true},
		{"FileRead", `{"type":"google_workspace","resource":"file","operation":"read","file_id":"file","format":"json"}`, workspaceconnector.FileMetadataObserver{FileID: "file", Operation: "read"}, false},
		{"FolderList", `{"type":"google_workspace","resource":"file","operation":"list","folder_id":"folder"}`, workspaceconnector.FileMetadataObserver{FileID: "folder", Operation: "list"}, false},
		{"FileCreate", `{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"crm.json","format":"json","content":"{{input}}"}`, workspaceconnector.FileMetadataObserver{FileID: "folder", Operation: "create"}, true},
		{"FileUpdate", `{"type":"google_workspace","resource":"file","operation":"update","file_id":"file","format":"json","content":"{{input}}"}`, workspaceconnector.FileMetadataObserver{FileID: "file", Operation: "update"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, err := tc.observer.Plan()
			require.NoError(t, err)
			for _, scenario := range []string{"Saved", "Inline", "WrongTarget", "WrongRole", "MalformedStatement"} {
				t.Run(scenario, func(t *testing.T) {
					source := &datasources.DataSource{Slug: "workspace", Type: model.DataSourceTypeGoogleWorkspace, Config: json.RawMessage(`{"access_mode":"selected_files"}`)}
					data, err := json.Marshal(map[string]any{"statement": tc.statement, "row_limit": 1000, "timeout_seconds": 30})
					require.NoError(t, err)
					operation := &queries.Query{DataSourceSlug: source.Slug, Data: data}
					step := pipelinespec.Step{Slug: "selected"}
					role := tc.isSink
					if scenario == "WrongRole" {
						role = !role
					}
					path := "/steps/selected/source/selected_resource"
					if role {
						step.Sink = &pipelinespec.SinkSpec{DataSourceSlug: "workspace-ref", OperationType: "google_workspace", OperationRef: "saved"}
						path = "/steps/selected/destination/selected_resource"
					} else {
						step.Query = &pipelinespec.QueryRef{DataSourceSlug: "workspace-ref", OperationType: "google_workspace", OperationRef: "saved"}
					}
					selectedTarget := target
					switch scenario {
					case "Inline":
						var inline map[string]any
						require.NoError(t, json.Unmarshal([]byte(tc.statement), &inline))
						if role {
							step.Sink.OperationRef, step.Sink.OperationInline = "", inline
						} else {
							step.Query.OperationRef, step.Query.OperationInline = "", inline
						}
					case "WrongTarget":
						selectedTarget.Resource = json.RawMessage(`{"resource":"file","file_id":"other","operation":"read"}`)
					case "MalformedStatement":
						operation.Data = json.RawMessage(`{"statement":"{}"}`)
					}
					compiler := &LiveCompiler{base: &Compiler{txm: testutil.FakeTransactionManager{}}, sources: observationSourceRepository{find: func(org, ds slug.Slug) (*datasources.DataSource, error) {
						require.Equal(t, slug.Slug("tenant"), org)
						require.Equal(t, source.Slug, ds)
						return source, nil
					}}, operations: observationOperationRepository{find: func(org, ds, query slug.Slug) (*queries.Query, error) {
						require.Equal(t, slug.Slug("tenant"), org)
						require.Equal(t, source.Slug, ds)
						require.Equal(t, slug.Slug("saved"), query)
						require.NotEqual(t, "Inline", scenario)
						return operation, nil
					}}}
					bindings := []model.ResolvedPipelineDataSourceRef{{RefSlug: "workspace-ref", DataSourceSlug: source.Slug, DataSourceType: source.Type}}
					got, err := compiler.selectedSource(t.Context(), model.ProofCandidateIdentity{OrganizationSlug: "tenant"}, &pipelinespec.Pipeline{Steps: []pipelinespec.Step{step}}, bindings, path, selectedTarget)
					if scenario == "Saved" || scenario == "Inline" {
						require.NoError(t, err)
						require.Same(t, source, got)
					} else {
						require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
						require.Nil(t, got)
					}
				})
			}
		})
	}
}

func TestObservationCandidateUsesCurrentIdentityAndRejectsOtherEnvironments(t *testing.T) {
	t.Parallel()
	target := testCandidateTarget(t)
	base, err := New(testutil.FakeTransactionManager{}, &fakeReceiptRepository{target: target}, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{time.Now()})
	require.NoError(t, err)
	compiler := &LiveCompiler{base: base}
	scope := ObservationScope{Organization: target.OrganizationSlug, Project: target.ProjectSlug, Environment: target.EnvironmentSlug}
	identity, pipeline, bindings, err := compiler.observationCandidate(t.Context(), scope, target.CandidateSlug)
	require.NoError(t, err)
	require.Equal(t, target.HeadSHA, identity.HeadSHA)
	require.Equal(t, target.SpecHash, identity.SpecHash)
	require.Equal(t, LiveCompilerVersion, identity.CompilerVersion)
	require.Equal(t, LivePolicyVersion, identity.PolicyVersion)
	require.Len(t, pipeline.Steps, 1)
	require.Len(t, bindings, 1)
	for _, wrong := range []ObservationScope{
		{Organization: scope.Organization, Project: "other", Environment: scope.Environment},
		{Organization: scope.Organization, Project: scope.Project, Environment: "other"},
	} {
		_, _, _, err := compiler.observationCandidate(t.Context(), wrong, target.CandidateSlug)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		_, err = compiler.CompileObserved(t.Context(), wrong, "actor", target.CandidateSlug, []slug.Slug{"job"})
		require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	}
	target.CanonicalSpec = []byte(`{"steps":`)
	_, _, _, err = compiler.observationCandidate(t.Context(), scope, target.CandidateSlug)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

func TestLiveReaderRejectsBrokenEvidenceReferencesWithoutRewritingHistory(t *testing.T) {
	t.Parallel()
	level := model.ProofEvidenceLevelLiveReadOnly
	for _, references := range [][]model.ProofEvidenceReference{nil, {{Kind: model.ProofEvidenceReferenceKindValidation, Digest: "unexpected"}}} {
		stored := &proofreceipts.Receipt{Value: model.ProofReceipt{ID: "receipt", Claims: []model.ProofClaim{{Verdict: model.ProofVerdictPassed, ActualEvidenceLevel: &level, ExpiresAt: new(time.Now().Add(time.Minute)), Evidence: references}}}}
		base, err := New(testutil.FakeTransactionManager{}, &fakeReceiptRepository{stored: stored}, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{time.Now()})
		require.NoError(t, err)
		detail, err := (&LiveCompiler{base: base}).Get(t.Context(), "org", "project", "environment", "receipt")
		require.NoError(t, err)
		require.Equal(t, model.ProofVerdictStale, detail.Receipt.Claims[0].Verdict)
		require.Equal(t, "proof.live_observation_changed", detail.Receipt.Claims[0].Diagnostics[0].Code)
		require.Equal(t, model.ProofVerdictPassed, stored.Value.Claims[0].Verdict)
	}
}

func TestObservationJobProjectionCannotExposePrivateLedgerPayloads(t *testing.T) {
	t.Parallel()
	for _, state := range []proofobservations.State{proofobservations.StatePending, proofobservations.StateRunning, proofobservations.StateFinished} {
		job := &proofobservations.Job{Slug: "job", State: state, RequestDigest: "request-digest", CancellationRequested: true, TargetCiphertext: []byte("private-target"), SnapshotCiphertext: []byte("private-snapshot"), Authorization: proofevidence.Authorization{ID: "private-authorization"}, Result: proofevidence.Result{Outcome: proofevidence.OutcomeCanceled}}
		view, err := observationJob(job, nil)
		require.NoError(t, err)
		require.Equal(t, state.String(), view.State)
		require.True(t, view.CancellationRequested)
		if state == proofobservations.StateFinished {
			require.Equal(t, proofevidence.OutcomeCanceled, *view.Outcome)
		} else {
			require.Nil(t, view.Outcome)
		}
		raw, err := json.Marshal(view)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private-")
	}
	_, err := observationJob(&proofobservations.Job{State: proofobservations.StateFinished}, nil)
	var typed *xerror.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, xerror.KindInternal, typed.Kind())
	_, err = observationJob(nil, proofevidence.ErrAuthorizationDenied)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

func TestTargetAndCredentialChangesCannotReuseAnObservation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	r := testutil.ProofEvidenceRequest()
	r.CredentialVersion = "credential/" + now.Format(time.RFC3339Nano)
	source := &datasources.DataSource{Slug: r.DataSourceSlug, Type: model.DataSourceTypeBigQuery, IsActive: true, Config: json.RawMessage(`{"project_id":"project","default_dataset":"dataset"}`), Credential: &datasources.CredentialMetadata{Slug: "credential", UpdatedAt: now, Status: "active"}}
	target, err := (bqconnector.TableMetadataObserver{ProjectID: "project", DatasetID: "dataset", TableID: "table"}).Plan()
	require.NoError(t, err)
	operation := json.RawMessage(`{"type":"bigquery","operation":"insert_rows","table":"table"}`)
	require.True(t, credentialMatches(source, r, now))
	require.True(t, targetMatches(source, operation, target))
	source.Credential.UpdatedAt = now.Add(time.Second)
	require.False(t, credentialMatches(source, r, now))
	source.Config = json.RawMessage(`{"project_id":"other","default_dataset":"dataset"}`)
	require.False(t, targetMatches(source, operation, target))
	source.Config = json.RawMessage(`{"project_id":"project","default_dataset":"dataset","base_url":"https://other.example"}`)
	require.False(t, targetMatches(source, operation, target))
}

func TestFailedObservationCannotClaimLiveEvidence(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, connector := range []model.DataSourceType{model.DataSourceTypeHubSpot, model.DataSourceTypeBigQuery, model.DataSourceTypeGoogleWorkspace} {
		for _, outcome := range proofevidence.OutcomeValues() {
			if outcome == proofevidence.OutcomeUnknown || outcome == proofevidence.OutcomeComplete {
				continue
			}
			t.Run(connector.String()+"/"+outcome.String(), func(t *testing.T) {
				r := testutil.ProofEvidenceRequest()
				r.ConnectorType = connector
				switch connector {
				case model.DataSourceTypeHubSpot:
					r.Operation = "hubspot.contact_metadata"
				case model.DataSourceTypeGoogleWorkspace:
					r.Operation = "google_workspace.worksheet_metadata"
				}
				digest, err := r.Digest()
				require.NoError(t, err)
				evidence := &proofevidencesvc.ReceiptObservation{Request: r, Observation: proofevidencesvc.Observation{Result: proofevidence.Result{RequestDigest: digest, Outcome: outcome}}}
				claim, err := observationClaim(evidence, now)
				require.NoError(t, err)
				expected := model.ProofVerdictInconclusive
				if outcome == proofevidence.OutcomeUnsupported {
					expected = model.ProofVerdictUnverified
				}
				require.Equal(t, expected, claim.Verdict)
				require.Nil(t, claim.ActualEvidenceLevel)
				require.Nil(t, claim.ObservedAt)
				require.Nil(t, claim.ExpiresAt)
				require.Equal(t, "proof.observation."+outcome.String(), claim.Diagnostics[0].Code)
				require.Equal(t, digest, claim.Evidence[0].Digest)
				_, err = model.NewProofReceipt(model.ProofReceipt{SchemaVersion: model.ProofReceiptSchemaVersion, ID: "failure-receipt", Candidate: r.Candidate, Claims: []model.ProofClaim{claim}, CreatedAt: now, RetainUntil: now.Add(time.Hour)}, func(s string) string { return s })
				require.NoError(t, err)
				evidence.Observation.Snapshot = &proofevidencesvc.Snapshot{}
				_, err = observationClaim(evidence, now)
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			})
		}
	}
	r := testutil.ProofEvidenceRequest()
	digest, err := r.Digest()
	require.NoError(t, err)
	for _, outcome := range []proofevidence.Outcome{proofevidence.OutcomeUnknown, proofevidence.Outcome(999), proofevidence.OutcomeComplete} {
		_, err = observationClaim(&proofevidencesvc.ReceiptObservation{Request: r, Target: proofObservationTestPlan(t, r), Observation: proofevidencesvc.Observation{Result: proofevidence.Result{RequestDigest: digest, Outcome: outcome}}}, now)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
}

func TestWorkspaceObservationRequiresMatchingWorksheetAndBounds(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	target, err := (workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"}).Plan()
	require.NoError(t, err)
	r := testutil.ProofEvidenceRequest()
	r.ConnectorType, r.Operation = target.ConnectorType, target.Operation
	digest, err := r.Digest()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, metadata string
		valid          bool
	}{
		{"matching zero worksheet", `{"worksheet_id":0,"row_count":2,"column_count":2}`, true},
		{"larger grid", `{"worksheet_id":0,"row_count":100,"column_count":20}`, true},
		{"other worksheet", `{"worksheet_id":1,"row_count":2,"column_count":2}`, false},
		{"too few rows", `{"worksheet_id":0,"row_count":1,"column_count":2}`, false},
		{"too few columns", `{"worksheet_id":0,"row_count":2,"column_count":1}`, false},
		{"missing grid", `{}`, false},
		{"malformed", `{"worksheet_id":`, false},
		{"BigQuery metadata", `{"table_type":"table"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := &proofevidencesvc.ReceiptObservation{
				DispatchedAt: now, Request: r, Target: target,
				Observation: proofevidencesvc.Observation{
					Result:   proofevidence.Result{RequestDigest: digest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: model.ProofEvidenceLevelLiveReadOnly, ExpiresAt: now.Add(time.Minute)},
					Snapshot: &proofevidencesvc.Snapshot{Metadata: json.RawMessage(tc.metadata)},
				},
			}
			claim, err := observationClaim(evidence, now)
			if !tc.valid {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				return
			}
			require.NoError(t, err)
			require.Equal(t, model.ProofVerdictPassed, claim.Verdict)
			require.Equal(t, model.ProofEvidenceLevelLiveReadOnly, *claim.ActualEvidenceLevel)
			require.Empty(t, claim.Diagnostics)
			require.Equal(t, digest, claim.Evidence[0].Digest)
		})
	}
}

func TestOldObservationPolicyRequiresExplicitReplacement(t *testing.T) {
	settings := ObservationPolicySettings{Enabled: true, TimeoutSeconds: 5, EvidenceTTLSeconds: 60, MaxRequests: 2, MaxRows: 10, MaxBytes: 1024}
	policy := observationPolicy(settings, "reviewed-policy")
	policy.Operations = policy.Operations[:2]
	stored := &proofobservations.Policy{Value: policy, WorkerIdentity: proofevidencesvc.ManagedExecutorIdentity}
	view, err := (&LiveCompiler{}).policyView(stored)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Nil(t, view)
	require.Len(t, stored.Value.Operations, 2)
}

func TestFailedFileObservationDescribesFileMetadata(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	r := testutil.ProofEvidenceRequest()
	r.ConnectorType = model.DataSourceTypeGoogleWorkspace
	r.Operation = "google_workspace.file_metadata"
	digest, err := r.Digest()
	require.NoError(t, err)
	claim, err := observationClaim(&proofevidencesvc.ReceiptObservation{Request: r, Observation: proofevidencesvc.Observation{Result: proofevidence.Result{RequestDigest: digest, Outcome: proofevidence.OutcomeDenied}}}, now)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictInconclusive, claim.Verdict)
	require.Equal(t, "selected resource metadata is reachable with the bound data-source credential", claim.Description)
}

func proofObservationTestPlan(t *testing.T, request proofevidence.Request) proofevidence.MetadataPlan {
	t.Helper()
	var observer proofevidence.MetadataObserver
	switch request.ConnectorType {
	case model.DataSourceTypeHubSpot:
		observer = hubspot.ContactMetadataObserver{AccountID: 1}
	case model.DataSourceTypeBigQuery:
		observer = bqconnector.TableMetadataObserver{ProjectID: "fixture", DatasetID: "dataset", TableID: "table"}
	case model.DataSourceTypeGoogleWorkspace:
		if request.Operation == "google_workspace.file_metadata" {
			observer = workspaceconnector.FileMetadataObserver{FileID: "file", Operation: "read"}
		} else {
			observer = workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "sheet", WorksheetID: 0, Range: "A1:B2"}
		}
	}
	plan, err := observer.Plan()
	require.NoError(t, err)
	return plan
}

func targetMatches(source *datasources.DataSource, operation json.RawMessage, target proofevidence.MetadataPlan) bool {
	return targetMatchesRole(source, operation, target, true) || targetMatchesRole(source, operation, target, false)
}
