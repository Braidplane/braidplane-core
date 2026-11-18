package environmentssvc

import (
	"context"
	"slices"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

func (s *service) CompareRuntimeVariables(ctx context.Context, orgSlug slug.Slug, usedKeys []string) (model.RuntimeVariableComparison, error) {
	return postgres.InTx(postgres.WithConsistentSnapshot(ctx), s.txm, func(ctx context.Context, tx postgres.DBTX) (model.RuntimeVariableComparison, error) {
		environments, err := s.environments.ListRuntimeVariableComparisonEnvironments(ctx, tx, orgSlug)
		if err != nil {
			return model.RuntimeVariableComparison{}, err
		}
		definitions, err := s.environments.ListRuntimeVariableDefinitions(ctx, tx, orgSlug)
		if err != nil {
			return model.RuntimeVariableComparison{}, err
		}
		overrides, err := s.environments.ListRuntimeVariableOverridesByOrganization(ctx, tx, orgSlug)
		if err != nil {
			return model.RuntimeVariableComparison{}, err
		}
		slugs := make([]slug.Slug, len(environments))
		for i, environment := range environments {
			slugs[i] = environment.Slug
		}
		return model.CompareRuntimeVariables(slugs, definitions, overrides, usedKeys), nil
	})
}

func (s *service) UpsertRuntimeVariableDefinition(ctx context.Context, orgSlug slug.Slug, definition model.RuntimeVariableDefinition, confirmations []slug.Slug, actor model.EventActor) (*model.RuntimeVariableDefinition, error) {
	if err := validateRuntimeVariableDefinition(definition); err != nil {
		return nil, err
	}
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.RuntimeVariableDefinition, error) {
		if err := s.confirmRuntimeVariableDefinitionChange(ctx, tx, orgSlug, confirmations); err != nil {
			return nil, err
		}
		saved, err := s.environments.UpsertRuntimeVariableDefinition(ctx, tx, orgSlug, definition)
		if err != nil {
			return nil, err
		}
		return saved, s.recordRuntimeVariableDefinitionEvent(ctx, tx, orgSlug, actor, definition.Key, "upsert")
	})
}

func (s *service) DeleteRuntimeVariableDefinition(ctx context.Context, orgSlug slug.Slug, key string, confirmations []slug.Slug, actor model.EventActor) error {
	if err := validateRuntimeVariableKey(key); err != nil {
		return err
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.confirmRuntimeVariableDefinitionChange(ctx, tx, orgSlug, confirmations); err != nil {
			return err
		}
		if err := s.environments.DeleteRuntimeVariableDefinition(ctx, tx, orgSlug, key); err != nil {
			return err
		}
		return s.recordRuntimeVariableDefinitionEvent(ctx, tx, orgSlug, actor, key, "delete")
	})
}

func (s *service) recordRuntimeVariableDefinitionEvent(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, actor model.EventActor, key, operation string) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required runtime variable definition audit event producer is unavailable")
	}
	_, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, runtimeVariableDefinitionEvent{orgSlug, key, operation})
	return err
}

func (s *service) confirmRuntimeVariableDefinitionChange(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, confirmations []slug.Slug) error {
	environments, err := s.environments.ListRuntimeVariableComparisonEnvironments(ctx, tx, orgSlug)
	if err != nil {
		return err
	}
	for _, environment := range environments {
		if environment.Protected && !slices.Contains(confirmations, environment.Slug) {
			return protectedenv.ErrConfirmationRequired.
				WithPublicDetail("Pass protected_environment_confirmations with the exact slug of each protected environment.").
				Detailf("confirmation must include environment %s", environment.Slug)
		}
	}
	return nil
}

func validateRuntimeVariableDefinition(definition model.RuntimeVariableDefinition) error {
	if err := validateRuntimeVariableKey(definition.Key); err != nil {
		return err
	}
	if len(definition.Key) > 100 {
		return ErrInvalidRuntimeVariable.Detail("runtime variable key must be at most 100 characters")
	}
	if definition.Description != nil && utf8.RuneCountInString(*definition.Description) > 2000 {
		return ErrInvalidRuntimeVariable.Detail("runtime variable description must be at most 2000 characters")
	}
	if definition.ValueType == model.RuntimeVariableValueTypeUnknown || !slices.Contains(model.RuntimeVariableValueTypeValues(), definition.ValueType) {
		return ErrInvalidRuntimeVariable.Detail("unsupported runtime variable value_type")
	}
	if definition.DefaultValue == nil {
		return nil
	}
	if utf8.RuneCountInString(*definition.DefaultValue) > 4000 {
		return ErrInvalidRuntimeVariable.Detail("runtime variable default must be at most 4000 characters")
	}
	return validateRuntimeVariableValue(definition.ValueType, *definition.DefaultValue)
}
