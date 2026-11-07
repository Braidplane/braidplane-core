package orgsvc

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
)

const retentionSettingsKey = "retention"

func (s *service) GetRetentionPolicy(
	ctx context.Context,
	orgSlug slug.Slug,
) (model.OrganizationRetentionPolicy, error) {
	var policy model.OrganizationRetentionPolicy
	err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		org, err := s.orgsRepo.FindBySlug(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("get retention organization: %w", err)
		}
		policy, err = retentionsvc.PolicyFromSettings(org.Settings)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		return nil
	})
	return policy, err
}

func (s *service) UpdateRetentionPolicy(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	policy model.OrganizationRetentionPolicy,
) (model.OrganizationRetentionPolicy, error) {
	if err := retentionsvc.ValidatePolicy(policy); err != nil {
		return model.OrganizationRetentionPolicy{}, err
	}

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		org, err := s.orgsRepo.FindBySlugForUpdate(ctx, tx, orgSlug)
		if err != nil {
			return xerror.Wrapf("find retention organization: %w", err)
		}
		settings, err := settingsWithRetentionPolicy(org.Settings, policy)
		if err != nil {
			return err
		}
		if err := s.orgsRepo.UpdateSettings(ctx, tx, &organizations.UpdateSettingsParams{
			Slug: orgSlug, Settings: settings,
		}); err != nil {
			return xerror.Wrapf("update organization retention: %w", err)
		}
		if err := s.enqueueRequiredOrganizationAuditEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			organizationRetentionUpdatedEvent{orgSlug: orgSlug, policy: policy},
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.OrganizationRetentionPolicy{}, err
	}
	return policy, nil
}

func settingsWithRetentionPolicy(raw []byte, policy model.OrganizationRetentionPolicy) ([]byte, error) {
	settings := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, xerror.Unexpected().With(err).Detail("organization settings are invalid")
		}
		if settings == nil {
			settings = map[string]json.RawMessage{}
		}
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	settings[retentionSettingsKey] = encoded
	updated, err := json.Marshal(settings)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return updated, nil
}
