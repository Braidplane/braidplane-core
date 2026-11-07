package http

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestMapServiceError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        error
		wantErr      *apierror.Error
		wantDetailed string
	}{
		{
			name:         "DestinationConstraintViolation",
			input:        xerror.DestinationConstraintViolation().WithPublicDetail("Review destination constraints."),
			wantErr:      apierror.New(400, "DESTINATION_CONSTRAINT_VIOLATION", "The destination rejected the write because it violates a constraint."),
			wantDetailed: "Review destination constraints.",
		},
		// User errors
		{
			name:    "UserNotFound",
			input:   userssvc.ErrUserNotFound,
			wantErr: apierror.New(404, "USER_NOT_FOUND", "user not found"),
		},
		{
			name:    "UserEmailAlreadyExists",
			input:   userssvc.ErrEmailAlreadyExists,
			wantErr: apierror.New(409, "USER_EMAIL_ALREADY_EXISTS", "user email already exists"),
		},
		{
			name:    "UserSlugAlreadyExists",
			input:   userssvc.ErrSlugAlreadyExists,
			wantErr: apierror.New(409, "USER_ALREADY_EXISTS", "user already exists"),
		},
		{
			name:    "InvalidUsernameOrPassword",
			input:   userssvc.ErrInvalidUsernameOrPassword,
			wantErr: apierror.New(401, "INVALID_CREDENTIALS", "invalid credentials"),
		},
		{
			name:    "EmailNotVerified",
			input:   userssvc.ErrEmailNotVerified,
			wantErr: apierror.New(401, "EMAIL_NOT_VERIFIED", "email not verified"),
		},
		{
			name:    "UserAlreadyVerified",
			input:   userssvc.ErrAlreadyVerified,
			wantErr: apierror.New(400, "ALREADY_VERIFIED", "already verified"),
		},
		{
			name:    "InvalidToken",
			input:   userssvc.ErrInvalidToken,
			wantErr: apierror.New(400, "INVALID_TOKEN", "invalid token"),
		},
		{
			name:    "UserInvalidInput",
			input:   userssvc.ErrInvalidInput,
			wantErr: apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},

		// Organization errors
		{
			name:    "OrganizationNotFound",
			input:   orgsvc.ErrOrganizationNotFound,
			wantErr: apierror.New(404, "ORGANIZATION_NOT_FOUND", "organization not found"),
		},
		{
			name:  "OrganizationSlugAlreadyExists",
			input: orgsvc.ErrSlugAlreadyExists,
			wantErr: apierror.New(
				409, "ORGANIZATION_ALREADY_EXISTS", "organization already exists",
			),
		},

		// Member errors
		{
			name:    "MemberNotFound",
			input:   memberssvc.ErrMemberNotFound,
			wantErr: apierror.New(404, "MEMBER_NOT_FOUND", "member not found"),
		},
		{
			name:    "MemberAlreadyExists",
			input:   memberssvc.ErrMemberAlreadyExists,
			wantErr: apierror.New(409, "MEMBER_ALREADY_EXISTS", "member already exists"),
		},
		{
			name:    "CannotRemoveLastAdmin",
			input:   memberssvc.ErrCannotRemoveLastAdmin,
			wantErr: apierror.New(400, "CANNOT_REMOVE_OWNER", "cannot remove owner"),
		},
		{
			name:    "CannotDemoteLastAdmin",
			input:   memberssvc.ErrCannotDemoteLastAdmin,
			wantErr: apierror.New(400, "CANNOT_DEMOTE_OWNER", "cannot demote owner"),
		},
		{
			name:    "MemberInvalidRole",
			input:   memberssvc.ErrInvalidRole,
			wantErr: apierror.New(400, "MEMBER_INVALID_INPUT", "member invalid input"),
		},

		// Role errors
		{
			name:    "RoleNotFound",
			input:   rolessvc.ErrRoleNotFound,
			wantErr: apierror.New(404, "ROLE_NOT_FOUND", "role not found"),
		},
		{
			name:    "RoleSlugAlreadyExists",
			input:   rolessvc.ErrRoleSlugAlreadyExists,
			wantErr: apierror.New(409, "ROLE_ALREADY_EXISTS", "role already exists"),
		},
		{
			name:  "CannotDeleteDefaultRole",
			input: rolessvc.ErrCannotDeleteDefaultRole,
			wantErr: apierror.New(
				400, "ROLE_CANNOT_DELETE_DEFAULT", "cannot delete default role",
			),
		},
		{
			name:  "CannotUpdateDefaultRole",
			input: rolessvc.ErrCannotUpdateDefaultRole,
			wantErr: apierror.New(
				400, "ROLE_CANNOT_UPDATE_DEFAULT", "cannot update default role",
			),
		},
		{
			name:    "RoleInvalidRoleSlug",
			input:   rolessvc.ErrInvalidRoleSlug,
			wantErr: apierror.New(400, "ROLE_INVALID_INPUT", "role invalid input"),
		},
		{
			name:    "RoleInvalidInput",
			input:   rolessvc.ErrInvalidInput,
			wantErr: apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},

		// Data source errors
		{
			name:    "DataSourceNotFound",
			input:   datasourcessvc.ErrDataSourceNotFound,
			wantErr: apierror.New(404, "DATA_SOURCE_NOT_FOUND", "data source not found"),
		},
		{
			name:  "DataSourceSlugTaken",
			input: datasourcessvc.ErrSlugTaken,
			wantErr: apierror.New(
				409, "DATA_SOURCE_ALREADY_EXISTS", "data source already exists",
			),
		},
		{
			name:    "DataSourceInvalidInput",
			input:   datasourcessvc.ErrInvalidInput,
			wantErr: apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
		{
			name:    "DataSourceInvalidType",
			input:   datasourcessvc.ErrInvalidDataSourceType,
			wantErr: apierror.New(400, "DATA_SOURCE_INVALID_INPUT", "data source invalid input"),
		},
		{
			name:  "DataSourceConnectionFailed",
			input: datasourcessvc.ErrConnectionFailed,
			wantErr: apierror.New(
				422, "DATA_SOURCE_CONNECTION_FAILED", "data source connection failed",
			),
		},
		{
			name:  "DataSourceSchemaIntrospectionUnsupported",
			input: datasourcessvc.ErrSchemaIntrospectionUnsupported,
			wantErr: apierror.New(
				422,
				"DATA_SOURCE_SCHEMA_INTROSPECTION_UNSUPPORTED",
				"data source schema introspection unsupported",
			),
		},

		// Query errors
		{
			name:    "QueryNotFound",
			input:   queriessvc.ErrQueryNotFound,
			wantErr: apierror.New(404, "QUERY_NOT_FOUND", "query not found"),
		},
		{
			name:    "QuerySlugTaken",
			input:   queriessvc.ErrSlugTaken,
			wantErr: apierror.New(409, "QUERY_ALREADY_EXISTS", "query already exists"),
		},
		{
			name:    "QueryInvalidInput",
			input:   queriessvc.ErrInvalidInput,
			wantErr: apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
		{
			name:  "QueryParameterValidationFailed",
			input: queriessvc.ErrParameterValidationFailed,
			wantErr: apierror.New(
				400, "PARAMETER_VALIDATION_FAILED", "parameter validation failed",
			),
		},
		{
			name:    "QueryTimeout",
			input:   queriessvc.ErrQueryTimeout,
			wantErr: apierror.New(408, "QUERY_TIMEOUT", "query timeout"),
		},
		{
			name:    "QueryDataSourceUnavailable",
			input:   queriessvc.ErrDataSourceUnavailable,
			wantErr: apierror.New(422, "DATA_SOURCE_UNAVAILABLE", "data source unavailable"),
		},
		{
			name:    "QueryExecutionFailed",
			input:   queriessvc.ErrQueryExecutionFailed,
			wantErr: apierror.New(422, "QUERY_UNAVAILABLE", "query unavailable"),
		},
		{
			name:    "QueryUnsupportedDataSourceType",
			input:   queriessvc.ErrUnsupportedDataSourceType,
			wantErr: apierror.New(422, "DATA_SOURCE_UNSUPPORTED", "data source unsupported"),
		},

		// Wrapping preserves kind and code
		{
			name:    "WrappedErrorPreservesMapping",
			input:   fmt.Errorf("service: %w", userssvc.ErrUserNotFound),
			wantErr: apierror.New(404, "USER_NOT_FOUND", "user not found"),
		},
		{
			name: "XerrorPublicDetailIsReturnedWhenExplicitlyMarkedSafe",
			input: xerror.InvalidInput(xerror.DataSource).
				WithPublicDetail("invalid credentials format"),
			wantErr:      apierror.New(400, "DATA_SOURCE_INVALID_INPUT", "data source invalid input"),
			wantDetailed: "invalid credentials format",
		},
		{
			name:    "ProviderInvalidCredentialsPreservesUnauthorizedMapping",
			input:   xerror.InvalidCredentials().Detail("provider rejected client credentials"),
			wantErr: apierror.New(401, "INVALID_CREDENTIALS", "invalid credentials"),
		},
		{
			name: "XerrorErrorfMapsToInternalServerError",
			input: xerror.Unexpected().Detailf(
				"storage bootstrap failed: %s",
				"boom",
			),
			wantErr: apierror.New(500, "INTERNAL_ERROR", "internal error"),
		},

		// Unknown errors
		{
			name:    "UnknownErrorMapsToInternalServerError",
			input:   fmt.Errorf("unexpected failure"),
			wantErr: apierror.InternalServerError(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mapServiceError(tt.input)
			var apiErr *apierror.Error
			require.ErrorAs(t, got, &apiErr)
			assert.Equal(t, tt.wantErr.HTTPStatus, apiErr.HTTPStatus)
			assert.Equal(t, tt.wantErr.Code, apiErr.Code)
			assert.Equal(t, tt.wantErr.Message, apiErr.Message)
			if tt.wantDetailed != "" {
				assert.Equal(t, tt.wantDetailed, apiErr.DetailedError)
			} else {
				assert.Empty(t, apiErr.DetailedError)
			}
		})
	}
}
