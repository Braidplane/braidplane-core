package rolessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

func (s *service) ListRoleGrants(ctx context.Context, orgSlug slug.Slug) ([]model.RoleGrant, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) ([]model.RoleGrant, error) {
		return s.rolesRepo.ListRoleGrants(ctx, tx, orgSlug)
	})
}

func (s *service) CreateRoleGrant(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, grant model.NewRoleGrant) (*model.RoleGrant, error) {
	if err := validateGrant(orgSlug, grant); err != nil {
		return nil, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.RoleGrant, error) {
		created, err := s.rolesRepo.CreateRoleGrant(ctx, tx, orgSlug, grant)
		if err != nil {
			return nil, err
		}
		event := grantAuditEvent{role: grant.RoleSlug, metadata: map[string]any{
			"grant_change": "grant",
			"grant_slug":   created.Slug.String(),
			"subject_kind": grant.SubjectKind,
			"subject_slug": grant.SubjectSlug.String(),
			"scope":        grant.Scope,
			"target_kind":  grant.Target.Kind,
			"target_slug":  grant.Target.Slug.String(),
		}}
		err = s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, event)
		return created, err
	})
}

func validateGrant(orgSlug slug.Slug, grant model.NewRoleGrant) error {
	if grant.SubjectKind != "member" && grant.SubjectKind != "api_key" {
		return xerror.ValidationFailed().Detail("select a member or API key for the role grant")
	}
	if grant.SubjectSlug == "" || grant.RoleSlug == "" || grant.Target.Slug == "" {
		return xerror.ValidationFailed().Detail("role grant requires a subject, role and target")
	}
	switch grant.Scope {
	case "organization":
		if grant.Target.Kind == "organizations" && grant.Target.Slug == orgSlug {
			return nil
		}
	case "project":
		if grant.Target.Kind == "projects" {
			return nil
		}
	case "environment":
		if grant.Target.Kind == "environments" {
			return nil
		}
	case "resource":
		if grant.Target.Kind != "" && grant.Target.Kind != "organizations" {
			return nil
		}
	}
	return xerror.ValidationFailed().Detail("role grant scope must match its target")
}

func (s *service) RevokeRoleGrant(ctx context.Context, orgSlug, grantSlug slug.Slug, actor model.EventActor) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		role, err := s.rolesRepo.RevokeRoleGrant(ctx, tx, orgSlug, grantSlug)
		if err != nil {
			return err
		}
		event := grantAuditEvent{role: role, metadata: map[string]any{
			"grant_change": "revoke",
			"grant_slug":   grantSlug.String(),
		}}
		return s.enqueueRequiredRoleAuditEvent(ctx, tx, orgSlug, actor, event)
	})
}

type grantAuditEvent struct {
	role     slug.Slug
	metadata map[string]any
}

func (e grantAuditEvent) EventRecord() (eventrecorder.Record, error) {
	return roleRecord("evt-role-grant", e.role, model.EventActionUpdate, e.metadata), nil
}
