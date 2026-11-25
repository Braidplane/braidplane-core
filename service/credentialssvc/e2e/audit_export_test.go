package e2e

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"
)

type credentialAuditExportAuthority struct {
	authorization.Service
	org slug.Slug
}

func (a credentialAuditExportAuthority) Can(_ context.Context, in authorization.Input) (bool, error) {
	return in.OrgSlug != nil && *in.OrgSlug == a.org && in.UserSlug == "audit-reader" &&
		in.Action == model.Read && in.Resource == model.ResourceEvents, nil
}

func verifyCredentialAuditExport(t *testing.T, events eventssvc.Service, owner, peer, eventSlug slug.Slug, marker string) {
	t.Helper()
	for _, tc := range []struct {
		name                        string
		authenticated               bool
		authorizedOrg, requestedOrg slug.Slug
		wantStatus, wantRows        int
	}{
		{"owner", true, owner, owner, http.StatusOK, 2},
		{"peer export", true, peer, peer, http.StatusOK, 1},
		{"cross-tenant", true, peer, owner, http.StatusForbidden, 0},
		{"anonymous", false, owner, owner, http.StatusUnauthorized, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := transport.NewServer(
				nil, nil, nil, session.NewStore(), credentialAuditExportAuthority{org: tc.authorizedOrg},
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				events, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "", false, false, nil,
			)
			app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
			app.Use(func(c fiber.Ctx) error {
				if tc.authenticated {
					reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "audit-reader", Source: reqctx.UserPrincipalSourceSession})
				}
				return c.Next()
			})
			api.RegisterHandlers(app, server)
			path := "/organizations/" + tc.requestedOrg.String() + "/security-audit-events/export"
			res, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, res.Body.Close()) }()
			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, res.StatusCode)
			require.NotContains(t, string(body), marker)
			if tc.wantRows == 2 {
				require.Contains(t, string(body), eventSlug.String())
			} else {
				require.NotContains(t, string(body), eventSlug.String())
			}
			if tc.wantRows > 0 {
				rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
				require.NoError(t, err)
				require.Len(t, rows, tc.wantRows)
			}
		})
	}
}
