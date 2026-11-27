package mcpauthsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/mcpauth"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var ErrInvalidMachineAdminSession = xerror.New(xerror.KindForbidden, "MCP_MACHINE_ADMIN_SESSION_REQUIRED", "Machine client management requires a valid browser session")

type MachineAdminRequest struct {
	Browser              BrowserIdentity
	Organization, APIKey slug.Slug
}

type MachineAdministration struct {
	tokens *TokenService
	audit  workerproducer.EventProducer
}

func NewMachineAdministration(tokens *TokenService, audit workerproducer.EventProducer) (*MachineAdministration, error) {
	if tokens == nil || tokens.Deployment() == nil || audit == nil {
		return nil, xerror.Unexpected().Detail("MCP machine administration requires a token service and audit producer")
	}
	return &MachineAdministration{tokens: tokens, audit: audit}, nil
}

// Register rechecks browser proof and records required audit evidence in the
// same transaction as registration. The caller authorizes the exact API key.
func (a *MachineAdministration) Register(ctx context.Context, request MachineAdminRequest) (MachineRegistration, error) {
	if !authorization.RecentSessionAuthentication(request.Browser.AuthenticatedAt) {
		return MachineRegistration{}, ErrInvalidMachineAdminSession
	}
	return postgres.InTx(ctx, a.tokens.txm, func(ctx context.Context, tx postgres.DBTX) (MachineRegistration, error) {
		if err := checkMachineAdminSession(ctx, tx, request); err != nil {
			return MachineRegistration{}, err
		}
		registration, err := a.tokens.RegisterMachineClient(ctx, request.Organization, request.APIKey)
		if err != nil {
			return MachineRegistration{}, err
		}
		return registration, a.recordAudit(ctx, tx, request, registration.ClientID, model.EventActionCreate)
	})
}

// Disconnect only changes a registration attached to the key authorized by the caller.
func (a *MachineAdministration) Disconnect(ctx context.Context, request MachineAdminRequest, clientID string) error {
	return a.tokens.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := checkMachineAdminSession(ctx, tx, request); err != nil {
			return err
		}
		if err := a.tokens.DisconnectMachineClient(ctx, request.Organization, request.APIKey, clientID); err != nil {
			return err
		}
		return a.recordAudit(ctx, tx, request, clientID, model.EventActionRevoke)
	})
}

func checkMachineAdminSession(ctx context.Context, tx postgres.DBTX, request MachineAdminRequest) error {
	if !request.Browser.valid() {
		return ErrInvalidMachineAdminSession
	}
	err := mcpauth.CheckMachineAdminSession(ctx, tx, request.Organization, request.Browser.User, request.Browser.AuthenticationVersion)
	if xerror.Is(err, mcpauth.ErrNotFound) {
		return ErrInvalidMachineAdminSession
	}
	return err
}

type machineClientAuditEvent struct {
	key      slug.Slug
	clientID string
	action   model.EventAction
}

func (e machineClientAuditEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix: "evt-mcp-machine-client", EventClass: model.EventClassSecurityAudit,
		EntityType: model.EventEntityTypeAPIKey, EntitySlug: e.key, Action: e.action,
		SchemaVersion: model.CurrentEventSchemaVersion, Metadata: map[string]any{"mcp_client_id": e.clientID},
	}, nil
}

func (a *MachineAdministration) recordAudit(ctx context.Context, tx postgres.DBTX, request MachineAdminRequest, clientID string, action model.EventAction) error {
	_, err := a.audit.EnqueueRequired(ctx, tx, request.Organization, model.UserEventActor(request.Browser.User), machineClientAuditEvent{
		key: request.APIKey, clientID: clientID, action: action,
	})
	if err != nil {
		return xerror.Wrapf("enqueue MCP machine client audit event: %w", err)
	}
	return nil
}
