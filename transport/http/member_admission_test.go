package http

import (
	"context"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

type fakeMemberAdmission struct{ err error }

func (f fakeMemberAdmission) CheckMemberAdmission(context.Context, slug.Slug) error { return f.err }

func TestMemberAdmissionRequiresPolicyAndPreservesDecision(t *testing.T) {
	t.Parallel()

	t.Run("missing policy fails closed", func(t *testing.T) {
		err := (&server{}).checkMemberAdmission(t.Context(), "tenant")
		var detail *xerror.Error
		require.True(t, xerror.As(err, &detail))
		require.Equal(t, xerror.KindUnavailable, detail.Kind())
	})
	t.Run("admitted by policy", func(t *testing.T) {
		s := &server{memberAdmission: fakeMemberAdmission{}}
		require.NoError(t, s.checkMemberAdmission(t.Context(), "tenant"))
	})
	t.Run("policy denial preserved", func(t *testing.T) {
		denied := xerror.New(xerror.KindForbidden, "MEMBER_DENIED", "Membership is restricted")
		s := &server{memberAdmission: fakeMemberAdmission{err: denied}}
		require.ErrorIs(t, s.checkMemberAdmission(t.Context(), "tenant"), denied)
	})
}
