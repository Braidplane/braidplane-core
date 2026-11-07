package views

import (
	"bytes"
	"context"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
)

func TestAccountFormsControlInvitationAcceptance(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		ctx := WithAccountForms(t.Context(), AccountForms{Enabled: enabled})
		var output bytes.Buffer
		require.NoError(t, MemberInvitationActionContent(MemberInvitationActionState{Token: "synthetic-token"}).Render(ctx, &output))
		require.Equal(t, enabled, bytes.Contains(output.Bytes(), []byte(`action="/member-invitations/accept`)))
		require.Contains(t, output.String(), `action="/member-invitations/decline`)
	}
	require.True(t, AccountActionsEnabled(context.Background()))
}

func TestWorkspaceSetupRendersOnlyConfiguredNotice(t *testing.T) {
	const box = "rounded-lg border border-base-300 bg-base-200/40 p-4 text-sm text-base-content/75"
	for _, present := range []bool{false, true} {
		ctx := t.Context()
		if present {
			ctx = WithAccountForms(ctx, AccountForms{Enabled: true, Notice: templ.Raw("Synthetic account notice")})
		}
		var output bytes.Buffer
		require.NoError(t, WorkspaceGatePageContent("personal", "", "Synthetic workspace", "/dashboard", "", "", "").Render(ctx, &output))
		require.Equal(t, present, bytes.Contains(output.Bytes(), []byte(box)))
	}
}
