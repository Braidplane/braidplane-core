package environmentssvc

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

var runtimeVariableKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var secretRuntimeVariableKeyFragments = []string{
	"secret",
	"password",
	"passwd",
	"token",
	"api_key",
	"apikey",
	"access_key",
	"accesskey",
	"private_key",
	"privatekey",
	"credential",
	"auth",
}

func (s *service) UpsertRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertRuntimeVariable,
	actor model.EventActor,
) (*model.RuntimeVariable, error) {
	if err := validateRuntimeVariable(params); err != nil {
		return nil, err
	}

	var ret *model.RuntimeVariable
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		environment, protectionErr := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if protectionErr != nil {
			return xerror.Wrapf("check environment protection: %w", protectionErr)
		}
		if err := protectedenv.ConfirmEnvironment(toEnvironment(environment), params.ProtectedEnvironmentConfirmation); err != nil {
			return err
		}
		variable, err := s.environments.UpsertRuntimeVariable(
			ctx,
			tx,
			orgSlug,
			environmentSlug,
			params,
		)
		if err != nil {
			return xerror.Wrapf("upsert runtime variable: %w", err)
		}
		ret = variable
		return s.recordEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			model.EventActionUpdate,
			environment.Protected,
		)
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s *service) GetRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.RuntimeVariable, error) {
	if err := validateRuntimeVariableKey(key); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.RuntimeVariable, error) {
		variable, err := s.environments.FindRuntimeVariable(ctx, tx, orgSlug, environmentSlug, key)
		if err != nil {
			return nil, xerror.Wrapf("get runtime variable: %w", err)
		}
		return variable, nil
	})
}

func (s *service) ListRuntimeVariables(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.RuntimeVariable], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortUpdatedAt)

	var items []*model.RuntimeVariable
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var listErr error
		items, listErr = s.environments.ListRuntimeVariables(ctx, tx, orgSlug, environmentSlug, queryReq)
		if listErr != nil {
			return xerror.Wrapf("list runtime variables: %w", listErr)
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.RuntimeVariable]{}, err
	}

	items, hasMore := model.FinalizePage(items, normalized)
	variables := make([]model.RuntimeVariable, len(items))
	for i, item := range items {
		variables[i] = *item
	}
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		variables,
		normalized,
		hasMore,
		func(item model.RuntimeVariable, _ string) (string, string) {
			return item.UpdatedAt.Format(time.RFC3339Nano), item.Key
		},
	)

	return model.ListResponse[model.RuntimeVariable]{
		Items:      variables,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) DeleteRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if err := validateRuntimeVariableKey(key); err != nil {
		return err
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		environment, protectionErr := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if protectionErr != nil {
			return xerror.Wrapf("check environment protection: %w", protectionErr)
		}
		if err := protectedenv.ConfirmEnvironment(toEnvironment(environment), confirmation); err != nil {
			return err
		}
		if err := s.environments.DeleteRuntimeVariable(ctx, tx, orgSlug, environmentSlug, key); err != nil {
			return xerror.Wrapf("delete runtime variable: %w", err)
		}
		return s.recordEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			model.EventActionUpdate,
			environment.Protected,
		)
	})
}

func validateRuntimeVariable(params model.UpsertRuntimeVariable) error {
	if err := validate.Default().Struct(params); err != nil {
		return xerror.ValidationFailed().With(err)
	}
	if err := validateRuntimeVariableKey(params.Key); err != nil {
		return err
	}
	if err := validateRuntimeVariableValue(params.ValueType, params.Value); err != nil {
		return err
	}
	return nil
}

func validateRuntimeVariableKey(key string) error {
	if !runtimeVariableKeyPattern.MatchString(key) {
		return ErrInvalidRuntimeVariable.Detail("runtime variable key must match [A-Za-z_][A-Za-z0-9_]*")
	}
	normalized := strings.ToLower(key)
	for _, fragment := range secretRuntimeVariableKeyFragments {
		if strings.Contains(normalized, fragment) {
			return ErrInvalidRuntimeVariable.Detail("runtime variables are non-secret; store credentials on concrete data sources or secret storage")
		}
	}
	return nil
}

func validateRuntimeVariableValue(valueType model.RuntimeVariableValueType, value string) error {
	switch valueType {
	case model.RuntimeVariableValueTypeText:
		return nil
	case model.RuntimeVariableValueTypeInteger:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return ErrInvalidRuntimeVariable.With(err).Detail("integer runtime variable value must be a base-10 integer")
		}
	case model.RuntimeVariableValueTypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return ErrInvalidRuntimeVariable.With(err).Detail("boolean runtime variable value must be true or false")
		}
	case model.RuntimeVariableValueTypeDate:
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return ErrInvalidRuntimeVariable.With(err).Detail("date runtime variable value must use YYYY-MM-DD")
		}
	case model.RuntimeVariableValueTypeUUID:
		if _, err := uuid.FromString(value); err != nil {
			return ErrInvalidRuntimeVariable.With(err).Detail("uuid runtime variable value must be a valid UUID")
		}
	default:
		return ErrInvalidRuntimeVariable.Detailf("unsupported runtime variable value_type %q", valueType)
	}
	return nil
}
