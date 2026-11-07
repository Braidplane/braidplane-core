package pipelinesvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestServiceValidationHistoryRequiresPrincipalBeforePersistence(t *testing.T) {
	t.Parallel()

	svc := &service{authorizationService: allowAuthorizationService{}}
	principal := ValidationHistoryPrincipal{}

	_, err := svc.RecordValidationResult(
		context.Background(),
		"acme",
		"default",
		"prod",
		principal,
		PipelineValidationResultSourceAPI,
		nil,
		nil,
	)
	require.ErrorIs(t, err, ErrPipelineValidationPrincipalRequired)

	_, err = svc.ListValidationResults(
		context.Background(),
		"acme",
		"default",
		"prod",
		principal,
		PipelineValidationResultFilters{},
		model.ListRequest{},
	)
	require.ErrorIs(t, err, ErrPipelineValidationPrincipalRequired)

	_, err = svc.GetValidationResult(
		context.Background(),
		"acme",
		"default",
		"prod",
		principal,
		"validation-acme-a1b2c3",
	)
	require.ErrorIs(t, err, ErrPipelineValidationPrincipalRequired)

	_, err = svc.RevalidatePipeline(
		context.Background(),
		"acme",
		"default",
		"prod",
		principal,
		"users-sync",
		"test-user",
	)
	require.ErrorIs(t, err, ErrPipelineValidationPrincipalRequired)
}

func TestServiceValidationHistoryPermissionSplit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		dataSourceAllowed bool
		dataSourceErr     error
		wantErrIs         error
		wantErrContains   string
	}{
		{
			name:              "AllowsSplitReadPermissions",
			dataSourceAllowed: true,
		},
		{
			name:              "RejectsDataSourceDenied",
			dataSourceAllowed: false,
			wantErrIs:         xerror.Forbidden(xerror.PipelineValidationResult),
		},
		{
			name:              "ReturnsDataSourceAuthorizationError",
			dataSourceAllowed: true,
			dataSourceErr:     xerror.Unexpected().Detail("data source authorization failed"),
			wantErrContains:   "authorize pipeline validation history",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := []authorization.Input{}
			svc := &service{
				proofLinks: newTestProofLinks(),
				authorizationService: allowAuthorizationService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						calls = append(calls, input)
						if input.Resource == model.ResourceDataSources {
							return tc.dataSourceAllowed, tc.dataSourceErr
						}
						return true, nil
					},
				},
			}

			err := svc.requireValidationHistoryPermissions(
				context.Background(),
				"acme",
				ValidationHistoryPrincipal{UserSlug: "test-user"},
				validationHistoryReadPermissions()...,
			)
			switch {
			case tc.wantErrIs != nil:
				require.ErrorIs(t, err, tc.wantErrIs)
			case tc.wantErrContains != "":
				require.ErrorContains(t, err, tc.wantErrContains)
			default:
				require.NoError(t, err)
			}

			require.Len(t, calls, 2)
			require.Equal(t, slug.Slug("test-user"), calls[0].UserSlug)
			require.Equal(t, model.Read, calls[0].Action)
			require.Equal(t, model.ResourcePipelines, calls[0].Resource)
			require.Equal(t, slug.Slug("acme"), *calls[0].OrgSlug)
			require.Equal(t, model.Read, calls[1].Action)
			require.Equal(t, model.ResourceDataSources, calls[1].Resource)
		})
	}
}

func TestServiceValidationHistoryRejectsCrossOrgAPIKeyPrincipal(t *testing.T) {
	t.Parallel()

	svc := &service{
		proofLinks: newTestProofLinks(),
		authorizationService: allowAuthorizationService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				t.Fatal("cross-organization api key principal must not reach authorization service")
				return false, nil
			},
		},
	}

	err := svc.requireValidationHistoryPermissions(
		context.Background(),
		"acme",
		ValidationHistoryPrincipal{
			APIKeySlug:     "key-prod",
			APIKeyOrgSlug:  "other-org",
			APIKeyRoleSlug: "automation",
		},
		validationHistoryPermission{action: model.Read, resource: model.ResourcePipelines},
	)
	require.ErrorIs(t, err, xerror.Forbidden(xerror.PipelineValidationResult))
}
