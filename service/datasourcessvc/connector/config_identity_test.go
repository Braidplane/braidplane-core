package connector

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestPublicConfigIdentity(t *testing.T) {
	a, err := PublicConfigDigest(model.DataSourceTypeS3, []byte(`{"region":"test","bucket":"orders"}`))
	require.NoError(t, err)
	b, err := PublicConfigDigest(model.DataSourceTypeS3, []byte(`{"bucket":"orders", "region":"test"}`))
	require.NoError(t, err)
	require.Equal(t, a, b)
	b, err = PublicConfigDigest(model.DataSourceTypeS3, []byte(`{"region":"test","bucket":"other"}`))
	require.NoError(t, err)
	require.NotEqual(t, a, b)
	for _, raw := range []string{
		`{"base_url":"https://user:password@example.test"}`,
		`{"base_url":"ssh://git@example.test/team/repo.git"}`,
		`{"base_url":"https://example.test?token=private"}`,
		`{"base_url":"https://example.test#private"}`,
		`{"base_url":"https://example.test","token":"private"}`,
		`{"base_url":"{{runtime.secrets.host}}"}`,
	} {
		digest, err := PublicConfigDigest(model.DataSourceTypeHTTP, []byte(raw))
		require.ErrorIs(t, err, ErrUnsafeConfigIdentity)
		require.Empty(t, digest)
		require.NotContains(t, err.Error(), "private")
	}
}

func TestPublicGitSSHConfigIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		allowed       bool
	}{
		{"SCP SSH", "git@example.test:team/repo.git", true},
		{"SSH URL", "ssh://git@example.test/team/repo.git", true},
		{"HTTPS", "https://example.test/team/repo.git", true},
		{"HTTPS user", "https://git@example.test/team/repo.git", false},
		{"SCP password user", "git:private@example.test:team/repo.git", false},
		{"SSH empty password", "ssh://git:@example.test/team/repo.git", false},
		{"SSH password", "ssh://git:private@example.test/team/repo.git", false},
		{"SSH private user", "ssh://private@example.test/team/repo.git", false},
		{"SCP private user", "private@example.test:team/repo.git", false},
		{"SCP query", "git@example.test:team/repo.git?token=private", false},
		{"SCP fragment", "git@example.test:team/repo.git#private", false},
		{"SCP missing host", "git@:team/repo.git", false},
		{"SCP missing path", "git@example.test:", false},
		{"SSH missing path", "ssh://git@example.test", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"repo_url": tc.address, "default_branch": "main"})
			require.NoError(t, err)
			digest, err := PublicConfigDigest(model.DataSourceTypeGit, raw)
			if tc.allowed {
				require.NoError(t, err)
				require.NotEmpty(t, digest)
			} else {
				require.ErrorIs(t, err, ErrUnsafeConfigIdentity)
				require.Empty(t, digest)
				require.NotContains(t, err.Error(), "private")
			}
		})
	}
}
