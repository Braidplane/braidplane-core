package orgsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

func TestUpdateRetentionPolicyPreservesOtherOrganizationSettings(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policy := model.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   2,
		ReplayCheckpointRetentionDays: 8,
		RunHistoryRetentionDays:       20,
		EventLogRetentionDays:         25,
	}
	orgs := &testutil.FakeOrgsRepo{
		FindBySlugForUpdateFn: func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*organizations.Organization, error) {
			return &organizations.Organization{Slug: orgSlug, Settings: []byte(`{"billing":{"currency":"EUR"}}`)}, nil
		},
		UpdateSettingsFn: func(_ context.Context, _ postgres.DBTX, params *organizations.UpdateSettingsParams) error {
			require.Equal(t, orgSlug, params.Slug)
			var settings map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(params.Settings, &settings))
			require.JSONEq(t, `{"currency":"EUR"}`, string(settings["billing"]))
			var stored model.OrganizationRetentionPolicy
			require.NoError(t, json.Unmarshal(settings[retentionSettingsKey], &stored))
			require.Equal(t, policy, stored)
			return nil
		},
	}

	events := &fakeOrgEventRecorder{}
	svc := New(
		testutil.FakeTransactionManager{},
		orgs,
		&fakeDefaultEnvironmentsService{},
		nil,
		nil,
		eventtest.NewProducer(events),
		&fakes.AdmissionService{},
	)

	updated, err := svc.UpdateRetentionPolicy(context.Background(), orgSlug, model.UserEventActor(must.Slug("alice")), policy)
	require.NoError(t, err)
	require.Equal(t, policy, updated)
	require.Len(t, events.calls, 1)
	require.Equal(t, model.EventClassSecurityAudit, events.calls[0].EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, events.calls[0].SchemaVersion)
	require.Equal(t, policy.EventLogRetentionDays, events.calls[0].Metadata["event_log_retention_days"])
}

func TestUpdateRetentionPolicyFailsClosedWhenAuditEventCannotBeRecorded(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	policy := model.DefaultOrganizationRetentionPolicy()
	orgs := &testutil.FakeOrgsRepo{
		FindBySlugForUpdateFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
			return &organizations.Organization{Slug: orgSlug, Settings: []byte(`{}`)}, nil
		},
		UpdateSettingsFn: func(context.Context, postgres.DBTX, *organizations.UpdateSettingsParams) error {
			return nil
		},
	}
	events := &fakeOrgEventRecorder{err: xerror.Unexpected().Detail("event store down")}
	svc := New(
		testutil.FakeTransactionManager{},
		orgs,
		&fakeDefaultEnvironmentsService{},
		nil,
		nil,
		eventtest.NewProducer(events),
		&fakes.AdmissionService{},
	)

	_, err := svc.UpdateRetentionPolicy(
		context.Background(),
		orgSlug,
		model.UserEventActor(must.Slug("alice")),
		policy,
	)

	require.ErrorContains(t, err, "enqueue required organization audit event")
	require.ErrorContains(t, err, "event store down")
	require.Len(t, events.calls, 1)
}

