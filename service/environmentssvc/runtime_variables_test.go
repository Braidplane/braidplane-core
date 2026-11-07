package environmentssvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
)

func TestService_UpsertRuntimeVariableRejectsSecretNames(t *testing.T) {
	t.Parallel()

	svc := newTestService(
		&fakeEnvironmentsRepo{},
		eventtest.NewProducer(nil),
	)

	for _, key := range []string{"api_token", "accessKey", "privateKey"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			got, err := svc.UpsertRuntimeVariable(
				context.Background(),
				must.Slug("acme"),
				must.Slug("prod"),
				model.UpsertRuntimeVariable{
					Key:       key,
					ValueType: model.RuntimeVariableValueTypeText,
					Value:     "secret",
				},
				model.EventActor{},
			)

			require.Nil(t, got)
			require.True(t, xerror.Is(err, ErrInvalidRuntimeVariable))
			require.ErrorContains(t, err, "runtime variables are non-secret")
		})
	}
}

func TestService_UpsertRuntimeVariableRejectsMissingValue(t *testing.T) {
	t.Parallel()

	svc := newTestService(
		&fakeEnvironmentsRepo{},
		eventtest.NewProducer(nil),
	)

	got, err := svc.UpsertRuntimeVariable(
		context.Background(),
		must.Slug("acme"),
		must.Slug("prod"),
		model.UpsertRuntimeVariable{
			Key:       "s3_prefix",
			ValueType: model.RuntimeVariableValueTypeText,
		},
		model.EventActor{},
	)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, xerror.ValidationFailed()))
	require.ErrorContains(t, err, "Value")
}

func TestService_UpsertRuntimeVariableValidatesTypedValues(t *testing.T) {
	t.Parallel()

	svc := newTestService(
		&fakeEnvironmentsRepo{},
		eventtest.NewProducer(nil),
	)

	got, err := svc.UpsertRuntimeVariable(
		context.Background(),
		must.Slug("acme"),
		must.Slug("prod"),
		model.UpsertRuntimeVariable{
			Key:       "batch_size",
			ValueType: model.RuntimeVariableValueTypeInteger,
			Value:     "many",
		},
		model.EventActor{},
	)

	require.Nil(t, got)
	require.True(t, xerror.Is(err, ErrInvalidRuntimeVariable))
	require.ErrorContains(t, err, "base-10 integer")
}

func TestService_UpsertRuntimeVariablePersistsNonSecretVariable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	var captured model.UpsertRuntimeVariable
	events := &fakeEnvironmentEventRecorder{}
	svc := newTestService(
		&fakeEnvironmentsRepo{
			upsertRuntimeVariableFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_, environmentSlug slug.Slug,
				params model.UpsertRuntimeVariable,
			) (*model.RuntimeVariable, error) {
				captured = params
				return &model.RuntimeVariable{
					EnvironmentSlug: environmentSlug,
					Key:             params.Key,
					ValueType:       params.ValueType,
					Value:           params.Value,
					CreatedAt:       now,
					UpdatedAt:       now,
				}, nil
			},
		},
		eventtest.NewProducer(events),
	)

	got, err := svc.UpsertRuntimeVariable(
		context.Background(),
		must.Slug("acme"),
		must.Slug("prod"),
		model.UpsertRuntimeVariable{
			Key:       "s3_prefix",
			ValueType: model.RuntimeVariableValueTypeText,
			Value:     "prod/dir",
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.NoError(t, err)
	require.Equal(t, "s3_prefix", captured.Key)
	require.Equal(t, "prod/dir", got.Value)
	require.Len(t, events.calls, 1)
}

func TestService_UpsertRuntimeVariableReturnsRequiredAuditFailureAfterMutation(t *testing.T) {
	t.Parallel()

	auditErr := xerror.Unexpected().Detail("audit write failed")
	mutationCalled := false
	svc := newTestService(
		&fakeEnvironmentsRepo{
			findBySlugFn: func(
				context.Context,
				postgres.DBTX,
				slug.Slug,
				slug.Slug,
			) (*environments.Environment, error) {
				return &environments.Environment{Slug: must.Slug("prod")}, nil
			},
			upsertRuntimeVariableFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_, environmentSlug slug.Slug,
				params model.UpsertRuntimeVariable,
			) (*model.RuntimeVariable, error) {
				mutationCalled = true
				return &model.RuntimeVariable{
					EnvironmentSlug: environmentSlug,
					Key:             params.Key,
					ValueType:       params.ValueType,
					Value:           params.Value,
				}, nil
			},
		},
		eventtest.NewProducer(&fakeEnvironmentEventRecorder{err: auditErr}),
	)

	got, err := svc.UpsertRuntimeVariable(
		context.Background(),
		must.Slug("acme"),
		must.Slug("prod"),
		model.UpsertRuntimeVariable{
			Key:       "s3_prefix",
			ValueType: model.RuntimeVariableValueTypeText,
			Value:     "prod/dir",
		},
		model.UserEventActor(must.Slug("alice")),
	)

	require.Nil(t, got)
	require.True(t, mutationCalled)
	require.ErrorIs(t, err, auditErr)
	require.ErrorContains(t, err, "enqueue required environment audit event")
}

func TestService_ListRuntimeVariablesReturnsNotFoundForMissingEnvironment(t *testing.T) {
	t.Parallel()

	svc := newTestService(
		&fakeEnvironmentsRepo{
			listRuntimeVariablesFn: func(
				_ context.Context,
				_ postgres.DBTX,
				_, _ slug.Slug,
				_ model.ListRequest,
			) ([]*model.RuntimeVariable, error) {
				return nil, ErrEnvironmentNotFound
			},
		},
		eventtest.NewProducer(nil),
	)

	got, err := svc.ListRuntimeVariables(
		context.Background(),
		must.Slug("acme"),
		must.Slug("missing"),
		model.ListRequest{},
	)

	require.Empty(t, got.Items)
	require.True(t, xerror.Is(err, ErrEnvironmentNotFound))
}
