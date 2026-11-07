package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
)

func TestAuthPagesUseSharedControlRoomShell(t *testing.T) {
	t.Parallel()

	pages := map[string]templ.Component{
		"login":                      LoginPage("Sign in", "", "", "", "/dashboard", true, ""),
		"signup":                     SignupPage("Sign up", "", "", "", "", "/dashboard", true, SignupState{}),
		"signup registration closed": SignupPage("Sign up", "", "", "", "", "", false, SignupState{}),
		"email verification":         EmailVerificationPage("Verify email", "operator@example.com", "", "/dashboard"),
		"verification success":       EmailVerificationResultPage("Email verified", true, "Email verified.", ""),
		"verification failure":       EmailVerificationResultPage("Verification failed", false, "Link expired.", ""),
		"forgot password":            ForgotPasswordPage("Forgot password", "", "", "", ""),
		"reset password":             ResetPasswordPage("Reset password", "reset-token", "", "", ""),
		"auth error":                 AuthErrorPage("Authentication failed", "Access denied."),
		"mfa":                        MFAPage("Check your email", "challenge-token", "/dashboard", "", ""),
		"workspace setup":            WorkspaceGatePage("Set up workspace", "personal", "", "Operator's workspace", "/dashboard", "", "", ""),
	}

	for name, page := range pages {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, page.Render(context.Background(), &out))

			html := out.String()
			require.Contains(t, html, `data-theme="control-room"`)
			require.Contains(t, html, `data-auth-shell`)
			require.Contains(t, html, `data-auth-flow`)
			require.Contains(t, html, `id="main-content"`)
			require.Contains(t, html, `/assets/icons/app/logo-1024-transparent.png?v=`)
			require.Contains(t, html, `h-24 w-24 shrink-0 object-contain`)
			require.NotContains(t, html, `>B</span>`)
			require.Contains(t, html, `>Braidplane<`)
			require.Contains(t, html, `>Alpha<`)
			require.NotContains(t, html, `data-theme="light"`)
		})
	}
}

func TestSignupAndVerificationRecoveryKeepNativeFormSemantics(t *testing.T) {
	t.Parallel()

	var signup bytes.Buffer
	require.NoError(t, SignupPage("Sign up", "", "", "", "", "", true, SignupState{}).Render(WithGoogleSignIn(context.Background(), true), &signup))
	html := signup.String()
	require.Contains(t, html, `autocomplete="name"`)
	require.Contains(t, html, `autocomplete="email"`)
	require.Contains(t, html, `autocomplete="new-password"`)
	require.Contains(t, html, `minlength="8"`)
	googleIndex := strings.Index(html, "Continue with Google")
	signupFormIndex := strings.Index(html, `action="/auth/signup"`)
	require.NotEqual(t, -1, googleIndex)
	require.NotEqual(t, -1, signupFormIndex)
	require.Less(t, googleIndex, signupFormIndex)
	require.NotContains(t, html, "Use test data only.")
	require.NotContains(t, html, "Do not connect production systems")
	require.NotContains(t, html, "(version v1)")
	require.Contains(t, html, "Create account")

	var verification bytes.Buffer
	require.NoError(t, EmailVerificationPage("Verify email", "operator@example.com", "", "/dashboard").Render(context.Background(), &verification))
	verificationHTML := verification.String()
	require.NotContains(t, verificationHTML, ">mail</div>")
	require.NotContains(t, verificationHTML, "Check your email.")
	require.Contains(t, verificationHTML, "Verify your email")
	require.Contains(t, verificationHTML, "Sent to: operator@example.com")

	var failed bytes.Buffer
	require.NoError(t, EmailVerificationResultPage("Verification failed", false, "Link expired.", "").Render(context.Background(), &failed))
	require.Contains(t, failed.String(), `action="/auth/resend-verification"`)
	require.Contains(t, failed.String(), `name="email"`)
	require.NotContains(t, failed.String(), "Back to sign up")
}

func TestAuthShellKeepsCriticalFormSemantics(t *testing.T) {
	t.Parallel()

	var login bytes.Buffer
	require.NoError(t, LoginPage("Sign in", "operator@example.com", "", "", "/runs", true, "").Render(WithGoogleSignIn(context.Background(), true), &login))
	require.Contains(t, login.String(), `method="post" action="/auth/login"`)
	require.Contains(t, login.String(), `name="return_to" value="/runs"`)
	require.Contains(t, login.String(), `href="/api/auth/google?return_to=%2Fruns"`)

	var mfa bytes.Buffer
	require.NoError(t, MFAPage("Check your email", "challenge-token", "/runs", "", "").Render(context.Background(), &mfa))
	require.Contains(t, mfa.String(), `hx-post="/auth/mfa"`)
	require.Contains(t, mfa.String(), `name="challenge_token" value="challenge-token"`)
	require.Contains(t, mfa.String(), `name="return_to" value="/runs"`)
	require.Contains(t, mfa.String(), `autocomplete="one-time-code"`)
}
