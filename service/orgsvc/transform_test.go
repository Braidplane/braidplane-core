package orgsvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/stretchr/testify/require"
)

func TestOrganizationProjectionPreservesIdentityAndRole(t *testing.T) {
	t.Parallel()
	svc := &service{}
	org := organizations.Organization{Name: "Community", Slug: "community"}
	require.Equal(t, org.Slug, svc.toOrganization(&org).Slug)
	result := svc.toOrganizationWithRole(&organizations.OrganizationWithRole{Organization: org, MemberRole: "admin"})
	require.Equal(t, org.Name, result.Name)
	require.Equal(t, "admin", result.MemberRole)
}
