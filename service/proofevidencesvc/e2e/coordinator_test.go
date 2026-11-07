package e2e

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/stretchr/testify/require"
)

type executionCredentials struct{ revoked atomic.Bool }

func (c *executionCredentials) ResolveProofCredential(_ context.Context, grant proofevidence.Grant) (credentialssvc.RuntimeCredential, error) {
	return credentialssvc.RuntimeCredential{Version: grant.Request().CredentialVersion, PlaintextJSON: []byte(`{"fixture":true}`)}, nil
}

func (c *executionCredentials) CheckProofCredential(context.Context, proofevidence.Grant) error {
	if c.revoked.Load() {
		return proofevidence.ErrAuthorizationDenied
	}
	return nil
}

type proofExecutor func(context.Context, proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error)

func (f proofExecutor) ObserveProof(ctx context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
	return f(ctx, request)
}

func completeObservation(t *testing.T, request proofevidencesvc.ExecutionRequest) proofevidencesvc.Observation {
	t.Helper()
	_, err := request.Grant()
	require.NoError(t, err)
	snapshot := &proofevidencesvc.Snapshot{
		TargetFingerprint: request.Request.TargetFingerprint,
		Metadata:          json.RawMessage(`{"table_type":"VIEW","fields":[{"name":"private_field","type":"STRING"}]}`),
	}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	return proofevidencesvc.Observation{Snapshot: snapshot, Result: proofevidence.Result{
		RequestDigest: request.Authorization.RequestDigest, Outcome: proofevidence.OutcomeComplete,
		EvidenceLevel: request.Request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: time.Now().UTC(),
		ExpiresAt: request.Request.EvidenceRetainUntil,
		Usage:     proofevidence.Usage{Requests: 2, MaxConcurrency: 1, Rows: 1, Bytes: int64(len(raw))},
	}}
}

func TestCoordinatorDispatchesReservationOnceAndPersistsProtectedEvidence(t *testing.T) {
	f := newFixture(t)
	job, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	calls := 0
	coordinator, err := proofevidencesvc.NewCoordinator(f.ledger, &executionCredentials{}, proofExecutor(func(ctx context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
		calls++
		require.Equal(t, job.Deadline, request.Deadline)
		require.NoError(t, ctx.Err())
		return completeObservation(t, request), nil
	}))
	require.NoError(t, err)
	result, err := coordinator.Run(t.Context(), f.request.OrganizationSlug, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofevidence.OutcomeComplete, result.Outcome)
	stored, err := f.ledger.ReadSnapshot(t.Context(), f.request.OrganizationSlug, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, result.EvidenceDigest, stored.Result.EvidenceDigest)
	require.Contains(t, string(stored.Snapshot.Metadata), "VIEW")
	_, err = coordinator.Run(t.Context(), f.request.OrganizationSlug, f.request.JobID)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Equal(t, 1, calls)
}

func TestCoordinatorRevocationCannotProduceEvidence(t *testing.T) {
	for _, scenario := range []string{"credential-before-dispatch", "credential-during-dispatch", "credential-at-completion", "policy", "cancel", "inactive-source", "wrong-snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
			require.NoError(t, err)
			credentials := &executionCredentials{}
			if scenario == "credential-before-dispatch" {
				credentials.revoked.Store(true)
			}
			calls := 0
			coordinator, err := proofevidencesvc.NewCoordinator(f.ledger, credentials, proofExecutor(func(ctx context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
				calls++
				observation := completeObservation(t, request)
				switch scenario {
				case "credential-at-completion":
					credentials.revoked.Store(true)
				case "credential-during-dispatch":
					credentials.revoked.Store(true)
					select {
					case <-ctx.Done():
					case <-time.After(3 * time.Second):
						t.Fatal("credential rotation did not cancel delivery")
					}
				case "policy":
					policy := f.policy
					policy.Enabled, policy.Version = false, "disabled-v2"
					require.NoError(t, f.ledger.SetPolicy(ctx, f.scope, policy, f.request.Worker.Identity))
				case "cancel":
					require.NoError(t, f.ledger.Cancel(ctx, f.request.OrganizationSlug, f.request.JobID))
				case "inactive-source":
					_, err := f.pool.Exec(ctx, `UPDATE data_sources SET is_active=false WHERE slug=$1 AND organization_id=(SELECT id FROM organizations WHERE slug=$2)`, f.request.DataSourceSlug, f.request.OrganizationSlug)
					require.NoError(t, err)
				case "wrong-snapshot":
					observation.Snapshot.TargetFingerprint = "substituted"
				}
				return observation, nil
			}))
			require.NoError(t, err)
			result, runErr := coordinator.Run(t.Context(), f.request.OrganizationSlug, f.request.JobID)
			switch scenario {
			case "policy", "cancel", "wrong-snapshot":
				require.ErrorIs(t, runErr, proofevidence.ErrAuthorizationDenied)
			case "credential-during-dispatch":
				require.NoError(t, runErr)
				require.Equal(t, proofevidence.OutcomeCanceled, result.Outcome)
			default:
				require.NoError(t, runErr)
				require.Equal(t, proofevidence.OutcomeDenied, result.Outcome)
			}
			_, err = f.ledger.ReadSnapshot(t.Context(), f.request.OrganizationSlug, f.request.JobID)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			if scenario == "credential-before-dispatch" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestCoordinatorProjectDeletionStopsProof(t *testing.T) {
	for _, state := range []string{"pending", "running"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
			require.NoError(t, err)
			txm := postgres.NewTransactionManager(f.pool)
			deletion := projectssvc.New(txm, projects.NewRepository(), nil, nil, nil, nil, nil, nil,
				eventtest.NewProducer(eventssvc.New(txm, events.New(), testutil.FakeClock{NowFn: time.Now})))
			started := make(chan struct{})
			coordinator, err := proofevidencesvc.NewCoordinator(f.ledger, &executionCredentials{}, proofExecutor(func(ctx context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
				close(started)
				<-ctx.Done()
				// An executor can still return a result after cancellation.
				return completeObservation(t, request), nil
			}))
			require.NoError(t, err)
			finished := make(chan error, 1)
			if state == "running" {
				go func() {
					_, err := coordinator.Run(t.Context(), f.scope.Organization, f.request.JobID)
					finished <- err
				}()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("Proof did not start")
				}
			}
			bounded, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			require.NoError(t, deletion.DeleteProject(bounded, f.scope.Organization, f.scope.Project, model.DeleteProjectOptions{}, model.SystemEventActor()))
			if state == "pending" {
				_, err = coordinator.Run(t.Context(), f.scope.Organization, f.request.JobID)
				require.Error(t, err)
				select {
				case <-started:
					t.Fatal("deleted project dispatched Proof")
				default:
				}
			} else {
				select {
				case err := <-finished:
					require.Error(t, err, "late result cannot be recorded")
				case <-time.After(3 * time.Second):
					t.Fatal("project deletion did not cancel Proof")
				}
			}
			_, err = f.ledger.ReadSnapshot(t.Context(), f.scope.Organization, f.request.JobID)
			require.Error(t, err)
		})
	}
}
