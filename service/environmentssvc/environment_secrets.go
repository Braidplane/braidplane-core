package environmentssvc

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

var environmentSecretKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type environmentSecretPayload struct {
	Value string `json:"value"`
}

func (s *service) UpsertEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertEnvironmentSecret,
	actor model.EventActor,
) (*model.EnvironmentSecret, error) {
	if err := validateEnvironmentSecret(params); err != nil {
		return nil, err
	}
	if s.credentials == nil {
		return nil, xerror.Unexpected().Detail("credential service is required for environment secrets")
	}

	var ret *model.EnvironmentSecret
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		environment, protectionErr := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if protectionErr != nil {
			return xerror.Wrapf("check environment protection: %w", protectionErr)
		}
		if err := protectedenv.ConfirmEnvironment(toEnvironment(environment), params.ProtectedEnvironmentConfirmation); err != nil {
			return err
		}
		payload, err := marshalEnvironmentSecret(params.Value)
		if err != nil {
			return err
		}
		credential, err := s.credentials.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
			Owner: credentialssvc.CredentialOwner{
				Kind:                 credentialssvc.CredentialOwnerKindEnvironmentSecret,
				OrgSlug:              orgSlug,
				EnvironmentSlug:      environmentSlug,
				EnvironmentSecretKey: params.Key,
			},
			CredentialType: credentialssvc.StaticCredentialTypeEnvironmentSecret,
			PlaintextJSON:  payload,
			Actor:          actor,
		})
		if err != nil {
			return xerror.Wrapf("create environment secret credential: %w", err)
		}
		secret, err := s.environments.UpsertEnvironmentSecret(
			ctx,
			tx,
			orgSlug,
			environmentSlug,
			params,
			credential.Slug,
		)
		if err != nil {
			return xerror.Wrapf("upsert environment secret metadata: %w", err)
		}
		ret = secret
		return s.recordEnvironmentSecretEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			params.Key,
			model.EventActionUpdate,
			environment.Protected,
		)
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (s *service) GetEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.EnvironmentSecret, error) {
	if err := validateEnvironmentSecretKey(key); err != nil {
		return nil, err
	}

	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.EnvironmentSecret, error) {
		secret, err := s.environments.FindEnvironmentSecret(ctx, tx, orgSlug, environmentSlug, key)
		if err != nil {
			return nil, xerror.Wrapf("get environment secret: %w", err)
		}
		return secret, nil
	})
}

func (s *service) ListEnvironmentSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.EnvironmentSecret], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortUpdatedAt)

	var items []*model.EnvironmentSecret
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug); err != nil {
			return xerror.Wrapf("check environment before listing secrets: %w", err)
		}
		var listErr error
		items, listErr = s.environments.ListEnvironmentSecrets(ctx, tx, orgSlug, environmentSlug, queryReq)
		if listErr != nil {
			return xerror.Wrapf("list environment secrets: %w", listErr)
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.EnvironmentSecret]{}, err
	}

	items, hasMore := model.FinalizePage(items, normalized)
	secrets := make([]model.EnvironmentSecret, len(items))
	for i, item := range items {
		secrets[i] = *item
	}
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		secrets,
		normalized,
		hasMore,
		func(item model.EnvironmentSecret, _ string) (string, string) {
			return item.UpdatedAt.Format(time.RFC3339Nano), item.Key
		},
	)

	return model.ListResponse[model.EnvironmentSecret]{
		Items:      secrets,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) DeleteEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if err := validateEnvironmentSecretKey(key); err != nil {
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
		if _, err := s.environments.FindEnvironmentSecret(ctx, tx, orgSlug, environmentSlug, key); err != nil {
			return xerror.Wrapf("find environment secret before delete: %w", err)
		}
		if err := s.revokeEnvironmentSecretCredential(ctx, orgSlug, environmentSlug, key, actor); err != nil {
			return xerror.Wrapf("revoke environment secret credential: %w", err)
		}
		if err := s.environments.DeleteEnvironmentSecret(ctx, tx, orgSlug, environmentSlug, key); err != nil {
			return xerror.Wrapf("delete environment secret: %w", err)
		}
		return s.recordEnvironmentSecretEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			key,
			model.EventActionDelete,
			environment.Protected,
		)
	})
}

func (s *service) ResolveRuntimeSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	if s.credentials == nil {
		return nil, xerror.Unexpected().Detail("credential service is required for environment secrets")
	}

	var values []model.RuntimeSecretValue
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug); err != nil {
			return xerror.Wrapf("check environment before resolving secrets: %w", err)
		}
		secrets, err := s.environments.ListAllEnvironmentSecrets(ctx, tx, orgSlug, environmentSlug)
		if err != nil {
			return xerror.Wrapf("list runtime environment secrets: %w", err)
		}
		values = make([]model.RuntimeSecretValue, 0, len(secrets))
		for _, secret := range secrets {
			runtimeCredential, err := s.credentials.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{
				Owner: credentialssvc.CredentialOwner{
					Kind:                 credentialssvc.CredentialOwnerKindEnvironmentSecret,
					OrgSlug:              orgSlug,
					EnvironmentSlug:      environmentSlug,
					EnvironmentSecretKey: secret.Key,
				},
			})
			if err != nil {
				return xerror.Wrapf("resolve environment secret %q: %w", secret.Key, err)
			}
			value, err := unmarshalEnvironmentSecret(runtimeCredential.PlaintextJSON)
			if err != nil {
				return err
			}
			values = append(values, model.RuntimeSecretValue{Key: secret.Key, Value: value})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

// revokeEnvironmentSecretCredential tombstones a secret value through the shared credential lifecycle.
func (s *service) revokeEnvironmentSecretCredential(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
	actor model.EventActor,
) error {
	if s.credentials == nil {
		return xerror.Unexpected().Detail("credential service is required for environment secrets")
	}
	_, err := s.credentials.Revoke(ctx, credentialssvc.RevokeParams{
		Owner: credentialssvc.CredentialOwner{
			Kind:                 credentialssvc.CredentialOwnerKindEnvironmentSecret,
			OrgSlug:              orgSlug,
			EnvironmentSlug:      environmentSlug,
			EnvironmentSecretKey: key,
		},
		Actor: actor,
	})
	return err
}

func validateEnvironmentSecret(params model.UpsertEnvironmentSecret) error {
	if err := validate.Default().Struct(params); err != nil {
		return xerror.ValidationFailed().With(err)
	}
	return validateEnvironmentSecretKey(params.Key)
}

func validateEnvironmentSecretKey(key string) error {
	if !environmentSecretKeyPattern.MatchString(key) {
		return ErrInvalidEnvironmentSecret.Detail("environment secret key must match [A-Za-z_][A-Za-z0-9_]*")
	}
	return nil
}

func marshalEnvironmentSecret(value string) ([]byte, error) {
	payload, err := json.Marshal(environmentSecretPayload{Value: value})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return payload, nil
}

func unmarshalEnvironmentSecret(payload []byte) (string, error) {
	var secret environmentSecretPayload
	if err := json.Unmarshal(payload, &secret); err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return secret.Value, nil
}
