package validate

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/structvalidate"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestValidateRoleTypeTag(t *testing.T) {
	t.Parallel()

	type roleInput struct {
		Role model.Role `validate:"role"`
	}

	tests := []struct {
		name            string
		input           roleInput
		wantErrContains string
	}{
		{
			name:            "AcceptsUserRole",
			input:           roleInput{Role: model.RoleUser},
			wantErrContains: "",
		},
		{
			name:            "AcceptsSuperAdminRole",
			input:           roleInput{Role: model.RoleSuperAdmin},
			wantErrContains: "",
		},
		{
			name:            "RejectsUnknownRole",
			input:           roleInput{Role: model.Role(99)},
			wantErrContains: "role",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateDataSourceTypeTag(t *testing.T) {
	t.Parallel()

	type sourceInput struct {
		Type model.DataSourceType `validate:"datasourcetype"`
	}

	tests := []struct {
		name            string
		input           sourceInput
		wantErrContains string
	}{
		{
			name:            "AcceptsPostgresType",
			input:           sourceInput{Type: model.DataSourceTypePostgres},
			wantErrContains: "",
		},
		{
			name:            "AcceptsMySQLType",
			input:           sourceInput{Type: model.DataSourceTypeMySQL},
			wantErrContains: "",
		},
		{
			name:            "AcceptsHTTPType",
			input:           sourceInput{Type: model.DataSourceTypeHTTP},
			wantErrContains: "",
		},
		{
			name:            "AcceptsMongoDBType",
			input:           sourceInput{Type: model.DataSourceTypeMongoDB},
			wantErrContains: "",
		},
		{
			name:            "AcceptsGitHubType",
			input:           sourceInput{Type: model.DataSourceTypeGithub},
			wantErrContains: "",
		},
		{
			name:            "AcceptsGitLabType",
			input:           sourceInput{Type: model.DataSourceTypeGitlab},
			wantErrContains: "",
		},
		{
			name:            "RejectsUnknownDataSourceType",
			input:           sourceInput{Type: model.DataSourceType(99)},
			wantErrContains: "datasourcetype",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateSlugTag(t *testing.T) {
	t.Parallel()

	type slugInput struct {
		Slug string `validate:"slug"`
	}

	tests := []struct {
		name            string
		input           slugInput
		wantErrContains string
	}{
		{
			name:  "AcceptsValidSlug",
			input: slugInput{Slug: "acme-users-sync"},
		},
		{
			name:            "RejectsUppercaseSlug",
			input:           slugInput{Slug: "ACME"},
			wantErrContains: "slug",
		},
		{
			name:            "RejectsSlugWithSpaces",
			input:           slugInput{Slug: "bad slug"},
			wantErrContains: "slug",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateWishlistCommunicationRequestVersionTag(t *testing.T) {
	t.Parallel()

	type wishlistInput struct {
		Version string `validate:"required,wishlistcommunicationrequestversion"`
	}

	v := New()
	require.NoError(t, v.Struct(wishlistInput{Version: model.WishlistCommunicationRequestVersionPreAlphaInvitationV1}))
	require.NoError(t, v.Struct(wishlistInput{Version: model.WishlistCommunicationRequestVersionPreAlphaInvitationV2}))
	require.Error(t, v.Struct(wishlistInput{Version: "unknown"}))
}

func TestValidateRunSourceTags(t *testing.T) {
	t.Parallel()

	type runSourceInput struct {
		Type          model.PipelineRunSourceType                `validate:"runsourcetype"`
		Entrypoint    model.PipelineRunSourceEntrypoint          `validate:"runsourceentrypoint"`
		ActorType     model.EventActorType                       `validate:"eventactortype"`
		MisfireResult model.PipelineTriggerScheduleMisfireResult `validate:"schedulemisfireresult"`
	}

	tests := []struct {
		name            string
		input           runSourceInput
		wantErrContains string
	}{
		{
			name: "AcceptsManualUISource",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
		},
		{
			name: "AcceptsAPIKeySource",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeAPI,
				Entrypoint:    model.PipelineRunSourceEntrypointAPI,
				ActorType:     model.EventActorTypeAPIKey,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultFireOnce,
			},
		},
		{
			name: "AcceptsScheduledSystemSource",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeSchedule,
				Entrypoint:    model.PipelineRunSourceEntrypointScheduler,
				ActorType:     model.EventActorTypeSystem,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultSkipped,
			},
		},
		{
			name: "RejectsUnknownRunSourceTypeSentinel",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeUnknown,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "runsourcetype",
		},
		{
			name: "RejectsUnknownRunSourceEntrypointSentinel",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUnknown,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "runsourceentrypoint",
		},
		{
			name: "RejectsUnknownEventActorTypeSentinel",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUnknown,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "eventactortype",
		},
		{
			name: "RejectsUnsupportedRunSourceType",
			input: runSourceInput{
				Type:          model.PipelineRunSourceType(99),
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "runsourcetype",
		},
		{
			name: "RejectsUnsupportedRunSourceEntrypoint",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypoint(99),
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "runsourceentrypoint",
		},
		{
			name: "RejectsUnsupportedEventActorType",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorType(99),
				MisfireResult: model.PipelineTriggerScheduleMisfireResultOnTime,
			},
			wantErrContains: "eventactortype",
		},
		{
			name: "RejectsUnknownScheduleMisfireResultSentinel",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResultUnknown,
			},
			wantErrContains: "schedulemisfireresult",
		},
		{
			name: "RejectsUnsupportedScheduleMisfireResult",
			input: runSourceInput{
				Type:          model.PipelineRunSourceTypeManual,
				Entrypoint:    model.PipelineRunSourceEntrypointUI,
				ActorType:     model.EventActorTypeUser,
				MisfireResult: model.PipelineTriggerScheduleMisfireResult(99),
			},
			wantErrContains: "schedulemisfireresult",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestDetailForFirstError(t *testing.T) {
	t.Parallel()

	type runSourceInput struct {
		Type    model.PipelineRunSourceType `validate:"required,runsourcetype"`
		Summary string                      `validate:"required"`
	}

	v := New()
	details := []ErrorDetail{
		{Field: "Type", Tag: "required", Detail: "type is required"},
		{Field: "Type", Detail: "type is unsupported"},
		{Field: "Summary", Detail: "summary is required"},
	}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "MapsRequiredTag",
			err:  v.Struct(runSourceInput{Summary: "Manual run"}),
			want: "type is required",
		},
		{
			name: "MapsAnyTagForField",
			err: v.Struct(runSourceInput{
				Type:    model.PipelineRunSourceType(99),
				Summary: "Manual run",
			}),
			want: "type is unsupported",
		},
		{
			name: "MapsNextField",
			err: v.Struct(runSourceInput{
				Type: model.PipelineRunSourceTypeManual,
			}),
			want: "summary is required",
		},
		{
			name: "FallsBackForNonValidatorError",
			err:  xerror.Unexpected().Detail("boom"),
			want: "fallback detail",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, DetailForFirstError(tc.err, "fallback detail", details...))
		})
	}
}

func TestValidateSinkBackendTag(t *testing.T) {
	t.Parallel()

	type sinkInput struct {
		Backend model.SinkBackend `validate:"sinkbackend"`
	}

	tests := []struct {
		name            string
		input           sinkInput
		wantErrContains string
	}{
		{
			name:  "AcceptsFTPBackend",
			input: sinkInput{Backend: model.SinkBackendFTP},
		},
		{
			name:  "AcceptsMongoBackend",
			input: sinkInput{Backend: model.SinkBackendMongo},
		},
		{
			name:            "RejectsUnknownBackend",
			input:           sinkInput{Backend: model.SinkBackend(99)},
			wantErrContains: "sinkbackend",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestPipelineEnumHelpers(t *testing.T) {
	t.Parallel()

	require.True(t, structvalidate.ValidTransformOutputKind(model.TransformOutputKindRows))
	require.False(t, structvalidate.ValidTransformOutputKind(model.TransformOutputKindUnknown))
	require.False(t, structvalidate.ValidTransformOutputKind(model.TransformOutputKind(99)))

	require.True(t, structvalidate.ValidSinkBackend(model.SinkBackendDB))
	require.False(t, structvalidate.ValidSinkBackend(model.SinkBackendUnknown))
	require.False(t, structvalidate.ValidSinkBackend(model.SinkBackend(99)))
}

func TestValidateEnumDomainTags(t *testing.T) {
	t.Parallel()

	type enumInput struct {
		QueryParameterDataType model.ParameterDataType           `validate:"queryparamdatatype"`
		EventActorType         model.EventActorType              `validate:"eventactortype"`
		EventEntityType        model.EventEntityType             `validate:"evententitytype"`
		EventAction            model.EventAction                 `validate:"eventaction"`
		EnvironmentColor       model.EnvironmentColor            `validate:"environmentcolor"`
		SinkIdempotencyClass   pipelinespec.SinkIdempotencyClass `validate:"sinkidempotencyclass"`
	}

	tests := []struct {
		name            string
		input           enumInput
		wantErrContains string
	}{
		{
			name: "AcceptsNaturallyIdempotentSinkClass",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
		},
		{
			name: "AcceptsIdempotentWithKeySinkClass",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassIdempotentWithKey,
			},
		},
		{
			name: "AcceptsDeduplicatedByLedgerSinkClass",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
			},
		},
		{
			name: "AcceptsUnsafeSinkClass",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassUnsafe,
			},
		},
		{
			name: "RejectsUnknownQueryParameterDataType",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeUnknown,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
			wantErrContains: "queryparamdatatype",
		},
		{
			name: "RejectsUnknownEventActorType",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUnknown,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
			wantErrContains: "eventactortype",
		},
		{
			name: "RejectsUnknownEventEntityType",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeUnknown,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
			wantErrContains: "evententitytype",
		},
		{
			name: "RejectsUnknownEventAction",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUnknown,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
			wantErrContains: "eventaction",
		},
		{
			name: "RejectsUnknownEnvironmentColor",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorUnknown,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			},
			wantErrContains: "environmentcolor",
		},
		{
			name: "RejectsUnknownSinkIdempotencyClass",
			input: enumInput{
				QueryParameterDataType: model.ParameterDataTypeText,
				EventActorType:         model.EventActorTypeUser,
				EventEntityType:        model.EventEntityTypeProject,
				EventAction:            model.EventActionUpdate,
				EnvironmentColor:       model.EnvironmentColorBlue,
				SinkIdempotencyClass:   pipelinespec.SinkIdempotencyClassUnknown,
			},
			wantErrContains: "sinkidempotencyclass",
		},
	}

	v := New()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := v.Struct(tc.input)
			if tc.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrContains)
				return
			}

			require.NoError(t, err)
		})
	}
}
