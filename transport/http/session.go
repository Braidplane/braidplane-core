package http

import (
	"log/slog"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func (s *server) PostSessions(c fiber.Ctx) error {
	newSession := new(api.NewSession)
	if err := c.Bind().Body(newSession); err != nil {
		return apierror.InvalidRequestBody()
	}

	result, err := s.usersService.BeginLogin(
		c.Context(),
		string(newSession.Email),
		newSession.Password,
	)
	if err != nil {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.login.failed", "error", serviceErrorHTTPStatus(err), "")
		return mapServiceError(err)
	}

	if result.MFARequired {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.login.mfa_challenge", "success", fiber.StatusAccepted, "")
		return c.Status(fiber.StatusAccepted).JSON(api.MFAChallenge{
			ChallengeToken: result.MFAChallenge.ChallengeToken,
			ExpiresAt:      result.MFAChallenge.ExpiresAt,
		})
	}

	user := result.User
	if err := reqctx.ResetAndSetAuthenticatedUser(c, s.sessionStore, user.Slug, user.AuthenticationVersion, true); err != nil {
		return apierror.InternalServerError()
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.login.succeeded", "success", fiber.StatusOK, "")
	response := userToHTTP(user)
	response.AuthenticationVersion = &user.AuthenticationVersion
	return c.JSON(response)
}

func (s *server) CreateSession(c fiber.Ctx) error {
	return s.PostSessions(c)
}

func (s *server) VerifySessionMFA(c fiber.Ctx) error {
	var req api.MFAVerification
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	user, err := s.usersService.VerifySessionMFA(
		c.Context(),
		req.ChallengeToken,
		req.Code,
	)
	if err != nil {
		logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelWarn, "auth.login.mfa_failed", "error", serviceErrorHTTPStatus(err), "")
		return mapServiceError(err)
	}

	if err := reqctx.ResetAndSetAuthenticatedUser(c, s.sessionStore, user.Slug, user.AuthenticationVersion, true); err != nil {
		return apierror.InternalServerError()
	}

	logAuthSecurityEvent(c, s.sessionStore, slog.Default(), slog.LevelInfo, "auth.login.succeeded", "success", fiber.StatusOK, "")
	response := userToHTTP(user)
	response.AuthenticationVersion = &user.AuthenticationVersion
	return c.JSON(response)
}
