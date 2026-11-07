package views

import (
	"bytes"
	"context"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestMembersPageContentSelectsRole(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"admin", "viewer"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			require.NoError(t, MembersPageContent(
				[]api.Member{{UserSlug: "alex", Email: "alex@example.com", Role: role}},
				nil, false, "", "", false, "", "", "acme-hq",
				MemberInviteFormData{Email: "jane@example.com", Role: role}, MemberRoleFormData{},
				"", "Invitation could not be sent.", "",
				NewOrganizationPermissions("members:create", "members:update"), true,
			).Render(context.Background(), &out))

			doc, err := html.Parse(&out)
			require.NoError(t, err)
			var selections []string
			var visit func(*html.Node)
			visit = func(node *html.Node) {
				if node.Type == html.ElementNode && node.Data == "option" {
					var selected bool
					var value string
					for _, attr := range node.Attr {
						switch attr.Key {
						case "selected":
							selected = true
						case "value":
							value = attr.Val
						}
					}
					if selected {
						selections = append(selections, value)
					}
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(doc)
			require.Equal(t, []string{role, role}, selections, "member and invitation must each select only their role")
		})
	}
}

func TestDefaultMemberInviteFormData(t *testing.T) {
	t.Parallel()

	require.Equal(t, MemberInviteFormData{Role: "viewer"}, DefaultMemberInviteFormData())
}

func TestOrganizationPermissions(t *testing.T) {
	t.Parallel()

	require.False(t, DefaultOrganizationPermissions().Can("members", "update"))

	perms := NewOrganizationPermissions(" members:update ", "", "queries:*")
	require.True(t, perms.Can("members", "update"))
	require.True(t, perms.Can("queries", "delete"))
	require.False(t, perms.Can("members", "delete"))

	require.True(t, NewOrganizationPermissions("*:*").Can("anything", "read"))
}

func TestMembersPageContentExplainsDisabledInvitations(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, MembersPageContent(
		nil,
		nil,
		false,
		"",
		"",
		false,
		"",
		"",
		"acme-hq",
		DefaultMemberInviteFormData(),
		MemberRoleFormData{},
		"",
		"",
		"",
		NewOrganizationPermissions("members:create", "members:update", "members:delete"),
		false,
	).Render(context.Background(), &out))

	html := out.String()
	require.Contains(
		t,
		html,
		"Member invitations are unavailable until the Terms of Service and Privacy Policy are published.",
	)
	require.NotContains(t, html, ">Invite Member</button>")
	require.NotContains(t, html, `id="invite-member"`)
}

func TestMemberRoleFormDataRoleFor(t *testing.T) {
	t.Parallel()
	member := api.Member{UserSlug: "alex", Role: "admin"}
	require.Equal(t, "viewer", (MemberRoleFormData{UserSlug: "alex", Role: "viewer"}).RoleFor(member))
	require.Equal(t, "admin", (MemberRoleFormData{UserSlug: "other", Role: "viewer"}).RoleFor(member))
	require.Equal(t, "admin", (MemberRoleFormData{UserSlug: "alex", Role: "invalid"}).RoleFor(member))
}