func TestUpdateRetentionPolicyReturnsFailuresWithoutPersisting(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	validPolicy := model.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   2,
		ReplayCheckpointRetentionDays: 8,
		RunHistoryRetentionDays:       20,
		EventLogRetentionDays:         25,
	}
	findErr := xerror.Unexpected().Detail("find failed")
	updateErr := xerror.Unexpected().Detail("update failed")

	tests := []struct {
		name            string
		policy          model.OrganizationRetentionPolicy
		orgs            *testutil.FakeOrgsRepo
		wantErr         error
		wantErrContains string
	}{
		{
			name:            "InvalidPolicy",
			policy:          model.OrganizationRetentionPolicy{},
			orgs:            &testutil.FakeOrgsRepo{},
			wantErrContains: "must be between 1 and 30",
		},
		{
			name:   "FindBySlugForUpdate",
			policy: validPolicy,
			orgs: &testutil.FakeOrgsRepo{
				FindBySlugForUpdateFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
					return nil, findErr
				},
			},
			wantErr: findErr,
		},
		{
			name:   "UpdateSettings",
			policy: validPolicy,
			orgs: &testutil.FakeOrgsRepo{
				FindBySlugForUpdateFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
					return &organizations.Organization{Slug: orgSlug, Settings: []byte(`{}`)}, nil
				},
				UpdateSettingsFn: func(context.Context, postgres.DBTX, *organizations.UpdateSettingsParams) error {
					return updateErr
				},
			},
			wantErr: updateErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			persisted := false
			originalUpdate := tt.orgs.UpdateSettingsFn
			tt.orgs.UpdateSettingsFn = func(
				ctx context.Context,
				tx postgres.DBTX,
				params *organizations.UpdateSettingsParams,
			) error {
				persisted = true
				if originalUpdate != nil {
					return originalUpdate(ctx, tx, params)
				}
				return nil
			}
			svc := New(
				testutil.FakeTransactionManager{},
				tt.orgs,
				&fakeDefaultEnvironmentsService{},
				nil,
				nil,
				eventtest.NewProducer(nil),
				&fakes.AdmissionService{},
			)

			_, err := svc.UpdateRetentionPolicy(
				context.Background(),
				orgSlug,
				model.UserEventActor(must.Slug("alice")),
				tt.policy,
			)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.ErrorContains(t, err, tt.wantErrContains)
			}
			if tt.name == "UpdateSettings" {
				require.True(t, persisted)
			} else {
				require.False(t, persisted)
			}
		})
	}
}

func TestGetRetentionPolicy(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme")
	storedPolicy := model.OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   2,
		ReplayCheckpointRetentionDays: 8,
		RunHistoryRetentionDays:       20,
		EventLogRetentionDays:         25,
	}
	findErr := xerror.Unexpected().Detail("find failed")
	settings, err := json.Marshal(map[string]any{retentionSettingsKey: storedPolicy})
	require.NoError(t, err)

	tests := []struct {
		name            string
		orgs            *testutil.FakeOrgsRepo
		want            model.OrganizationRetentionPolicy
		wantErr         error
		wantErrContains string
	}{
		{
			name: "StoredPolicy",
			orgs: &testutil.FakeOrgsRepo{
				FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
					return &organizations.Organization{Slug: orgSlug, Settings: settings}, nil
				},
			},
			want: storedPolicy,
		},
		{
			name: "FindBySlug",
			orgs: &testutil.FakeOrgsRepo{
				FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
					return nil, findErr
				},
			},
			wantErr: findErr,
		},
		{
			name: "InvalidSettings",
			orgs: &testutil.FakeOrgsRepo{
				FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
					return &organizations.Organization{Slug: orgSlug, Settings: []byte(`{`)}, nil
				},
			},
			wantErrContains: "organization settings are invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := New(
				testutil.FakeTransactionManager{},
				tt.orgs,
				&fakeDefaultEnvironmentsService{},
				nil,
				nil,
				eventtest.NewProducer(nil),
				&fakes.AdmissionService{},
			)
			got, err := svc.GetRetentionPolicy(context.Background(), orgSlug)
			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantErrContains != "":
				require.ErrorContains(t, err, tt.wantErrContains)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestSettingsWithRetentionPolicyAcceptsNullSettings(t *testing.T) {
	t.Parallel()

	policy := model.DefaultOrganizationRetentionPolicy()
	updated, err := settingsWithRetentionPolicy([]byte(`null`), policy)
	require.NoError(t, err)

	var settings map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(updated, &settings))
	var stored model.OrganizationRetentionPolicy
	require.NoError(t, json.Unmarshal(settings[retentionSettingsKey], &stored))
	require.Equal(t, policy, stored)
}
