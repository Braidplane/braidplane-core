package e2e

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/i18n"
	"github.com/Braidplane/braidplane-core/service/email"
)

func TestEmailE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping email E2E test in short mode")
	}
	t.Setenv("ENCRYPTION_KEY_HEX", "0202020202020202020202020202020202020202020202020202020202020202")

	cfg, err := backendconfig.Load("../../../config.toml")
	require.NoError(t, err, "Failed to load config")

	dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
	conn, err := dialer.DialContext(context.Background(), "tcp", cfg.SMTP.Host)
	if err != nil {
		t.Fatalf("Required email E2E infrastructure failed: SMTP server not reachable at %s: %v", cfg.SMTP.Host, err)
	}
	require.NoError(t, conn.Close())

	bundle, err := i18n.NewBundle("en")
	require.NoError(t, err, "Failed to create i18n bundle")

	s, err := email.NewService(cfg.SMTP, bundle)
	require.NoError(t, err, "Failed to create email service")

	user := &model.User{
		Email:  "user@example.com",
		Locale: "pl",
	}

	err = s.EmailVerification(t.Context(), user, "http://example.com/verify?token=abc123")
	require.NoError(t, err, "Failed to send email verification")
}
