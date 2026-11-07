package proofreceiptsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/stretchr/testify/require"
)

func TestObservationAuditOmitsMalformedStoredDigests(t *testing.T) {
	t.Parallel()
	valid := "sha256:" + strings.Repeat("ab", 32)
	for _, digest := range []string{valid, "private-provider-diagnostic", strings.ToUpper(valid), "sha256:abcd", strings.Repeat("ab", 32)} {
		record, err := (observationAudit{job: &proofobservations.Job{Slug: "job", RequestDigest: digest}}).EventRecord()
		require.NoError(t, err)
		if digest == valid {
			require.Equal(t, valid, record.Metadata["request_digest"])
		} else {
			require.NotContains(t, record.Metadata, "request_digest")
		}
	}
}

type observationAuditStore struct {
	eventssvc.Service
	create func(context.Context, eventssvc.CreateEventParams) (*model.Event, error)
}

func (s observationAuditStore) CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	return s.create(ctx, params)
}

func TestRejectedObservationAuditSurvivesCallerCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	scope := ObservationScope{Organization: "org", Project: "project", Environment: "environment"}
	var captured eventssvc.CreateEventParams
	store := observationAuditStore{create: func(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
		require.NoError(t, ctx.Err())
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
		captured = params
		return &model.Event{Slug: params.Slug}, nil
	}}
	compiler := &LiveCompiler{base: &Compiler{txm: testutil.FakeTransactionManager{}}, events: eventrecorder.New(store)}
	require.ErrorIs(t, compiler.auditRejectedControl(ctx, scope, "reviewer", model.EventActionAccept, proofevidence.ErrAuthorizationDenied), proofevidence.ErrAuthorizationDenied)
	require.Equal(t, scope.Organization, captured.OrgSlug)
	require.Equal(t, model.UserEventActor("reviewer").Slug, captured.ActorSlug)
	require.Equal(t, model.EventClassSecurityAudit, captured.EventClass)
	require.Equal(t, model.EventActionAccept, captured.Action)
	require.Equal(t, false, captured.Metadata["accepted"])
	require.NotContains(t, captured.Metadata, "request_digest")

	require.NoError(t, AuditObservationAccessRejected(ctx, store, scope, model.UserEventActor("reviewer"), model.Execute, model.ResourcePipelines))
	require.Equal(t, model.EventActionDecline, captured.Action)
	require.Equal(t, "execute", captured.Metadata["requested_action"])
	require.Equal(t, model.ResourcePipelines.String(), captured.Metadata["resource"])
}

func TestObservationAuditOutageWithholdsProtectedReadsAndHidesDiagnostics(t *testing.T) {
	t.Parallel()
	scope := ObservationScope{Organization: "org", Project: "project", Environment: "environment"}
	store := observationAuditStore{create: func(context.Context, eventssvc.CreateEventParams) (*model.Event, error) {
		return nil, xerror.Unexpected().Detail("private-storage-diagnostic")
	}}
	compiler := &LiveCompiler{base: &Compiler{txm: testutil.FakeTransactionManager{}}, events: eventrecorder.New(store)}
	for _, err := range []error{
		AuditReceiptRead(t.Context(), store, scope, model.UserEventActor("reviewer"), "receipt"),
		AuditReceiptRead(t.Context(), nil, scope, model.UserEventActor("reviewer"), "receipt"),
		AuditObservationAccessRejected(t.Context(), store, scope, model.UserEventActor("reviewer"), model.Execute, model.ResourcePipelines),
		compiler.auditRejectedControl(t.Context(), scope, "reviewer", model.EventActionAccept, proofevidence.ErrAuthorizationDenied),
	} {
		require.ErrorIs(t, err, ErrObservationAuditUnavailable)
		require.NotContains(t, err.Error(), "private-storage-diagnostic")
	}
}
