package proofconfigsvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/proofconfig"
	"github.com/stretchr/testify/require"
)

func TestAssessmentTargetsRequireExplicitCurrentContext(t *testing.T) {
	contexts := []proofconfig.TargetContext{{Project: "etl", Environment: "staging", Ref: "refs/heads/main", Directory: "pipelines"}, {Project: "etl", Environment: "production", Ref: "refs/heads/main", Directory: "pipelines"}}
	target := proofconfig.Target{Project: "etl", Environment: "staging", Ref: "refs/heads/main", Path: "pipelines/contacts.yaml"}
	require.NoError(t, validateTargets([]proofconfig.Target{target}, contexts))
	second := target
	second.Environment = "production"
	require.NoError(t, validateTargets([]proofconfig.Target{target, second}, contexts))
	require.ErrorIs(t, validateTargets(nil, contexts), ErrConfiguration)
	require.ErrorIs(t, validateTargets([]proofconfig.Target{target, target}, contexts), ErrConfiguration)
	for _, value := range []string{"../contacts.yaml", "/pipelines/contacts.yaml", "pipelines/../secret.yaml", "pipelines-extra/contacts.yaml", "pipelines\\contacts.yaml", "pipelines/contacts.yaml\n"} {
		invalid := target
		invalid.Path = value
		require.ErrorIs(t, validateTargets([]proofconfig.Target{invalid}, contexts), ErrConfiguration, value)
	}
	invalid := target
	invalid.Environment = ""
	require.ErrorIs(t, validateTargets([]proofconfig.Target{invalid}, contexts), ErrConfiguration)
	invalid = target
	invalid.Ref = "refs/heads/other"
	require.ErrorIs(t, validateTargets([]proofconfig.Target{invalid}, contexts), ErrConfiguration)
	require.True(t, sameTargets([]proofconfig.Target{target, second}, []proofconfig.Target{second, target}))
	require.False(t, sameTargets([]proofconfig.Target{target}, []proofconfig.Target{second}))
}

func TestEligibilityFailsClosedUntilManagedRepositoryIsUsable(t *testing.T) {
	t.Parallel()
	ready := proofconfig.Source{Provider: "github", Managed: true, CredentialActive: true, RepositoryAccessible: true, Active: true}
	require.Empty(t, eligibility(ready))
	for _, tc := range []struct {
		name, message string
		change        func(*proofconfig.Source)
	}{
		{"generic", "Generic Git", func(s *proofconfig.Source) { s.Provider = "git" }},
		{"unmanaged", "managed provider", func(s *proofconfig.Source) { s.Managed = false }},
		{"revoked", "Reconnect", func(s *proofconfig.Source) { s.CredentialActive = false }},
		{"repository access lost", "Restore provider access", func(s *proofconfig.Source) { s.RepositoryAccessible = false }},
		{"untested", "Test the repository", func(s *proofconfig.Source) { s.Active = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := ready
			tc.change(&source)
			require.Contains(t, eligibility(source), tc.message)
		})
	}
}
