package views

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestAPIKeyCreateDefaults(t *testing.T) {
	t.Parallel()

	require.Equal(t, APIKeyCreateFormData{Role: "viewer"}, DefaultAPIKeyCreateFormData())
	require.Equal(t, "0 keys", APIKeyCountLabel(0))
	require.Equal(t, "1 key", APIKeyCountLabel(1))
	require.Equal(t, "2 keys", APIKeyCountLabel(2))
}

func TestAPIKeyStatusHelpers(t *testing.T) {
	t.Parallel()

	now := time.Now()
	future := time.Now().Add(time.Hour)

	tests := []struct {
		name       string
		key        api.APIKey
		wantLabel  string
		wantBadge  string
		wantExpiry bool
	}{
		{
			name:      "active",
			key:       api.APIKey{ExpiresAt: &future},
			wantLabel: "Active",
			wantBadge: "badge badge-success",
		},
		{
			name:       "expired",
			key:        api.APIKey{ExpiresAt: &now},
			wantLabel:  "Expired",
			wantBadge:  "badge badge-warning",
			wantExpiry: true,
		},
		{
			name:       "revoked overrides expired",
			key:        api.APIKey{ExpiresAt: &now, RevokedAt: &now},
			wantLabel:  "Revoked",
			wantBadge:  "badge badge-outline",
			wantExpiry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantLabel, APIKeyStatusLabel(tt.key))
			require.Equal(t, tt.wantBadge, APIKeyStatusBadgeClass(tt.key))
			require.Equal(t, tt.wantExpiry, APIKeyExpired(tt.key))
		})
	}
}

func TestAPIKeyTimeFormatting(t *testing.T) {
	t.Parallel()

	value := time.Date(2026, 5, 14, 9, 30, 0, 0, time.UTC)

	require.Equal(t, "Never", FormatOptionalAPIKeyTime(nil))
	require.Contains(t, FormatOptionalAPIKeyTime(&value), "2026-05-14 09:30")
	require.Empty(t, FormatAPIKeyDateTimeLocal(nil))
	require.Equal(t, value.Local().Format("2006-01-02T15:04"), FormatAPIKeyDateTimeLocal(&value))
}
